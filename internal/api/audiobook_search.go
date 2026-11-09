package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/getbooky/booky/internal/audiobook"
	"github.com/getbooky/booky/internal/catalog"
)

// Searching for, grabbing and monitoring a book's audiobook. Access follows
// the ebook's: anyone who can see the book may search and grab — except
// that a grab which would replace an audiobook already on the shelf deletes
// a file, and deleting is for admins.

// audioTarget checks the book is visible and in the library, and returns
// the library it's in.
func (s *Server) audioTarget(w http.ResponseWriter, r *http.Request) (bookID, libraryID int64, ok bool) {
	bookID, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return 0, 0, false
	}
	if !s.requireBook(w, r, bookID) {
		return 0, 0, false
	}
	b, err := s.Catalog.GetBook(bookID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return 0, 0, false
	}
	if b.LibraryID == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("add the book to the library first"))
		return 0, 0, false
	}
	return bookID, b.LibraryID, true
}

// mayReplaceAudio answers a grab that would land on top of an audiobook
// the book already has.
func (s *Server) mayReplaceAudio(w http.ResponseWriter, r *http.Request, bookID int64) bool {
	if s.access(r).admin() {
		return true
	}
	if _, err := s.Audiobooks.Get(bookID); errors.Is(err, audiobook.ErrNone) {
		return true
	}
	writeErr(w, http.StatusForbidden, errors.New("only an admin can replace an audiobook"))
	return false
}

// GET /books/{id}/audiobook/releases — search for the book's audiobook.
func (s *Server) handleAudioReleases(w http.ResponseWriter, r *http.Request) {
	bookID, _, ok := s.audioTarget(w, r)
	if !ok {
		return
	}
	rels, sources, err := s.Acquire.SearchAudio(r.Context(), bookID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.signReleases(bookID, "audiobook", rels)
	writeJSON(w, http.StatusOK, map[string]any{"releases": rels, "sources": sources})
}

// POST /books/{id}/audiobook/grab {"release": {...}} — grab one release.
func (s *Server) handleAudioGrab(w http.ResponseWriter, r *http.Request) {
	bookID, libraryID, ok := s.audioTarget(w, r)
	if !ok {
		return
	}
	var req grabRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Release.DownloadURL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("release required"))
		return
	}
	if !s.mayReplaceAudio(w, r, bookID) {
		return
	}
	if !s.sealedRelease(w, bookID, "audiobook", &req.Release) {
		return
	}
	queueID, err := s.Acquire.GrabAudio(r.Context(), bookID, libraryID, req.Release)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queueId": queueID})
}

// POST /books/{id}/audiobook/autograb — search and grab the best release.
func (s *Server) handleAudioAutoGrab(w http.ResponseWriter, r *http.Request) {
	bookID, libraryID, ok := s.audioTarget(w, r)
	if !ok {
		return
	}
	grabbed, err := s.Acquire.AutoGrabAudio(r.Context(), bookID, libraryID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"grabbed": grabbed})
}

// PUT /books/{id}/audiobook/monitored {"monitored": bool} — switch the
// search for the book's audiobook on or off. Switching it on for a book
// with no audiobook searches straight away.
func (s *Server) handleAudioMonitor(w http.ResponseWriter, r *http.Request) {
	bookID, libraryID, ok := s.audioTarget(w, r)
	if !ok {
		return
	}
	var req monitorRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Catalog.SetAudioMonitored(libraryID, bookID, req.Monitored); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Monitored {
		if _, err := s.Audiobooks.Get(bookID); errors.Is(err, audiobook.ErrNone) {
			//nolint:gosec // G118: the search must outlive the HTTP request by design
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
				defer cancel()
				grabbed, err := s.Acquire.AutoGrabAudio(ctx, bookID, libraryID)
				switch {
				case err != nil:
					log.Printf("api: audiobook search on monitor, book %d: %v", bookID, err) //nolint:gosec // G706: a number and our own error
				case !grabbed:
					_ = s.Catalog.AddHistory(bookID, libraryID, "search", "no grabbable audiobook found — the backlog will retry")
				}
			}()
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"audioMonitored": req.Monitored})
}

// GET /profiles/audiobook — the audiobook profile.
func (s *Server) handleAudioProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	p, err := s.Catalog.GetAudioProfile()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

// PUT /profiles/audiobook — save it.
func (s *Server) handleUpdateAudioProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var p catalog.AudioProfile
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Catalog.UpdateAudioProfile(p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
