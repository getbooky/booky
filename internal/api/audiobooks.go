package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/getbooky/booky/internal/audiobook"
	"github.com/getbooky/booky/internal/importer"
	"github.com/getbooky/booky/internal/readalong"
)

// Audiobooks follow the book's file rule: whoever may download a book may
// hear it, add an audiobook to it, and read along. Choosing a server path
// to import from, replacing the read-along, and deleting are admin work.

// maxAudiobookUpload caps one upload — a long unabridged book at a high
// bitrate is a few gigabytes.
const maxAudiobookUpload = 8 << 30

// audioImport tracks an import running in the background: joining forty
// MP3s into one M4B takes minutes, far longer than an HTTP request should.
type audioImport struct {
	mu     sync.Mutex
	err    string
	done   bool
	cancel context.CancelFunc
}

func (s *Server) importState(bookID int64) *audioImport {
	if v, ok := s.audioImports.Load(bookID); ok {
		return v.(*audioImport)
	}
	return nil
}

// ebookFile is the book's file on the shelf, if it has one.
func (s *Server) ebookFile(bookID int64) string {
	var p string
	_ = s.db.QueryRow(`SELECT COALESCE(file_path, '') FROM library_books WHERE book_id = ? LIMIT 1`, bookID).Scan(&p)
	return p
}

// audioHome is the path the audiobook is named after: the ebook's, or —
// before the ebook arrives — where the naming scheme will put it.
func (s *Server) audioHome(bookID int64) (string, error) {
	if p := s.ebookFile(bookID); p != "" {
		return p, nil
	}
	var root string
	if err := s.db.QueryRow(`SELECT root_path FROM libraries WHERE id = ?`, s.Catalog.TheLibrary()).Scan(&root); err != nil {
		return "", errors.New("set up the library first")
	}
	book, err := s.Catalog.GetBook(bookID)
	if err != nil {
		return "", err
	}
	if book.LibraryID == 0 {
		return "", errors.New("add the book to the library first")
	}
	return filepath.Join(root, importer.RenderName(s.Settings.Get("naming_scheme"), book)+".epub"), nil
}

// EPUBFor is the read-along worker's source text: the book's file, when it
// is an EPUB.
func (s *Server) EPUBFor(bookID int64) (string, error) {
	p := s.ebookFile(bookID)
	if p == "" || !strings.EqualFold(filepath.Ext(p), ".epub") {
		return "", audiobook.ErrNoEPUB
	}
	if _, err := os.Stat(p); err != nil {
		return "", err
	}
	return p, nil
}

// requireFile guards everything an audiobook does: the book's file rule.
func (s *Server) requireFile(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return 0, false
	}
	if !s.mayFile(s.access(r), id) {
		writeErr(w, http.StatusForbidden, errors.New("you don't have access to this book"))
		return 0, false
	}
	return id, true
}

type audiobookView struct {
	*audiobook.Audiobook
	FilePath    string `json:"filePath,omitempty"` // admins only
	ReadAlong   bool   `json:"readAlong"`
	Importing   bool   `json:"importing,omitempty"`
	ImportError string `json:"importError,omitempty"`
}

// GET /books/{id}/audiobook — the audiobook and its read-along's state.
func (s *Server) handleAudiobook(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireFile(w, r)
	if !ok {
		return
	}
	view := audiobookView{}
	if st := s.importState(id); st != nil {
		st.mu.Lock()
		view.Importing, view.ImportError = !st.done, st.err
		st.mu.Unlock()
	}
	a, err := s.Audiobooks.Get(id)
	switch {
	case errors.Is(err, audiobook.ErrNone):
		if !view.Importing && view.ImportError == "" {
			writeErr(w, http.StatusNotFound, err)
			return
		}
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	default:
		view.Audiobook = a
		if s.access(r).admin() {
			view.FilePath = a.Path
		}
		if a.Status == audiobook.Ready {
			_, err := os.Stat(filepath.Join(s.ReadAlong.OutDir(id), readalong.EditionFile)) //nolint:gosec // G703: the path is built from a numeric id
			view.ReadAlong = err == nil
		}
	}
	writeJSON(w, http.StatusOK, view)
}

