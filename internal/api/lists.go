package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/watcher"
)

// ---- watched lists ----

// Watched lists poll an external shelf and add books into a library on a
// timer, carrying their own quality profile. That is install configuration,
// so the whole set of endpoints is admin-only.
func (s *Server) handleLists(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	lists, err := s.Watcher.Lists()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"lists": lists})
}

// listRequest is the create/update payload. For Goodreads, sourceRef accepts
// whatever the user pasted (profile URL or ID) and is canonicalized with the
// shelf; for Hardcover it's the numeric list id.
type listRequest struct {
	Name             string `json:"name"`
	Kind             string `json:"kind"`
	SourceRef        string `json:"sourceRef"`
	Shelf            string `json:"shelf"` // goodreads only; default to-read
	LibraryID        int64  `json:"libraryId"`
	MonitorScope     string `json:"monitorScope"`
	OnRemove         string `json:"onRemove"`
	SearchOnAdd      bool   `json:"searchOnAdd"`
	Enabled          bool   `json:"enabled"`
	QualityProfileID int64  `json:"qualityProfileId"`
}

func (req *listRequest) toList() (watcher.WatchedList, error) {
	sourceRef := req.SourceRef
	if req.Kind == "goodreads_rss" {
		ref, err := watcher.ParseGoodreadsRef(req.SourceRef, req.Shelf)
		if err != nil {
			return watcher.WatchedList{}, err
		}
		sourceRef = ref
	}
	return watcher.WatchedList{
		Name: req.Name, Kind: req.Kind, SourceRef: sourceRef,
		LibraryID: req.LibraryID, MonitorScope: req.MonitorScope, OnRemove: req.OnRemove,
		SearchOnAdd: req.SearchOnAdd, Enabled: req.Enabled, QualityProfileID: req.QualityProfileID,
	}, nil
}

