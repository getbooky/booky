package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/importer"
	"github.com/getbooky/booky/internal/metadata"
)

// Per-book actions behind the grid/detail ⋯ menus.

func (s *Server) seedFromBook(bookID int64) (metadata.BookMeta, error) {
	b, err := s.Catalog.GetBook(bookID)
	if err != nil {
		return metadata.BookMeta{}, errors.New("book not found")
	}
	return metadata.BookMeta{
		Provider: "refresh",
		Title:    b.Title,
		Authors:  []string{b.Author},
		// The stored series rides along so enrich treats it as known: a fuzzy
		// provider answer can only fill an EMPTY series, never replace the one
		// already on the book. (A same-titled book by another author once
		// swapped a correct series for its own this way.)
		SeriesName:  b.SeriesName,
		SeriesIndex: b.SeriesNum,
		ISBN13:      b.ISBN13,
		GoodreadsID: b.GoodreadsID,
		HardcoverID: b.HardcoverID,
	}, nil
}

// POST /books/{id}/refresh — re-resolve one book against Hardcover. A book
// without a Hardcover identity is matched and adopted first (what the old
// separate re-match button did); one that has it is refreshed from it.
// Locked fields survive (UpsertBook enforces the locks).
func (s *Server) handleBookRefresh(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireBook(w, r, id) {
		return
	}
	book, err := s.Catalog.GetBook(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("book not found"))
		return
	}
	// no canonical identity yet: adopt one, silently falling back to a plain
	// enrich when no confident match exists (refresh must never hard-fail on
	// a messy title the way the explicit re-match endpoint does). A locked
	// hardcoverId is the user saying "stop guessing" — no adoption attempt.
	if book.HardcoverID == "" && !book.FieldLocks["hardcoverId"] {
		if adopted, err := s.rematchToHardcover(r.Context(), id); err == nil && adopted != nil {
			writeJSON(w, http.StatusOK, adopted)
			return
		}
	}
	seed, err := s.seedFromBook(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	// A book that carries its canonical Hardcover identity is refreshed FROM
	// that identity: fetch the exact record and make it the seed, so the
	// canonical title, series, date, and cover flow through the upsert (where
	// locked fields still win) instead of merely filling blanks. This is also
	// what makes a hand-corrected Hardcover ID work — edit the id, hit
	// refresh, and THAT book's metadata arrives. Any fetch trouble falls back
	// to the stored seed: refresh degrades to gap-fill rather than failing.
	if book.HardcoverID != "" {
		hc := metadata.NewHardcover(func() string { return s.Settings.Get("hardcover_token") })
		if m, err := hc.FetchByHardcoverID(r.Context(), book.HardcoverID); err == nil && m != nil && m.Title != "" {
			// stored identities Hardcover doesn't know carry over
			m.GoodreadsID = book.GoodreadsID
			if m.ISBN13 == "" {
				m.ISBN13 = book.ISBN13
			}
			seed = *m
		}
	}
	enriched := s.Chain.Enrich(r.Context(), seed)
	if _, err := s.Catalog.UpsertBook(enriched); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// A hand-asked refresh re-fetches the cover instead of keeping whatever is
	// cached: when the provider's art changes, "Refresh metadata" is where
	// people expect the new one to arrive, and Ensure would skip it forever.
	// A locked cover is the user's own pick and stays. The bulk/weekly refresh
	// still only fills gaps — it must not re-download every cover it touches.
	if !book.FieldLocks["cover"] {
		if err := s.Covers.Replace(r.Context(), id, enriched.CoverURL); err != nil {
			_ = err // the old cover survives; the metadata refresh still succeeded
		}
	}
	updated, err := s.Catalog.GetBook(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// rematchToHardcover resolves a book against Hardcover and, on a confident
// match, adopts it as canonical: identity repointed, clean metadata upserted
// (user-locked fields still win), cached cover replaced. Returns the updated
// book, or nil when no confident match exists.
func (s *Server) rematchToHardcover(ctx context.Context, id int64) (*catalog.Book, error) {
	book, err := s.Catalog.GetBook(id)
	if err != nil {
		return nil, errors.New("book not found")
	}
	hc := metadata.NewHardcover(func() string { return s.Settings.Get("hardcover_token") })
	if !hc.WorksConfigured() {
		return nil, errors.New("hardcover token not configured (Settings → Metadata)")
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var candidates []metadata.BookMeta
	if book.ISBN13 != "" {
		candidates, _ = hc.Search(cctx, metadata.SearchParams{ISBN: book.ISBN13, Limit: 3})
	}
	if len(candidates) == 0 {
		candidates, err = hc.Search(cctx, metadata.SearchParams{Title: book.Title, Author: book.Author, Limit: 5})
		if err != nil {
			return nil, err
		}
	}
	best, ok := metadata.BestConfidentMatch(candidates, book.Title, book.Author)
	if !ok {
		return nil, nil // no confident match — caller decides what that means
	}

	// repoint identity first so the upsert converges on this row even when the
	// book carries no Goodreads id or ISBN
	if err := s.Catalog.OverrideHardcoverID(id, best.HardcoverID); err != nil {
		return nil, err
	}
	// Hardcover is canonical; Goodreads identity and anything Hardcover lacks
	// carry over from the stored record
	best.GoodreadsID = book.GoodreadsID
	if best.ISBN13 == "" {
		best.ISBN13 = book.ISBN13
	}
	if _, err := s.Catalog.UpsertBook(best); err != nil {
		return nil, err
	}
	// adopt the matched cover: drop the cache so Ensure re-fetches — unless
	// the user locked a custom cover in place
	if best.CoverURL != "" && !book.FieldLocks["cover"] {
		if err := s.Covers.Remove(id); err == nil {
			if err := s.Covers.Ensure(cctx, id, best.CoverURL); err != nil {
				_ = err // cover retries on the next refresh; the re-match itself succeeded
			}
		}
	}
	updated, err := s.Catalog.GetBook(id)
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// POST /books/{id}/rematch — kept for compatibility; refresh now adopts
// automatically, this is the explicit form that reports a failed match.
func (s *Server) handleBookRematch(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireBook(w, r, id) {
		return
	}
	updated, err := s.rematchToHardcover(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if updated == nil {
		writeErr(w, http.StatusNotFound, errors.New("no confident Hardcover match for this title/author — try editing the title first"))
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// POST /books/{id}/cover — re-fetch the cover from the providers, replacing
// the cached one. The order matters: ask the providers first and only swap
// the file once the new image is in hand. Deleting up front meant a book
// whose providers had no cover (or a download that failed) was left with no
// cover at all, having just thrown away the one it had.
func (s *Server) handleBookCoverRegen(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireBook(w, r, id) {
		return
	}
	if b, err := s.Catalog.GetBook(id); err == nil && b.FieldLocks["cover"] {
		writeErr(w, http.StatusConflict, errors.New("the cover is locked — unlock it in Edit metadata first"))
		return
	}
	seed, err := s.seedFromBook(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	enriched := s.Chain.Enrich(r.Context(), seed)
	if enriched.CoverURL == "" {
		writeErr(w, http.StatusNotFound, errors.New("no provider has a cover for this book"))
		return
	}
	if err := s.Covers.Replace(r.Context(), id, enriched.CoverURL); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "regenerated"})
}

// PUT /books/{id}/cover/custom — the user supplies the cover themselves,
// either as an uploaded image (multipart field "file") or a URL (JSON
// {"url": …}). The cover is replaced and auto-locked so refreshes and
// rematches leave it alone; unlock it in Edit metadata to hand it back to
// the providers.
func (s *Server) handleBookCoverCustom(w http.ResponseWriter, r *http.Request) {
	// supplying the artwork by hand is a metadata edit (and auto-locks the
	// field), so it lands on the admin side of the line
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if _, err := s.Catalog.GetBook(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}

	ct := r.Header.Get("Content-Type")
	switch {
	case strings.HasPrefix(ct, "multipart/form-data"):
		// bound the WHOLE request body, not just the in-memory portion —
		// covers are ≤10MB, so 12MB of multipart is plenty
		r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
		//nolint:gosec // G120: the body IS bounded — MaxBytesReader on the line above
		if err := r.ParseMultipartForm(12 << 20); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("multipart field 'file' required"))
			return
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 10<<20))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if len(data) == 0 {
			writeErr(w, http.StatusBadRequest, errors.New("empty image upload"))
			return
		}
		if err := verifyImageBytes(data); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if err := s.Covers.SaveBytes(id, data); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	default:
		var req struct {
			URL string `json:"url"`
		}
		if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.URL) == "" {
			writeErr(w, http.StatusBadRequest, errors.New("provide an image upload or a url"))
			return
		}
		if err := s.Covers.ForceURL(r.Context(), id, strings.TrimSpace(req.URL)); err != nil {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
	}

	// a hand-picked cover is a decision — lock it so nothing overwrites it
	if err := s.Catalog.SetFieldLock(id, "cover", true); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "locked": true})
}

// POST /books/{id}/import — manual import: deliver a file already on disk
// into a library for this book, with the same renaming, metadata write, and
// cross-library hardlinking as an automatic import. Two source shapes:
// JSON {libraryId, path} points at a file/folder already on the SERVER
// (a directory is accepted — the best book file inside is picked), while a
// multipart body (fields libraryId + file) UPLOADS the book from the device
// the browser is on; the upload lands in the downloads dir and delivers
// from there.
func (s *Server) handleBookManualImport(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}

	var libraryID, queueID int64
	var src string
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		libraryID, queueID, src, err = s.receiveBookUpload(w, r, id)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	} else {
		// The JSON form names a path on the SERVER rather than uploading one.
		// A scoped user has no need for it — their route in is the browser
		// upload above — so it stays with the admin, and the path itself is
		// fenced to the media mount so even an admin can't point it at
		// /config or /etc and pull the result back out over HTTP.
		if !s.requireAdmin(w, r) {
			return
		}
		var req struct {
			LibraryID int64  `json:"libraryId"`
			Path      string `json:"path"`
			// QueueID, when set, ties this import to a failed queue row: a
			// successful delivery marks that row done so Activity reflects
			// the hand-resolved outcome.
			QueueID int64 `json:"queueId"`
		}
		if err := decodeJSON(r, &req); err != nil || req.LibraryID == 0 || strings.TrimSpace(req.Path) == "" {
			writeErr(w, http.StatusBadRequest, errors.New("libraryId and path required"))
			return
		}
		libraryID, queueID = req.LibraryID, req.QueueID
		a := s.access(r)
		src, err = s.resolveImportSource(a, strings.TrimSpace(req.Path))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		// gosec's taint analysis follows req.Path here and can't see through
		// the helper that cleared it. src is resolveImportSource's OUTPUT: the
		// symlink-resolved path, already proven to sit inside an allowed root.
		info, err := os.Stat(src) //nolint:gosec // G703: fenced by resolveImportSource, see above
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("can't read %s: %w", src, err))
			return
		}
		if info.IsDir() {
			src, err = importer.FindBookFile(src)
			if err != nil {
				writeErr(w, http.StatusBadRequest, fmt.Errorf("no book file found in the folder: %w", err))
				return
			}
			// re-fence the picked file: a symlink inside the folder must not
			// lead back out of the mount
			if src, err = s.resolveImportSource(a, src); err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
		}
		// the upload path already enforces this; a named path deserves the
		// same answer, so booky.db can't arrive wearing a book's name
		if !uploadBookExts[strings.ToLower(filepath.Ext(src))] {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("%s is not an accepted book format", filepath.Ext(src)))
			return
		}
	}

	if !s.requireLibrary(w, r, libraryID) {
		return
	}
	dst, err := s.Importer.Deliver(id, libraryID, src, s.Acquire.NamingSettings(id))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if queueID > 0 {
		s.Acquire.MarkDone(queueID)
	}
	book, err := s.Catalog.GetBook(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": dst, "book": book})
}