// audioExts are the upload names accepted — the bytes are checked as well.
var audioExts = map[string]string{".mp3": ".mp3", ".m4b": ".m4b", ".m4a": ".m4a", ".mp4": ".m4a"}

// POST /books/{id}/audiobook — upload one audiobook file, or its parts in
// order (several "file" fields), from the browser.
func (s *Server) handleAudiobookUpload(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireFile(w, r)
	if !ok {
		return
	}
	// anyone who can see the book may add its audiobook; replacing one
	// deletes the old file, and deleting is for admins
	if !s.access(r).admin() {
		if _, err := s.Audiobooks.Get(id); !errors.Is(err, audiobook.ErrNone) {
			writeErr(w, http.StatusForbidden, errors.New("only an admin can replace an audiobook"))
			return
		}
	}
	if s.ReadAlong.Tools.FFprobe == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("audiobooks need ffmpeg, which ships in the Docker image"))
		return
	}
	if _, err := s.audioHome(id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if st := s.importState(id); st != nil && !st.done {
		writeErr(w, http.StatusConflict, errors.New("an audiobook is already being added to this book"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAudiobookUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	base := s.Settings.Get("downloads_dir")
	if base == "" {
		base = "/data/downloads/booky"
	}
	base = filepath.Join(base, "uploads")
	if err := os.MkdirAll(base, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	dir, err := os.MkdirTemp(base, "audiobook-")
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(dir)
		}
	}()

	type upload struct{ path, name string }
	var files []upload
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("upload: %w", err))
			return
		}
		if part.FormName() != "file" {
			continue
		}
		name := filepath.Base(strings.TrimSpace(part.FileName()))
		ext, ok := audioExts[strings.ToLower(filepath.Ext(name))]
		if !ok {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("%q isn't an MP3, M4A or M4B file", name))
			return
		}
		if len(files) >= 1000 {
			writeErr(w, http.StatusBadRequest, errors.New("too many files"))
			return
		}
		// the stored name is server-made; the client's only becomes a
		// chapter title
		dst := filepath.Join(dir, fmt.Sprintf("%04d%s", len(files)+1, ext))
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600) //nolint:gosec // G304: temp dir + server-made name
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		_, err = io.Copy(f, part)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("upload: %w", err))
			return
		}
		files = append(files, upload{dst, name})
	}
	if len(files) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("no audio file in the upload"))
		return
	}
	// the browser decides the order files arrive in; put them in reading
	// order by their names, the way a folder import does
	sortUploads := make([]readalong.Part, 0, len(files))
	for _, u := range files {
		f, err := readalong.Detect(u.path)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("%s: %w", u.name, err))
			return
		}
		sortUploads = append(sortUploads, readalong.Part{Path: u.path, Format: f,
			Title: strings.TrimSuffix(u.name, filepath.Ext(u.name))})
	}
	audiobook.SortParts(sortUploads)
	if err := s.startAudioImport(id, sortUploads, true, dir); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	keep = true // the import owns the temp dir now
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "importing"})
}

// POST /books/{id}/audiobook/import {"path": "..."} — take an audiobook
// already on the server: a file, or a folder of parts. Admin only, and
// fenced like every server path. A single file is moved into place; a
// folder's parts are joined into one M4B and the originals left alone.
func (s *Server) handleAudiobookImport(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if s.ReadAlong.Tools.FFprobe == "" {
		writeErr(w, http.StatusServiceUnavailable, errors.New("audiobooks need ffmpeg, which ships in the Docker image"))
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path required"))
		return
	}
	if _, err := s.audioHome(id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	src, err := s.resolveImportSource(s.access(r), filepath.Clean(strings.TrimSpace(req.Path)))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// a series folder is split into its books, each filed with its own
	if split, err := audiobook.SplitByBook(src, s.audioCandidates(id)); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	} else if split.Bundle {
		if st := s.importState(id); st != nil && !st.done {
			writeErr(w, http.StatusConflict, errImportBusy)
			return
		}
		//nolint:gosec // G118: the import must outlive the HTTP request by design
		go func() {
			if err := s.ImportAudioFrom(context.Background(), id, src); err != nil {
				log.Printf("audiobook: series import for book %d: %v", id, err) //nolint:gosec // G706: a number and our own error
			}
		}()
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "importing"})
		return
	}
	parts, err := audiobook.Parts(src)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.startAudioImport(id, parts, len(parts) == 1, ""); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "importing"})
}