func (s *Server) handleCreateList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req listRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	list, err := req.toList()
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.Watcher.CreateList(list)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	created, err := s.Watcher.GetList(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) handleUpdateList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	existing, err := s.Watcher.GetList(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	// start from the stored row so partial updates (e.g. just the enabled
	// toggle) don't blank other fields
	req := listRequest{
		Name: existing.Name, Kind: existing.Kind, SourceRef: existing.SourceRef,
		LibraryID: existing.LibraryID, MonitorScope: existing.MonitorScope, OnRemove: existing.OnRemove,
		SearchOnAdd: existing.SearchOnAdd, Enabled: existing.Enabled, QualityProfileID: existing.QualityProfileID,
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	list := watcher.WatchedList{
		ID: id, Name: req.Name, Kind: existing.Kind, SourceRef: req.SourceRef,
		LibraryID: req.LibraryID, MonitorScope: req.MonitorScope, OnRemove: req.OnRemove,
		SearchOnAdd: req.SearchOnAdd, Enabled: req.Enabled, QualityProfileID: req.QualityProfileID,
	}
	// re-canonicalize a goodreads ref only when it was actually edited
	if existing.Kind == "goodreads_rss" && req.SourceRef != existing.SourceRef {
		ref, err := watcher.ParseGoodreadsRef(req.SourceRef, req.Shelf)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		list.SourceRef = ref
	}
	if err := s.Watcher.UpdateList(list); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	updated, err := s.Watcher.GetList(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleDeleteList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	// named before it's gone; a missing list still deletes cleanly
	list, _ := s.Watcher.GetList(id)
	if err := s.Watcher.DeleteList(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Books the list brought in stay where they are — say so, because the
	// obvious worry on seeing "list deleted" is that the shelf went with it.
	name, libraryID := "", int64(0)
	if list != nil {
		name, libraryID = list.Name, list.LibraryID
	}
	_ = s.Catalog.AddHistory(0, libraryID, "removed",
		fmt.Sprintf("watched list %s deleted — books already added stay in the library", quoteName(name, id)))
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handlePollList polls one list immediately ("check now" in the UI).
func (s *Server) handlePollList(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	added, err := s.Watcher.PollList(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

// ---- author backlist ----

// handleAddAuthor creates (or finds) an author by name and kicks off their
// bibliography sync right away, so the author page fills in within seconds
// instead of waiting for the watcher's next pass.
func (s *Server) handleAddAuthor(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name required"))
		return
	}
	authorID, err := s.Catalog.EnsureAuthor(name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Remember that they were asked for. Their bibliography arrives
	// catalog-only, in no library at all, so without this the author would
	// drop straight off the Authors page of whoever just added them — the one
	// case where holding nothing is the point rather than a sign they should
	// have gone. MarkAuthorAdded covers the install-wide view; AddAuthorFor
	// records which account it was, so scoped users see their own.
	if err := s.Catalog.MarkAuthorAdded(authorID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	a := s.access(r)
	if a.user != nil {
		if err := s.Catalog.AddAuthorFor(a.user.ID, authorID); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	// a deliberately-added author is monitored: that's what keeps them on the
	// Authors page before any book is in a library, and re-syncs their
	// bibliography weekly for new releases
	if err := s.Catalog.SetAuthorMonitored(authorID, true); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := s.Watcher.SyncBibliography(ctx, authorID); err != nil {
			log.Printf("api: add-author bibliography sync for %q: %v", name, err)
		}
	}()
	writeJSON(w, http.StatusCreated, map[string]any{"id": authorID, "name": name})
}

// handleExpandAuthor refreshes an author's bibliography on demand. The
// watcher does this automatically in the background (new authors within
// seconds, monitored authors weekly); this endpoint is the manual "now"
// button. Books arrive catalog-only — visible on the author page, in no
// library until monitored.
func (s *Server) handleExpandAuthor(w http.ResponseWriter, r *http.Request) {
	authorID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireAuthor(w, r, authorID) {
		return
	}
	added, err := s.Watcher.SyncBibliography(r.Context(), authorID)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"added": added})
}

// handleAuthorSearch fires a search for the author's monitored books that
// need something: missing (no file) and cutoff-unmet (file below the
// profile's cutoff format). Never the whole bibliography — on-shelf books at
// cutoff and unmonitored books are skipped. Grabs run in the background,
// paced, so the response is immediate.
func (s *Server) handleAuthorSearch(w http.ResponseWriter, r *http.Request) {
	authorID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireAuthor(w, r, authorID) {
		return
	}
	books, err := s.Catalog.ListBooks(authorID, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	a := s.access(r)
	books = s.bookScope(a).filter(books)
	var targets []grabTarget
	seen := map[grabTarget]bool{}
	add := func(t grabTarget) {
		if !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}
	for _, b := range books {
		if b.Monitored && b.LibraryID != 0 && b.FilePath == "" {
			add(grabTarget{b.ID, b.LibraryID})
		}
	}
	// upgrades: this author's books sitting below their profile's cutoff
	if unmet, libIDs, err := s.Acquire.CutoffUnmet(); err == nil {
		for i, b := range unmet {
			if b.AuthorID == authorID && a.mayLibrary(libIDs[i]) {
				add(grabTarget{b.ID, libIDs[i]})
			}
		}
	}
	go s.grabTargetsPaced(targets)
	writeJSON(w, http.StatusOK, map[string]any{"queued": len(targets)})
}

type grabTarget struct{ bookID, libraryID int64 }

// grabTargetsPaced auto-grabs each target in the background with a pacing gap,
// so bulk searches (author, library) never burst the sources.
func (s *Server) grabTargetsPaced(targets []grabTarget) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	for _, t := range targets {
		if _, err := s.Acquire.AutoGrab(ctx, t.bookID, t.libraryID, 0); err != nil {
			log.Printf("api: bulk search grab book %d: %v", t.bookID, err)
		}
		s.Acquire.DeliverPending()
		select {
		case <-ctx.Done():
			return
		case <-time.After(3 * time.Second): // pace the sources
		}
	}
}

// handleLibrarySearch fires a search for every monitored book in the library
// that needs something: missing, plus cutoff-unmet upgrades. Library id 0
// means every library. Same background pacing as the author search.
func (s *Server) handleLibrarySearch(w http.ResponseWriter, r *http.Request) {
	libraryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	// id 0 fans the search out over every library — for a scoped user that
	// means every library they hold, not every library that exists
	if libraryID != 0 && !s.requireLibrary(w, r, libraryID) {
		return
	}
	books, err := s.Catalog.ListBooks(0, libraryID, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	a := s.access(r)
	books = s.bookScope(a).filter(books)
	var targets []grabTarget
	seen := map[grabTarget]bool{}
	add := func(t grabTarget) {
		if !seen[t] {
			seen[t] = true
			targets = append(targets, t)
		}
	}
	for _, b := range books {
		if b.Monitored && b.LibraryID != 0 && b.FilePath == "" {
			add(grabTarget{b.ID, b.LibraryID})
		}
	}
	if unmet, libIDs, err := s.Acquire.CutoffUnmet(); err == nil {
		for i, b := range unmet {
			if (libraryID == 0 || libIDs[i] == libraryID) && a.mayLibrary(libIDs[i]) {
				add(grabTarget{b.ID, libIDs[i]})
			}
		}
	}
	go s.grabTargetsPaced(targets)
	writeJSON(w, http.StatusOK, map[string]any{"queued": len(targets)})
}

// handleDeleteAuthor removes an author and every one of their books from
// Booky. mode=catalog (default) never touches the shelf; mode=files also
// deletes the books' files on disk and prunes the directories that emptied —
// including the author's folder itself under a {Author}/{Title} scheme.
func (s *Server) handleDeleteAuthor(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	authorID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	mode := r.URL.Query().Get("mode")
	switch mode {
	case "", "catalog", "files":
	default:
		writeErr(w, http.StatusBadRequest, errors.New("mode must be catalog or files"))
		return
	}
	// both are unreadable once the cascade has run
	name, books := s.Catalog.AuthorSummary(authorID)
	deletedFiles := 0
	if mode == "files" {
		// files first: the author-row cascade erases the paths
		deletedFiles, err = s.Catalog.DeleteAuthorFiles(authorID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.Catalog.DeleteAuthor(authorID); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	// no book id and no library: the books went with the author, so this is an
	// install-wide event and the detail carries everything it has to say
	files := "files kept"
	if mode == "files" {
		files = fmt.Sprintf("%d file(s) deleted", deletedFiles)
	}
	_ = s.Catalog.AddHistory(0, 0, "removed",
		fmt.Sprintf("author %s deleted — %d book(s), %s", quoteName(name, authorID), books, files))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "deletedFiles": deletedFiles})
}

// handleSetLibraryProfile assigns a quality profile to a library.
func (s *Server) handleSetLibraryProfile(w http.ResponseWriter, r *http.Request) {
	// which quality profile a library uses is library configuration
	if !s.requireAdmin(w, r) {
		return
	}
	libraryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req struct {
		ProfileID int64 `json:"profileId"`
	}
	if err := decodeJSON(r, &req); err != nil || req.ProfileID == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("profileId required"))
		return
	}
	if err := s.Catalog.SetLibraryProfile(libraryID, req.ProfileID); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// ---- calendar ----

// handleCalendar returns monitored books with release dates from the last two
// weeks (the search taper window) onward, soonest first.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	books, err := s.Catalog.ListBooks(0, 0, 0)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	cutoff := time.Now().UTC().AddDate(0, 0, -14).Format("2006-01-02")
	upcoming := []catalog.Book{}
	for _, b := range s.bookScope(s.access(r)).filter(books) {
		if !b.Monitored || len(b.ReleaseDate) != 10 || b.ReleaseDate < cutoff {
			continue
		}
		upcoming = append(upcoming, b)
	}
	// soonest first
	for i := 1; i < len(upcoming); i++ {
		for j := i; j > 0 && upcoming[j].ReleaseDate < upcoming[j-1].ReleaseDate; j-- {
			upcoming[j], upcoming[j-1] = upcoming[j-1], upcoming[j]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"books": upcoming})
}
