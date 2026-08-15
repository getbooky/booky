package api

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
)

// ---- in-app reader: book file + reading progress ----

// handleBookFile streams the on-disk ebook to the web reader. Session auth is
// enforced by requireAuth like every other /api/v1 route; the path comes from
// the catalog, never the request.
func (s *Server) handleBookFile(w http.ResponseWriter, r *http.Request) {
	bookID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireBook(w, r, bookID) {
		return
	}
	path := s.Catalog.AnyFilePath(bookID)
	if path == "" {
		http.NotFound(w, r)
		return
	}
	if _, err := os.Stat(path); err != nil {
		http.NotFound(w, r)
		return
	}
	// inline, keeping the real filename so the reader can sniff the format
	// from the extension (and a plain save keeps a sensible name)
	w.Header().Set("Content-Disposition", `inline; filename="`+filepath.Base(path)+`"`)
	http.ServeFile(w, r, path)
}

// progressUserID keys reading positions per account; before any account
// exists the API is open and everything shares slot 0.
func (s *Server) progressUserID(r *http.Request) int64 {
	if u := s.sessionUser(r); u != nil {
		return u.ID
	}
	return 0
}

func (s *Server) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	bookID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireBook(w, r, bookID) {
		return
	}
	var locator string
	var percent float64
	var updatedAt string
	err = s.db.QueryRow(`SELECT locator, percent, updated_at FROM reading_progress
		WHERE user_id = ? AND book_id = ?`, s.progressUserID(r), bookID).
		Scan(&locator, &percent, &updatedAt)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"locator": "", "percent": 0.0})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"locator": locator, "percent": percent, "updatedAt": updatedAt,
	})
}

func (s *Server) handlePutProgress(w http.ResponseWriter, r *http.Request) {
	bookID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireBook(w, r, bookID) {
		return
	}
	var req struct {
		Locator string  `json:"locator"`
		Percent float64 `json:"percent"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Percent < 0 || req.Percent > 1 {
		writeErr(w, http.StatusBadRequest, errors.New("percent must be 0..1"))
		return
	}
	_, err = s.db.Exec(`INSERT INTO reading_progress (user_id, book_id, locator, percent, updated_at)
		VALUES (?, ?, ?, ?, datetime('now'))
		ON CONFLICT (user_id, book_id) DO UPDATE SET
			locator = excluded.locator, percent = excluded.percent, updated_at = excluded.updated_at`,
		s.progressUserID(r), bookID, req.Locator, req.Percent)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
