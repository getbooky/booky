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

	"github.com/getbooky/booky/internal/epub"
	"github.com/getbooky/booky/internal/metadata"
	"github.com/getbooky/booky/internal/settings"
)

// ---- metadata search ----

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	params := metadata.SearchParams{
		Query:  q.Get("q"),
		Title:  q.Get("title"),
		Author: q.Get("author"),
		ISBN:   q.Get("isbn"),
		Limit:  20,
	}
	if params.Query == "" && params.Title == "" && params.ISBN == "" {
		writeErr(w, http.StatusBadRequest, errors.New("missing query"))
		return
	}
	results, err := s.Chain.Search(r.Context(), params)
	if err != nil && len(results) == 0 {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	// annotate each result with whether it's already in a library / monitored
	// and which distinct authors are already added, so the UI can reflect
	// existing state instead of offering duplicate adds
	type searchResult struct {
		metadata.BookMeta
		InLibrary bool `json:"inLibrary"`
		Monitored bool `json:"monitored"`
	}
	out := make([]searchResult, len(results))
	authors := map[string]bool{}
	for i, m := range results {
		inLib, mon := s.Catalog.MonitorState(m)
		out[i] = searchResult{BookMeta: m, InLibrary: inLib, Monitored: mon}
		for _, a := range m.Authors {
			if _, seen := authors[a]; !seen {
				exists, _ := s.Catalog.AuthorState(a)
				authors[a] = exists
			}
		}
	}
	// author portraits for the suggestion rows — one extra Author-type
	// search, best-effort (missing images just fall back to initials)
	hc := metadata.NewHardcover(func() string { return s.Settings.Get("hardcover_token") })
	var authorImages map[string]string
	if hc.WorksConfigured() && params.Query != "" {
		ictx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		defer cancel()
		authorImages = hc.SearchAuthorImages(ictx, params.Query, 5)
	}
	writeJSON(w, http.StatusOK, map[string]any{"results": out, "knownAuthors": authors, "authorImages": authorImages})
}