// claimImport registers an audiobook import for the book, so the page can
// show it and a second one can't start alongside. False when one is running.
func (s *Server) claimImport(bookID int64, cancel context.CancelFunc) (*audioImport, bool) {
	st := &audioImport{cancel: cancel}
	if prev, busy := s.audioImports.LoadOrStore(bookID, st); busy {
		p := prev.(*audioImport)
		p.mu.Lock()
		running := !p.done
		p.mu.Unlock()
		if running {
			return nil, false
		}
		s.audioImports.Store(bookID, st)
	}
	return st, true
}

// finishImport records how an import ended. A clean finish drops the
// record; a failure stays, so the page can say what went wrong.
func (s *Server) finishImport(bookID int64, st *audioImport, errMsg string) {
	st.mu.Lock()
	st.done, st.err = true, errMsg
	st.mu.Unlock()
	if errMsg == "" {
		s.audioImports.CompareAndDelete(bookID, st)
	}
}

var errImportBusy = errors.New("an audiobook is already being added to this book")

// startAudioImport installs an audiobook in the background and queues its
// read-along. cleanup, when set, is a temp dir to remove afterwards.
func (s *Server) startAudioImport(bookID int64, parts []readalong.Part, move bool, cleanup string) error {
	ctx, cancel := context.WithCancel(context.Background())
	st, ok := s.claimImport(bookID, cancel)
	if !ok {
		cancel()
		return errImportBusy
	}
	go func() {
		defer cancel()
		if cleanup != "" {
			defer os.RemoveAll(cleanup)
		}
		msg, _ := s.installAudio(ctx, bookID, parts, move)
		s.finishImport(bookID, st, msg)
	}()
	return nil
}

// ImportAudioFrom files a downloaded audiobook — a file, or a folder of
// parts — for a book, and queues its read-along. It returns once the file
// is in place; the acquisition engine calls it for finished downloads. The
// source is left for the caller to clean up, except a single file, which
// is moved.
func (s *Server) ImportAudioFrom(ctx context.Context, bookID int64, path string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	st, ok := s.claimImport(bookID, cancel)
	if !ok {
		return errImportBusy
	}
	split, err := audiobook.SplitByBook(path, s.audioCandidates(bookID))
	if err != nil {
		s.finishImport(bookID, st, "The download had no usable audio.")
		return err
	}
	if split.Bundle {
		msg, err := s.installSeries(ctx, bookID, split)
		s.finishImport(bookID, st, msg)
		return err
	}
	parts, err := audiobook.Parts(path)
	if err != nil {
		s.finishImport(bookID, st, "The download had no usable audio.")
		return err
	}
	msg, err := s.installAudio(ctx, bookID, parts, len(parts) == 1)
	s.finishImport(bookID, st, msg)
	return err
}

// audioCandidates are the books a download for bookID might also hold: the
// author's books in the library. The book itself comes first.
func (s *Server) audioCandidates(bookID int64) []audiobook.BookRef {
	book, err := s.Catalog.GetBook(bookID)
	if err != nil {
		return nil
	}
	out := []audiobook.BookRef{{ID: book.ID, Title: book.Title, Author: book.Author}}
	books, err := s.Catalog.ListBooks(book.AuthorID, 0, 0)
	if err != nil {
		return out
	}
	seen := map[int64]bool{book.ID: true}
	for _, b := range books {
		if b.LibraryID == 0 || seen[b.ID] {
			continue
		}
		seen[b.ID] = true
		out = append(out, audiobook.BookRef{ID: b.ID, Title: b.Title, Author: b.Author})
	}
	return out
}

// errNotInDownload: a series pack didn't hold the book it was grabbed for.
var errNotInDownload = errors.New("the download doesn't contain this book")

