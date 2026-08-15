// Package api wires the HTTP surface: the JSON API and the embedded SPA.
package api

import (
	"database/sql"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/getbooky/booky/internal/acquire"
	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/backup"
	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/importer"
	"github.com/getbooky/booky/internal/koreader"
	"github.com/getbooky/booky/internal/metadata"
	"github.com/getbooky/booky/internal/opds"
	"github.com/getbooky/booky/internal/settings"
	"github.com/getbooky/booky/internal/watcher"
)

type Server struct {
	db         *sql.DB
	version    string
	spa        fs.FS
	refreshing sync.Map // libraryID → in-flight metadata refresh
	Catalog    *catalog.Store
	Chain      *metadata.Chain
	Importer   *importer.Importer
	Covers     *catalog.CoverCache
	// AuthorPhotos caches author portraits on disk, keyed by author id —
	// same mechanics as book covers, separate directory.
	AuthorPhotos *catalog.CoverCache
	Settings     *settings.Store
	Logs         *LogRing
	Acquire      *acquire.Engine
	Watcher      *watcher.Watcher
	Auth         *auth.Store
	KoReader     *koreader.Store
	Backups      *backup.Manager
	OPDS         *opds.Handler
	// mediaRoot is the media mount in the documented Docker deployment:
	// libraries and downloads live under /data, while the database and backups
	// sit on a separate /config volume. Every path the UI can point at —
	// browse suggestions, library roots, the downloads folder, manual-import
	// sources — is fenced to this mount, keeping /config, /etc and the binary
	// itself out of reach. Tests repoint it at a temp dir.
	mediaRoot string
}

type Deps struct {
	DB           *sql.DB
	Version      string
	Dist         fs.FS
	Catalog      *catalog.Store
	Chain        *metadata.Chain
	Importer     *importer.Importer
	Covers       *catalog.CoverCache
	AuthorPhotos *catalog.CoverCache
	Settings     *settings.Store
	Logs         *LogRing
	Acquire      *acquire.Engine
	Watcher      *watcher.Watcher
	Auth         *auth.Store
	KoReader     *koreader.Store
	Backups      *backup.Manager
	OPDS         *opds.Handler
}