// handleEnrich fills a search result's gaps (description, cover, series,
// identifiers) across the provider chain without adding anything — the
// preview card in the add dialog.
func (s *Server) handleEnrich(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Meta clientBookMeta `json:"meta"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Meta.Title == "" {
		writeErr(w, http.StatusBadRequest, errors.New("meta.title required"))
		return
	}
	writeJSON(w, http.StatusOK, s.Chain.Enrich(r.Context(), req.Meta.BookMeta))
}

// ---- catalog ----

func (s *Server) handleAuthors(w http.ResponseWriter, r *http.Request) {
	authors, err := s.Catalog.ListAuthors(s.visibility(s.access(r)))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"authors": authors})
}

// handleAuthorPhoto serves the author's cached portrait, fetching it from the
// provider URL on first request — after that, browsing never leaves the box.
func (s *Server) handleAuthorPhoto(w http.ResponseWriter, r *http.Request) {
	authorID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireAuthor(w, r, authorID) {
		return
	}
	// Path is built internally from the numeric id — no user-controlled path
	path := s.AuthorPhotos.Path(authorID)
	if path == "" {
		url := s.Catalog.AuthorImageURL(authorID)
		if url == "" {
			http.NotFound(w, r)
			return
		}
		if err := s.AuthorPhotos.Ensure(r.Context(), authorID, url); err != nil {
			http.NotFound(w, r) // the next request retries
			return
		}
		path = s.AuthorPhotos.Path(authorID)
	}
	if path == "" {
		http.NotFound(w, r)
		return
	}
	// The URL is stable but the BYTES change — a custom upload, a re-match, a
	// regenerate. A long max-age meant the browser kept serving the old image
	// (for a day) on every view that didn't tack a cache-busting query on,
	// which is most of them. "no-cache" still caches; it only forces
	// revalidation, so the usual answer is a cheap 304 out of ServeFile's
	// Last-Modified handling and a replaced image appears at once.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, path)
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	series, err := s.Catalog.ListSeries(s.visibility(s.access(r)))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"series": series})
}

func (s *Server) handleBooks(w http.ResponseWriter, r *http.Request) {
	authorID, _ := strconv.ParseInt(r.URL.Query().Get("authorId"), 10, 64)
	libraryID, _ := strconv.ParseInt(r.URL.Query().Get("libraryId"), 10, 64)
	seriesID, _ := strconv.ParseInt(r.URL.Query().Get("seriesId"), 10, 64)
	a := s.access(r)
	// an explicit filter the caller can't reach is a 403; without one the
	// result is simply narrowed to their libraries
	if libraryID != 0 && !a.mayLibrary(libraryID) {
		writeErr(w, http.StatusForbidden, errForbidden)
		return
	}
	if authorID != 0 && !s.mayAuthor(a, authorID) {
		writeErr(w, http.StatusForbidden, errors.New("you don't have access to this author"))
		return
	}
	if seriesID != 0 && !s.maySeries(a, seriesID) {
		writeErr(w, http.StatusForbidden, errors.New("you don't have access to this series"))
		return
	}
	books, err := s.Catalog.ListBooks(authorID, libraryID, seriesID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"books": s.bookScope(a).filter(books)})
}

func (s *Server) handleBook(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
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
	writeJSON(w, http.StatusOK, book)
}

// clientBookMeta is a BookMeta as the client sends it back: the search
// endpoint annotates each result with inLibrary/monitored, and the add and
// enrich flows echo the whole result object verbatim. Accepting the two
// annotation fields (and ignoring them) lets the strict JSON decoder round-
// trip a search result without rejecting it as having unknown fields.
type clientBookMeta struct {
	metadata.BookMeta
	InLibrary bool `json:"inLibrary"`
	Monitored bool `json:"monitored"`
}

type addBookRequest struct {
	Meta      clientBookMeta `json:"meta"`
	LibraryID int64          `json:"libraryId"`
	Monitored bool           `json:"monitored"`
}

// handleAddBook adds a book from metadata-search results: enrich across the
// provider chain, upsert, attach to the library, cache the cover.
func (s *Server) handleAddBook(w http.ResponseWriter, r *http.Request) {
	var req addBookRequest
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
	meta := s.Chain.Enrich(r.Context(), req.Meta.BookMeta)
	bookID, err := s.Catalog.UpsertBook(meta)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.Catalog.AddToLibrary(bookID, req.LibraryID, req.Monitored); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := s.Covers.Ensure(r.Context(), bookID, meta.CoverURL); err != nil {
		// non-fatal: cover retries on next refresh
		_ = err
	}
	_ = s.Catalog.AddHistory(bookID, req.LibraryID, "added", "added via search: "+meta.Title)
	book, err := s.Catalog.GetBook(bookID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// adding a book MONITORED is a request to go get it — kick off the search
	// in the background instead of waiting for the weekly backlog pass
	if req.Monitored && book.FilePath == "" {
		//nolint:gosec // G118: the search must outlive the HTTP request by design
		go func(bookID, libID int64, title string) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			grabbed, err := s.Acquire.AutoGrab(ctx, bookID, libID, 0)
			switch {
			case err != nil:
				// %q escapes newlines, so a hostile title cannot forge log lines
				log.Printf("api: auto-grab on add %q: %v", title, err) //nolint:gosec // G706: quoted above
			case grabbed:
				s.Acquire.DeliverPending()
			default:
				_ = s.Catalog.AddHistory(bookID, libID, "search", "no grabbable release found for "+title+" — backlog will retry")
			}
		}(bookID, req.LibraryID, meta.Title)
	}
	writeJSON(w, http.StatusCreated, book)
}

// ---- libraries ----

func (s *Server) handleLibraries(w http.ResponseWriter, r *http.Request) {
	libs, err := s.Catalog.ListLibraries()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// This is what the sidebar is built from, so scoping it here is what
	// makes another library invisible rather than merely unreachable.
	writeJSON(w, http.StatusOK, map[string]any{"libraries": s.access(r).filterLibraries(libs)})
}

type createLibraryRequest struct {
	Name     string `json:"name"`
	RootPath string `json:"rootPath"`
}

func (s *Server) handleCreateLibrary(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req createLibraryRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" || req.RootPath == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name and rootPath required"))
		return
	}
	// A library root becomes an import source and a delivery target, so it's
	// fenced to the media mount like every other path the UI can point at.
	root, err := s.fenceMediaPath(req.RootPath)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("library root %w", err))
		return
	}
	req.RootPath = root
	profileID, err := s.Catalog.EnsureDefaultProfile()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// OPDS credentials are set afterwards in Settings → Libraries; unset until then
	id, err := s.Catalog.CreateLibrary(req.Name, req.RootPath, profileID, req.Name+"-shelf", "unset")
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

// handleDeleteLibrary removes a library. mode=keep (default) leaves every
// file on disk; mode=files also deletes the library's files and prunes the
// directories that emptied. Either way the books lose their membership and
// drop out of the Library view, staying catalog-only on author/series pages.
func (s *Server) handleDeleteLibrary(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	libraryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	mode := r.URL.Query().Get("mode")
	switch mode {
	case "", "keep", "files":
	default:
		writeErr(w, http.StatusBadRequest, errors.New("mode must be keep or files"))
		return
	}
	name := s.Catalog.LibraryName(libraryID) // gone after the delete
	deletedFiles := 0
	if mode == "files" {
		// files first: the library-row cascade erases the paths
		deletedFiles, err = s.Catalog.DeleteLibraryFiles(libraryID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if err := s.Catalog.DeleteLibrary(libraryID); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	// the library reference would be nulled by the cascade anyway, so this is
	// recorded install-wide and says which library it was in the detail
	files := "files kept"
	if mode == "files" {
		files = fmt.Sprintf("%d file(s) deleted", deletedFiles)
	}
	_ = s.Catalog.AddHistory(0, 0, "removed",
		fmt.Sprintf("library %s deleted — %s", quoteName(name, libraryID), files))
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok", "deletedFiles": deletedFiles})
}

func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	// scanning walks the library root on disk and feeds the review queue —
	// filesystem work that belongs with library configuration
	if !s.requireAdmin(w, r) {
		return
	}
	libraryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	libs, err := s.Catalog.ListLibraries()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var rootPath string
	for _, l := range libs {
		if l.ID == libraryID {
			rootPath = l.RootPath
		}
	}
	if rootPath == "" {
		writeErr(w, http.StatusNotFound, errors.New("library not found"))
		return
	}
	result, err := s.Importer.Scan(r.Context(), libraryID, rootPath)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	// the review screen exists to hand-assign metadata to unmatched files
	if !s.requireAdmin(w, r) {
		return
	}
	libraryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	queue, err := s.Importer.ReviewQueue(libraryID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": queue})
}

// ---- covers ----

func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	bookID, err := strconv.ParseInt(r.PathValue("bookId"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireBook(w, r, bookID) {
		return
	}
	// Path is built internally from the numeric id — no user-controlled path
	path := s.Covers.Path(bookID)
	if path == "" {
		// no downloaded cover — the book file on the shelf usually embeds
		// one; extract it once and it's cached like any other
		if fp := s.Catalog.AnyFilePath(bookID); strings.HasSuffix(strings.ToLower(fp), ".epub") {
			if data, _, err := epub.Cover(fp); err == nil {
				if err := s.Covers.SaveBytes(bookID, data); err == nil {
					path = s.Covers.Path(bookID)
				}
			}
		}
	}
	if path == "" {
		http.NotFound(w, r)
		return
	}
	// The URL is stable but the BYTES change — a custom upload, a re-match, a
	// regenerate. A long max-age meant the browser kept serving the old image
	// (for a day) on every view that didn't tack a cache-busting query on,
	// which is most of them. "no-cache" still caches; it only forces
	// revalidation, so the usual answer is a cheap 304 out of ServeFile's
	// Last-Modified handling and a replaced image appears at once.
	w.Header().Set("Cache-Control", "no-cache")
	http.ServeFile(w, r, path)
}

// ---- settings ----

// settableKeys is the allowlist of keys the API may read or write.
var settableKeys = map[string]bool{
	"provider_order": true, "naming_scheme": true, "list_poll_seconds": true,
	"hardcover_token": true, "metadata_write": true, "write_on_import": true,
	"rewrite_on_refresh": true, "user_scopes": true, "series_overlay": true,
	"prowlarr_url": true, "prowlarr_api_key": true,
	"sab_url": true, "sab_api_key": true, "sab_category": true,
	"source_order": true, "downloads_dir": true, "backlog_enabled": true,
	"server_url": true, "backup_enabled": true, "backup_keep": true,
	"exclude_patterns": true,
	"annas_mirrors":    true, "annas_key": true,
	"zlib_domains": true, "zlib_email": true, "zlib_password": true,
	"prowlarr_enabled": true, "annas_enabled": true, "zlib_enabled": true,
}

// userReadableKeys are the settings a non-admin account may read: the
// sidebar's saved scopes, the shelf's series overlay toggle, and the server
// URL the KoReader panel shows next to the plugin download. Everything else
// (provider credentials, download clients, paths) is admin-only.
var userReadableKeys = map[string]bool{
	"user_scopes": true, "series_overlay": true, "server_url": true,
}

// userWritableKeys is narrower still: saved scopes are a UI convenience the
// whole install shares, and blocking writes would break the sidebar's
// "add scope" button for users.
var userWritableKeys = map[string]bool{"user_scopes": true}

// secretKeys are never echoed back — GET returns a mask instead. The list
// lives with the settings store, which also seals these at rest.
var secretKeys = settings.SecretKeys

// settingKeyFor gives each account its own slot for the settings that are
// really per-person UI state. Saved sidebar scopes are the only one: they live
// in the global key/value table, so without this every user who added a scope
// would rewrite the admin's list — and everyone else's.
func (s *Server) settingKeyFor(a access, key string) string {
	if key == "user_scopes" && a.scoped() && a.user != nil {
		return key + "_" + strconv.FormatInt(a.user.ID, 10)
	}
	return key
}

func (s *Server) handleGetSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !settableKeys[key] {
		writeErr(w, http.StatusNotFound, errors.New("unknown setting"))
		return
	}
	if !userReadableKeys[key] && !s.requireAdmin(w, r) {
		return
	}
	value := s.Settings.Get(s.settingKeyFor(s.access(r), key))
	if secretKeys[key] && value != "" {
		value = "********" // never echo secrets
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": value})
}

func (s *Server) handlePutSetting(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("key")
	if !settableKeys[key] {
		writeErr(w, http.StatusNotFound, errors.New("unknown setting"))
		return
	}
	if !userWritableKeys[key] && !s.requireAdmin(w, r) {
		return
	}
	stored := s.settingKeyFor(s.access(r), key)
	var body struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Secrets are echoed to the UI as a mask; a form saved without retyping
	// sends that mask back. Writing it would replace the real credential with
	// eight asterisks — treat it as "unchanged" instead, so Save/Test works
	// without re-entering the secret every time.
	if secretKeys[key] && body.Value == "********" {
		writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": "ok"})
		return
	}
	// The downloads folder is where acquisitions and uploads land on disk, so
	// a custom one is fenced to the media mount; empty means the built-in
	// default (/data/downloads/booky).
	if key == "downloads_dir" && strings.TrimSpace(body.Value) != "" {
		dir, err := s.fenceMediaPath(body.Value)
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("downloads folder %w", err))
			return
		}
		body.Value = dir
	}
	if err := s.Settings.Set(stored, body.Value); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"key": key, "value": "ok"})
}