// receiveBookUpload streams a browser-uploaded book file to the downloads
// dir and returns the library id, optional failed-queue-row id, and the
// file's server path, ready for Deliver (which moves it into the library).
// Streamed part-by-part so a 300MB PDF never has to fit in memory; the
// whole body is capped at 512MB.
func (s *Server) receiveBookUpload(w http.ResponseWriter, r *http.Request, bookID int64) (libraryID, queueID int64, src string, err error) {
	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	mr, err := r.MultipartReader()
	if err != nil {
		return 0, 0, "", err
	}
	dir := s.Settings.Get("downloads_dir")
	if dir == "" {
		dir = "/data/downloads/booky"
	}
	dir = filepath.Join(dir, "uploads")
	smallField := func(part io.Reader) (int64, error) {
		buf, err := io.ReadAll(io.LimitReader(part, 32))
		if err != nil {
			return 0, err
		}
		n, _ := strconv.ParseInt(strings.TrimSpace(string(buf)), 10, 64)
		return n, nil
	}
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, 0, "", err
		}
		switch part.FormName() {
		case "libraryId":
			if libraryID, err = smallField(part); err != nil {
				return 0, 0, "", err
			}
		case "queueId":
			if queueID, err = smallField(part); err != nil {
				return 0, 0, "", err
			}
		case "file":
			// Base() strips any path the browser sent; the id prefix keeps
			// concurrent uploads from colliding
			name := filepath.Base(strings.TrimSpace(part.FileName()))
			if name == "" || name == "." || name == "/" {
				return 0, 0, "", errors.New("upload has no filename")
			}
			ext := strings.ToLower(filepath.Ext(name))
			if !uploadBookExts[ext] {
				return 0, 0, "", fmt.Errorf("file type %q is not an accepted book format", ext)
			}
			// re-key the extension from the allowlist itself so the whole dst
			// path derives from server-side values only — nothing the client
			// sent reaches the filesystem path (and gosec's taint analysis
			// can see that). The importer renames on delivery, so the
			// original filename carries no information worth keeping.
			for allowed := range uploadBookExts {
				if allowed == ext {
					ext = allowed
					break
				}
			}
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return 0, 0, "", err
			}
			dst := filepath.Join(dir, fmt.Sprintf("%d-upload%s", bookID, ext))
			f, err := os.Create(dst) //nolint:gosec // G304: dst is the configured downloads dir + server-derived name
			if err != nil {
				return 0, 0, "", err
			}
			if _, err := io.Copy(f, part); err != nil {
				f.Close()
				_ = os.Remove(dst) //nolint:gosec // G703: dst is the configured downloads dir + a server-derived name; no client bytes reach the path
				return 0, 0, "", fmt.Errorf("upload: %w", err)
			}
			if err := f.Close(); err != nil {
				_ = os.Remove(dst) //nolint:gosec // G703: dst is the configured downloads dir + a server-derived name; no client bytes reach the path
				return 0, 0, "", err
			}
			// content must actually BE the claimed format, not just wear
			// its extension
			if err := verifyBookUpload(dst, ext); err != nil {
				_ = os.Remove(dst) //nolint:gosec // G703: dst is the configured downloads dir + a server-derived name; no client bytes reach the path
				return 0, 0, "", err
			}
			src = dst
		}
	}
	if libraryID == 0 || src == "" {
		if src != "" {
			_ = os.Remove(src) //nolint:gosec // G703: src == dst above — same server-derived path
		}
		return 0, 0, "", errors.New("multipart fields libraryId and file required")
	}
	return libraryID, queueID, src, nil
}

type moveBookRequest struct {
	FromLibraryID int64 `json:"fromLibraryId"`
	ToLibraryID   int64 `json:"toLibraryId"`
}

// PUT /books/{id}/library — move a book's membership between libraries.
// Books with files stay put until the importer can move
// files safely; membership-only moves work today.
func (s *Server) handleBookMove(w http.ResponseWriter, r *http.Request) {
	// a move rewrites library membership on both sides — library management
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req moveBookRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Catalog.MoveBook(id, req.FromLibraryID, req.ToLibraryID); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "moved"})
}