// installSeries files each library book a series pack holds as that book's
// audiobook. The book it was grabbed for is filed whatever it had before;
// the others only when they have no audiobook yet — a pack never replaces
// one nobody asked to replace. The outcome goes to the book's history, and
// the returned message is shown on its page.
func (s *Server) installSeries(ctx context.Context, bookID int64, split *audiobook.Split) (string, error) {
	title := func(id int64) string {
		if b, err := s.Catalog.GetBook(id); err == nil {
			return b.Title
		}
		return fmt.Sprintf("book %d", id)
	}
	ids := make([]int64, 0, len(split.Books))
	for id := range split.Books {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var filed, kept, failed []string
	var targetMsg string
	var targetErr error
	for _, id := range ids {
		if id != bookID {
			if _, err := s.Audiobooks.Get(id); !errors.Is(err, audiobook.ErrNone) {
				kept = append(kept, title(id))
				continue
			}
		}
		parts := split.Books[id]
		// another book of the pack is claimed while it's being filed, so
		// its page shows the work and an upload can't race it
		var claim *audioImport
		if id != bookID {
			c, ok := s.claimImport(id, func() {})
			if !ok {
				kept = append(kept, title(id))
				continue
			}
			claim = c
		}
		// parts are copied, not moved: the pack's folder is cleaned up as a
		// whole once everything is filed
		msg, err := s.installAudio(ctx, id, parts, false)
		if claim != nil {
			s.finishImport(id, claim, msg)
		}
		if id == bookID {
			targetMsg, targetErr = msg, err
		}
		if err != nil {
			failed = append(failed, title(id))
			continue
		}
		filed = append(filed, title(id))
		if id != bookID {
			_ = s.Catalog.AddHistory(id, s.Catalog.TheLibrary(), "imported", "audiobook from a series download, grabbed for "+title(bookID))
		}
	}
	var report []string
	if len(filed) > 0 {
		report = append(report, fmt.Sprintf("filed %d: %s", len(filed), strings.Join(filed, ", ")))
	}
	if len(kept) > 0 {
		report = append(report, "already had an audiobook (kept): "+strings.Join(kept, ", "))
	}
	if len(failed) > 0 {
		report = append(report, "couldn't file: "+strings.Join(failed, ", "))
	}
	if len(split.Unmatched) > 0 {
		report = append(report, "not in your library: "+strings.Join(split.Unmatched, ", "))
	}
	summary := fmt.Sprintf("series download with %d book(s) — %s", len(split.Books)+len(split.Unmatched), strings.Join(report, "; "))
	_ = s.Catalog.AddHistory(bookID, s.Catalog.TheLibrary(), "series download", summary)
	if _, ok := split.Books[bookID]; !ok {
		return "The series download didn't contain this book — " + strings.Join(report, "; ") + ".", errNotInDownload
	}
	return targetMsg, targetErr
}

// installAudio puts an audiobook in place, records it, replaces any old one
// and queues the read-along. On failure it returns a message fit to show
// people; the error, with the detail, goes to the log.
func (s *Server) installAudio(ctx context.Context, bookID int64, parts []readalong.Part, move bool) (string, error) {
	home, err := s.audioHome(bookID)
	if err != nil {
		return err.Error(), err
	}
	inst, err := audiobook.Install(ctx, s.ReadAlong.Tools, parts, home, move)
	if err != nil {
		log.Printf("audiobook: import for book %d: %v", bookID, err) //nolint:gosec // G706: a number and our own error
		if errors.Is(err, readalong.ErrNotAudio) {
			return err.Error(), err
		}
		return "Couldn't add the audiobook — the server log has the details.", err
	}
	old, _ := s.Audiobooks.Get(bookID)
	s.ReadAlong.Forget(bookID) // the old read-along matches the old audio
	if err := s.Audiobooks.Put(bookID, inst.Path, inst.Format, inst.Size, inst.Duration); err != nil {
		log.Printf("audiobook: record book %d: %v", bookID, err) //nolint:gosec // G706: a number and our own error
		return "Couldn't record the audiobook.", err
	}
	if old != nil && old.Path != inst.Path {
		if err := os.Remove(old.Path); err != nil && !os.IsNotExist(err) { //nolint:gosec // G703: a path Booky placed itself, from its own database
			log.Printf("audiobook: remove replaced file: %v", err)
		}
	}
	_ = s.Catalog.AddHistory(bookID, s.Catalog.TheLibrary(), "imported", inst.Path+" (audiobook)")
	s.ReadAlong.Wake()
	return "", nil
}

// POST /books/{id}/audiobook/rebuild — build the read-along again.
func (s *Server) handleAudiobookRebuild(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	s.ReadAlong.Cancel(id)
	if err := s.Audiobooks.Requeue(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	s.ReadAlong.Wake()
	writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})
}