func New(d Deps) (*Server, error) {
	sub, err := fs.Sub(d.Dist, "dist")
	if err != nil {
		return nil, err
	}
	return &Server{
		db: d.DB, version: d.Version, spa: sub,
		Catalog: d.Catalog, Chain: d.Chain, Importer: d.Importer,
		Covers: d.Covers, AuthorPhotos: d.AuthorPhotos, Settings: d.Settings, Logs: d.Logs,
		Acquire: d.Acquire, Watcher: d.Watcher,
		Auth: d.Auth, KoReader: d.KoReader, Backups: d.Backups, OPDS: d.OPDS,
		mediaRoot: "/data",
	}, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/system/status", s.handleStatus)
	mux.HandleFunc("GET /api/v1/system/health", s.handleHealth)
	mux.HandleFunc("GET /api/v1/system/logs", s.handleLogs)
	mux.HandleFunc("GET /api/v1/system/browse", s.handleBrowse)
	mux.HandleFunc("GET /api/v1/search", s.handleSearch)
	mux.HandleFunc("POST /api/v1/search/enrich", s.handleEnrich)
	mux.HandleFunc("GET /api/v1/authors", s.handleAuthors)
	mux.HandleFunc("GET /api/v1/authors/{id}/photo", s.handleAuthorPhoto)
	mux.HandleFunc("GET /api/v1/series", s.handleSeries)
	mux.HandleFunc("GET /api/v1/books", s.handleBooks)
	mux.HandleFunc("GET /api/v1/books/{id}", s.handleBook)
	mux.HandleFunc("POST /api/v1/books", s.handleAddBook)
	mux.HandleFunc("PATCH /api/v1/books/{id}", s.handleEditBook)
	mux.HandleFunc("POST /api/v1/books/{id}/refresh", s.handleBookRefresh)
	mux.HandleFunc("POST /api/v1/books/{id}/rematch", s.handleBookRematch)
	mux.HandleFunc("POST /api/v1/books/{id}/cover", s.handleBookCoverRegen)
	mux.HandleFunc("PUT /api/v1/books/{id}/cover/custom", s.handleBookCoverCustom)
	mux.HandleFunc("POST /api/v1/books/{id}/import", s.handleBookManualImport)
	mux.HandleFunc("PUT /api/v1/books/{id}/lock", s.handleBookLock)
	mux.HandleFunc("PUT /api/v1/books/{id}/library", s.handleBookMove)
	mux.HandleFunc("PUT /api/v1/libraries/{id}/books/{bookId}/monitored", s.handleBookMonitor)
	mux.HandleFunc("DELETE /api/v1/libraries/{id}/books/{bookId}", s.handleBookRemove)
	mux.HandleFunc("POST /api/v1/authors", s.handleAddAuthor)
	mux.HandleFunc("POST /api/v1/authors/{id}/expand", s.handleExpandAuthor)
	mux.HandleFunc("POST /api/v1/authors/{id}/search", s.handleAuthorSearch)
	mux.HandleFunc("DELETE /api/v1/authors/{id}", s.handleDeleteAuthor)
	mux.HandleFunc("PUT /api/v1/libraries/{id}/profile", s.handleSetLibraryProfile)
	mux.HandleFunc("POST /api/v1/series/{id}/library", s.handleSeriesAddToLibrary)
	mux.HandleFunc("POST /api/v1/import/review/{fileId}/ignore", s.handleReviewIgnore)
	mux.HandleFunc("POST /api/v1/import/review/{fileId}/match", s.handleReviewMatch)
	mux.HandleFunc("GET /api/v1/libraries", s.handleLibraries)
	mux.HandleFunc("POST /api/v1/libraries", s.handleCreateLibrary)
	mux.HandleFunc("DELETE /api/v1/libraries/{id}", s.handleDeleteLibrary)
	mux.HandleFunc("POST /api/v1/libraries/{id}/scan", s.handleScan)
	mux.HandleFunc("POST /api/v1/libraries/{id}/search", s.handleLibrarySearch)
	mux.HandleFunc("POST /api/v1/libraries/{id}/refresh", s.handleLibraryRefresh)
	mux.HandleFunc("GET /api/v1/libraries/{id}/review", s.handleReview)
	mux.HandleFunc("GET /api/v1/covers/{bookId}", s.handleCover)
	mux.HandleFunc("GET /api/v1/books/{id}/file", s.handleBookFile)
	mux.HandleFunc("GET /api/v1/books/{id}/progress", s.handleGetProgress)
	mux.HandleFunc("PUT /api/v1/books/{id}/progress", s.handlePutProgress)
	mux.HandleFunc("GET /api/v1/books/{id}/releases", s.handleReleases)
	mux.HandleFunc("POST /api/v1/books/{id}/grab", s.handleGrab)
	mux.HandleFunc("POST /api/v1/books/{id}/autograb", s.handleAutoGrab)
	mux.HandleFunc("GET /api/v1/queue", s.handleQueue)
	mux.HandleFunc("POST /api/v1/queue/{id}/retry", s.handleQueueRetry)
	mux.HandleFunc("GET /api/v1/wanted", s.handleWanted)
	mux.HandleFunc("GET /api/v1/history", s.handleHistory)
	mux.HandleFunc("GET /api/v1/profiles", s.handleProfiles)
	mux.HandleFunc("PUT /api/v1/profiles/{id}", s.handleUpdateProfile)
	mux.HandleFunc("POST /api/v1/system/test/prowlarr", s.handleTestProwlarr)
	mux.HandleFunc("POST /api/v1/system/test/sab", s.handleTestSab)
	mux.HandleFunc("POST /api/v1/system/test/zlib", s.handleTestZlib)
	mux.HandleFunc("POST /api/v1/system/test/hardcover", s.handleTestHardcover)
	mux.HandleFunc("POST /api/v1/system/test/grlist", s.handleTestGoodreadsList)
	mux.HandleFunc("POST /api/v1/system/discover/goodreads", s.handleDiscoverGoodreads)
	mux.HandleFunc("GET /api/v1/system/discover/hardcover", s.handleDiscoverHardcover)
	mux.HandleFunc("POST /api/v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /api/v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /api/v1/auth/me", s.handleMe)
	mux.HandleFunc("GET /api/v1/users", s.handleUsers)
	mux.HandleFunc("POST /api/v1/users", s.handleCreateUser)
	mux.HandleFunc("PUT /api/v1/users/{id}/libraries", s.handleSetUserLibraries)
	mux.HandleFunc("DELETE /api/v1/users/{id}", s.handleDeleteUser)
	mux.HandleFunc("PUT /api/v1/libraries/{id}/opds", s.handleSetOPDS)
	mux.HandleFunc("GET /api/v1/devices", s.handleDevices)
	mux.HandleFunc("POST /api/v1/devices", s.handleCreateDevice)
	mux.HandleFunc("DELETE /api/v1/devices/{id}", s.handleRevokeDevice)
	mux.HandleFunc("GET /api/v1/devices/{id}/plugin.zip", s.handlePluginZip)
	mux.HandleFunc("GET /api/koreader/v1/sync", s.handleKoSync)
	mux.HandleFunc("GET /api/koreader/v1/download/{libraryId}/{bookId}", s.handleKoDownload)
	mux.HandleFunc("GET /api/koreader/v1/cover/{libraryId}/{bookId}", s.handleKoCover)
	mux.HandleFunc("GET /api/v1/backups", s.handleBackups)
	mux.HandleFunc("POST /api/v1/backups", s.handleCreateBackup)
	mux.HandleFunc("POST /api/v1/backups/{name}/restore", s.handleRestoreBackup)
	mux.HandleFunc("POST /api/v1/system/restart", s.handleRestart)
	mux.HandleFunc("GET /api/v1/lists", s.handleLists)
	mux.HandleFunc("POST /api/v1/lists", s.handleCreateList)
	mux.HandleFunc("PUT /api/v1/lists/{id}", s.handleUpdateList)
	mux.HandleFunc("DELETE /api/v1/lists/{id}", s.handleDeleteList)
	mux.HandleFunc("POST /api/v1/lists/{id}/poll", s.handlePollList)
	mux.HandleFunc("GET /api/v1/calendar", s.handleCalendar)
	mux.HandleFunc("GET /api/v1/settings/{key}", s.handleGetSetting)
	mux.HandleFunc("PUT /api/v1/settings/{key}", s.handlePutSetting)
	s.OPDS.Register(mux)
	mux.Handle("/", s.spaHandler())
	return s.requireAuth(mux)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("api: encode response: %v", err)
	}
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	status := "ok"
	code := http.StatusOK
	if err := s.db.Ping(); err != nil {
		status = "degraded"
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, map[string]string{"app": "booky", "version": s.version, "status": status})
}

// spaHandler serves the built frontend, falling back to index.html for client
// routes. Paths are served only from the embedded FS — no disk access.
// index.html must revalidate on every load so a container update is picked up
// immediately; the hashed bundles under assets/ are immutable and cache hard.
func (s *Server) spaHandler() http.Handler {
	fileServer := http.FileServerFS(s.spa)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, "/")
		if p != "" {
			if f, err := s.spa.Open(p); err == nil {
				f.Close()
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				} else {
					w.Header().Set("Cache-Control", "no-cache")
				}
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		r.URL.Path = "/"
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}
