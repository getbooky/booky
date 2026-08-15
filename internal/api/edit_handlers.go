package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"
)

func pathID(r *http.Request, name string) (int64, error) {
	return strconv.ParseInt(r.PathValue(name), 10, 64)
}

type editBookRequest struct {
	Fields map[string]string `json:"fields"`
	// Lock defaults true: manual edits protect themselves from refreshes.
	Lock *bool `json:"lock,omitempty"`
}

// handleEditBook writes metadata by hand. This is the line a plain user does
// not cross: they can re-pull metadata from the providers all day, but the
// stored values (and the locks that pin them) are the admin's.
func (s *Server) handleEditBook(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req editBookRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	lock := req.Lock == nil || *req.Lock
	if err := s.Catalog.EditBook(id, req.Fields, lock); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	book, err := s.Catalog.GetBook(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("book not found"))
		return
	}
	writeJSON(w, http.StatusOK, book)
}

type monitorRequest struct {
	Monitored bool `json:"monitored"`
}

// PUT /books/{id}/lock — toggle one field's refresh-protection lock, so the
// edit dialog can show and flip locks without saving field values.
func (s *Server) handleBookLock(w http.ResponseWriter, r *http.Request) {
	// a lock is a manual-edit decision — same gate as editing the field
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req struct {
		Field  string `json:"field"`
		Locked bool   `json:"locked"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Catalog.SetFieldLock(id, req.Field, req.Locked); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	book, err := s.Catalog.GetBook(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("book not found"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fieldLocks": book.FieldLocks})
}

func (s *Server) handleBookMonitor(w http.ResponseWriter, r *http.Request) {
	libID, err1 := pathID(r, "id")
	bookID, err2 := pathID(r, "bookId")
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireLibrary(w, r, libID) {
		return
	}
	var req monitorRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Catalog.SetMonitored(libID, bookID, req.Monitored); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// monitoring a missing book is a request to go get it — search in the
	// background rather than waiting for the weekly backlog pass
	if req.Monitored {
		if b, err := s.Catalog.GetBook(bookID); err == nil && b.FilePath == "" {
			//nolint:gosec // G118: the search must outlive the HTTP request by design
			go func(bookID, libID int64, title string) {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				grabbed, err := s.Acquire.AutoGrab(ctx, bookID, libID, 0)
				switch {
				case err != nil:
					// %q escapes newlines, so a hostile title cannot forge log lines
					log.Printf("api: auto-grab on monitor %q: %v", title, err) //nolint:gosec // G706: quoted above
				case grabbed:
					s.Acquire.DeliverPending()
				default:
					_ = s.Catalog.AddHistory(bookID, libID, "search", "no grabbable release found for "+title+" — backlog will retry")
				}
			}(bookID, libID, b.Title)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"monitored": req.Monitored})
}

// handleSeriesAddToLibrary shelves a whole series in one named library: every
// book joins it monitored, and the missing ones are searched for in the
// background at the usual pace.
//
// The library is named by the caller rather than inferred, and checked like
// any other write into a library. That is the whole design: the old "monitor
// series" toggle flipped a global flag that cascaded into every library
// holding any of those books, so one account monitoring a series started
// downloads on another account's shelf.
func (s *Server) handleSeriesAddToLibrary(w http.ResponseWriter, r *http.Request) {
	seriesID, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireSeries(w, r, seriesID) {
		return
	}
	var req struct {
		LibraryID int64 `json:"libraryId"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.LibraryID == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("libraryId required"))
		return
	}
	if !s.requireLibrary(w, r, req.LibraryID) {
		return
	}
	added, err := s.Catalog.AddSeriesToLibrary(seriesID, req.LibraryID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// go get the ones that aren't on the shelf, in this library only
	queued := 0
	if books, err := s.Catalog.ListBooks(0, req.LibraryID, seriesID); err == nil {
		today := time.Now().UTC().Format("2006-01-02")
		var targets []grabTarget
		for _, b := range books {
			future := len(b.ReleaseDate) == 10 && b.ReleaseDate > today
			if b.FilePath == "" && !future {
				targets = append(targets, grabTarget{b.ID, req.LibraryID})
			}
		}
		queued = len(targets)
		if queued > 0 {
			go s.grabTargetsPaced(targets)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added, "queued": queued})
}

// removeDetail spells out what each mode actually did, appended to the title.
var removeDetail = map[string]string{
	"library": " — removed from the library, file kept",
	"file":    " — removed from the library, file deleted",
	"block":   " — removed from the library, file deleted and blocked from re-import",
}

// bookTitle is the book's title for a log line, falling back to its id when
// the row is already gone — history entries are never worth failing over.
func (s *Server) bookTitle(bookID int64) string {
	if b, err := s.Catalog.GetBook(bookID); err == nil && b.Title != "" {
		return b.Title
	}
	return fmt.Sprintf("book %d", bookID)
}

// quoteName names a deleted thing, falling back to its id when the row was
// already gone by the time we looked.
func quoteName(name string, id int64) string {
	if name == "" {
		return fmt.Sprintf("#%d", id)
	}
	return strconv.Quote(name)
}

// handleBookRemove takes a book off a shelf. mode=library drops the
// membership and leaves every file alone, which anyone may do to their own
// library — a person who can add a series has to be able to unadd it, or they
// accumulate a mess they can't clear.
//
// The other two modes stay with the admin. mode=file deletes this library's
// copy; hard-linked copies elsewhere keep their own directory entries, so the
// data survives, but it is still a delete. mode=block additionally blocklists
// the book install-wide, which really does reach every library — that one is
// nobody's to press but an admin's.
func (s *Server) handleBookRemove(w http.ResponseWriter, r *http.Request) {
	libID, err1 := pathID(r, "id")
	bookID, err2 := pathID(r, "bookId")
	if err1 != nil || err2 != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	mode := r.URL.Query().Get("mode")
	switch mode {
	case "library":
		if !s.requireLibrary(w, r, libID) {
			return
		}
	case "file", "block":
		if !s.requireAdmin(w, r) {
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, errors.New("mode must be library, file or block"))
		return
	}
	// read the title BEFORE the removal: mode=file takes the book's only copy
	// with it, and a history line saying "a book was deleted" helps nobody
	title := s.bookTitle(bookID)
	if err := s.Catalog.RemoveBookFromLibrary(bookID, libID, mode); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	// A delete is the one action nothing else leaves a trace of — the book
	// simply stops being there. Record what went and how far it went, so
	// "where did X go?" has an answer that isn't a shrug.
	_ = s.Catalog.AddHistory(bookID, libID, "removed", title+removeDetail[mode])
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// ---- import review actions ----

func (s *Server) handleReviewIgnore(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	fileID, err := pathID(r, "fileId")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if err := s.Importer.Ignore(fileID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ignored"})
}

type reviewMatchRequest struct {
	Meta clientBookMeta `json:"meta"`
}

func (s *Server) handleReviewMatch(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	fileID, err := pathID(r, "fileId")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req reviewMatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	meta := s.Chain.Enrich(r.Context(), req.Meta.BookMeta)
	bookID, err := s.Importer.Accept(fileID, meta)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Covers.Ensure(r.Context(), bookID, meta.CoverURL); err != nil {
		_ = err // non-fatal
	}
	book, err := s.Catalog.GetBook(bookID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, book)
}