// DELETE /books/{id}/audiobook?file=keep|delete — drop the audiobook (and
// its read-along); file=delete also removes the audio file from disk.
func (s *Server) handleAudiobookDelete(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	mode := r.URL.Query().Get("file")
	if mode != "keep" && mode != "delete" {
		writeErr(w, http.StatusBadRequest, errors.New("file must be keep or delete"))
		return
	}
	if st := s.importState(id); st != nil {
		st.cancel()
		s.audioImports.Delete(id)
	}
	a, err := s.Audiobooks.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	s.dropAudiobook(a, mode == "delete")
	_ = s.Catalog.AddHistory(id, s.Catalog.TheLibrary(), "removed", s.bookTitle(id)+" — audiobook removed"+
		map[bool]string{true: ", file deleted", false: ", file kept"}[mode == "delete"])
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// dropAudiobook forgets a book's audiobook and read-along, deleting the
// audio file too when asked.
func (s *Server) dropAudiobook(a *audiobook.Audiobook, deleteFile bool) {
	s.ReadAlong.Forget(a.BookID)
	if err := s.Audiobooks.Delete(a.BookID); err != nil {
		log.Printf("audiobook: delete row for book %d: %v", a.BookID, err) //nolint:gosec // G706: a number and our own error
	}
	if deleteFile {
		if err := os.Remove(a.Path); err != nil && !os.IsNotExist(err) { //nolint:gosec // G703: a path Booky placed itself, from its own database
			log.Printf("audiobook: delete file: %v", err)
		}
	}
}

// GET /books/{id}/audiobook/file — the audiobook itself, for listening
// without a read-along. Range requests work, so a player can seek.
func (s *Server) handleAudiobookFile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireFile(w, r)
	if !ok {
		return
	}
	a, err := s.Audiobooks.Get(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	ctype := "audio/mpeg"
	if a.Format == readalong.MP4 {
		ctype = "audio/mp4"
	}
	w.Header().Set("Content-Type", ctype)
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeFile(w, r, a.Path) //nolint:gosec // G703: a path Booky placed itself, from its own database
}

// readAlongFile is the whitelist of what a built read-along serves: the
// edition, the timing file, and its numbered audio chunks. Nothing from the
// request reaches the filesystem except a name that matched it.
var readAlongFile = regexp.MustCompile(`^(readalong\.epub|timing\.json|audio/[0-9]{4}\.mp3)$`)

// GET /books/{id}/readalong/{file...}
func (s *Server) handleReadAlongFile(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireFile(w, r)
	if !ok {
		return
	}
	name := r.PathValue("file")
	if !readAlongFile.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	a, err := s.Audiobooks.Get(id)
	if err != nil || a.Status != audiobook.Ready {
		writeErr(w, http.StatusNotFound, errors.New("no read-along for this book yet"))
		return
	}
	switch {
	case strings.HasSuffix(name, ".epub"):
		w.Header().Set("Content-Type", "application/epub+zip")
	case strings.HasSuffix(name, ".json"):
		w.Header().Set("Content-Type", "application/json")
	default:
		w.Header().Set("Content-Type", "audio/mpeg")
	}
	w.Header().Set("Cache-Control", "private, no-cache")
	http.ServeFile(w, r, filepath.Join(s.ReadAlong.OutDir(id), filepath.FromSlash(name))) //nolint:gosec // G703: name passed the whitelist above
}

// GET/PUT /books/{id}/listen — where the account is in the audiobook.
func (s *Server) handleGetListen(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireFile(w, r)
	if !ok {
		return
	}
	pos, at, err := s.Audiobooks.Position(s.progressUserID(r), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"position": pos, "updatedAt": at})
}

func (s *Server) handlePutListen(w http.ResponseWriter, r *http.Request) {
	id, ok := s.requireFile(w, r)
	if !ok {
		return
	}
	var req struct {
		Position float64 `json:"position"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if math.IsNaN(req.Position) || math.IsInf(req.Position, 0) || req.Position < 0 || req.Position > 1e7 {
		writeErr(w, http.StatusBadRequest, errors.New("position must be seconds from the start"))
		return
	}
	if err := s.Audiobooks.SetPosition(s.progressUserID(r), id, req.Position); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
