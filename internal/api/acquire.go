package api

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/getbooky/booky/internal/prowlarr"
	"github.com/getbooky/booky/internal/release"
	"github.com/getbooky/booky/internal/sabnzbd"
)

// ---- interactive release search & grab ----

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	bookID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	libraryID, _ := strconv.ParseInt(r.URL.Query().Get("libraryId"), 10, 64)
	if libraryID == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("libraryId required"))
		return
	}
	if !s.requireLibrary(w, r, libraryID) {
		return
	}
	rels, sources, err := s.Acquire.SearchDetailed(r.Context(), bookID, libraryID, 0)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"releases": rels, "sources": sources})
}

// handleAutoGrab searches and grabs the highest-ranked release in one shot,
// applying the same source/format priorities the watcher uses.
func (s *Server) handleAutoGrab(w http.ResponseWriter, r *http.Request) {
	bookID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	libraryID, _ := strconv.ParseInt(r.URL.Query().Get("libraryId"), 10, 64)
	if libraryID == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("libraryId required"))
		return
	}
	if !s.requireLibrary(w, r, libraryID) {
		return
	}
	grabbed, err := s.Acquire.AutoGrab(r.Context(), bookID, libraryID, 0)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if grabbed {
		s.Acquire.DeliverPending()
	}
	writeJSON(w, http.StatusOK, map[string]any{"grabbed": grabbed})
}

type grabRequest struct {
	LibraryID int64           `json:"libraryId"`
	Release   release.Release `json:"release"`
}

func (s *Server) handleGrab(w http.ResponseWriter, r *http.Request) {
	bookID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req grabRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.LibraryID == 0 || req.Release.DownloadURL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("libraryId and release required"))
		return
	}
	if !s.requireLibrary(w, r, req.LibraryID) {
		return
	}
	queueID, err := s.Acquire.Grab(r.Context(), bookID, req.LibraryID, req.Release)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	// direct downloads are already on disk — deliver into the library now
	s.Acquire.DeliverPending()
	writeJSON(w, http.StatusOK, map[string]any{"queueId": queueID})
}

// ---- queue, wanted, history ----

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	// the watcher tracks downloads in the background; a queue view also nudges
	// it inline so the UI reflects finished jobs immediately
	s.Acquire.PollSab(r.Context())
	s.Acquire.DeliverPending()
	items, err := s.Acquire.Queue(s.visibility(s.access(r)))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": items})
}

// handleQueueCancel aborts a queue row — the download is stopped where it
// lives and its files cleaned up, with no blocklist and no cascade.
func (s *Server) handleQueueCancel(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var libID int64
	switch err := s.db.QueryRow(`SELECT library_id FROM queue WHERE id = ?`, id).Scan(&libID); {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, http.StatusNotFound, errors.New("queue item not found"))
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !s.requireLibrary(w, r, libID) {
		return
	}
	if err := s.Acquire.Cancel(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleQueueRetry re-attempts a failed import after the user fixed the
// underlying problem — the already-downloaded file is delivered again, no
// re-download.
func (s *Server) handleQueueRetry(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	// look the row up directly rather than scanning the recent-items list —
	// a failed import worth retrying may well have scrolled past 200 rows ago
	var libID int64
	switch err := s.db.QueryRow(`SELECT library_id FROM queue WHERE id = ?`, id).Scan(&libID); {
	case errors.Is(err, sql.ErrNoRows):
		writeErr(w, http.StatusNotFound, errors.New("queue item not found"))
		return
	case err != nil:
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !s.requireLibrary(w, r, libID) {
		return
	}
	if err := s.Acquire.RetryImport(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleWanted returns both Wanted tabs: missing (monitored, no file) and
// cutoff-unmet (has a file, but below the profile's cutoff format).
func (s *Server) handleWanted(w http.ResponseWriter, r *http.Request) {
	books, err := s.Catalog.ListBooks(0, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	a := s.access(r)
	wanted := books[:0:0]
	for _, b := range s.bookScope(a).filter(books) {
		if b.Monitored && b.FilePath == "" {
			wanted = append(wanted, b)
		}
	}
	unmet, unmetLibs, err := s.Acquire.CutoffUnmet()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if a.scoped() {
		// CutoffUnmet returns library ids alongside the books, so the pair has
		// to be filtered together rather than through filterBooks
		mine := unmet[:0:0]
		for i, b := range unmet {
			if a.mayLibrary(unmetLibs[i]) {
				mine = append(mine, b)
			}
		}
		unmet = mine
	}
	writeJSON(w, http.StatusOK, map[string]any{"books": wanted, "cutoffUnmet": unmet})
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	items, err := s.Catalog.ListHistory(100, s.visibility(s.access(r)))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"history": items})
}

// ---- connection tests (used by the settings panels) ----

type testConnRequest struct {
	URL      string `json:"url"`
	APIKey   string `json:"apiKey"`
	Category string `json:"category"`
}

func (s *Server) handleTestProwlarr(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req testConnRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.URL == "" {
		req.URL = s.Settings.Get("prowlarr_url")
	}
	if req.APIKey == "" || req.APIKey == "********" {
		req.APIKey = s.Settings.Get("prowlarr_api_key")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	client := prowlarr.New(req.URL, req.APIKey)
	st, err := client.Test(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	idx, _ := client.Indexers(ctx)
	writeJSON(w, http.StatusOK, map[string]any{"version": st.Version, "indexers": len(idx)})
}

func (s *Server) handleTestSab(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req testConnRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.URL == "" {
		req.URL = s.Settings.Get("sab_url")
	}
	if req.APIKey == "" || req.APIKey == "********" {
		req.APIKey = s.Settings.Get("sab_api_key")
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	version, err := sabnzbd.New(req.URL, req.APIKey, req.Category).Test(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version})
}

func (s *Server) handleTestZlib(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	z := s.Acquire.Zlib()
	if !z.Configured() {
		writeErr(w, http.StatusBadRequest, errors.New("save your Z-Library email and password first"))
		return
	}
	left, limit, err := z.Test(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"downloadsLeft": left, "downloadsLimit": limit})
}

// ---- quality profiles ----

// Quality profiles are library configuration — named format ladders with
// preferred and avoided terms. Only the settings screens and the setup wizard
// read them, both admin-only.
func (s *Server) handleProfiles(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	profiles, err := s.Catalog.ListProfiles()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": profiles})
}

func (s *Server) handleUpdateProfile(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var p struct {
		Name         string   `json:"name"`
		Formats      []string `json:"formats"`
		CutoffFormat string   `json:"cutoffFormat"`
		// pointer: an absent field keeps the stored list (older callers
		// don't send it), while an explicit "" turns the filter off
		Languages      *string `json:"languages"`
		PreferredTerms string  `json:"preferredTerms"`
		AvoidedTerms   string  `json:"avoidedTerms"`
	}
	if err := decodeJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(p.Formats) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("at least one format required"))
		return
	}
	if p.Languages == nil {
		var cur string
		if err := s.db.QueryRow(`SELECT language FROM quality_profiles WHERE id = ?`, id).Scan(&cur); err == nil {
			p.Languages = &cur
		} else {
			empty := ""
			p.Languages = &empty
		}
	}
	if err := s.Catalog.UpdateProfile(id, p.Name, p.Formats, p.CutoffFormat, *p.Languages, p.PreferredTerms, p.AvoidedTerms); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
