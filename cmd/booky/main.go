// Booky — ebook automation for your shelf.
package main

import (
	"context"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/getbooky/booky/internal/acquire"
	"github.com/getbooky/booky/internal/api"
	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/backup"
	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/importer"
	"github.com/getbooky/booky/internal/kindle"
	"github.com/getbooky/booky/internal/koreader"
	"github.com/getbooky/booky/internal/metadata"
	"github.com/getbooky/booky/internal/opds"
	"github.com/getbooky/booky/internal/secrets"
	"github.com/getbooky/booky/internal/settings"
	"github.com/getbooky/booky/internal/watcher"
	"github.com/getbooky/booky/web"
)

// set via -ldflags "-X main.version=..."
var version = "dev"

func main() {
	configDir := envOr("BOOKY_CONFIG_DIR", "/config")
	port := envOr("BOOKY_PORT", "8787")

	// keep recent log lines in memory for the Logs settings panel
	logs := api.NewLogRing(500)
	log.SetOutput(io.MultiWriter(os.Stderr, logs))

	if err := os.MkdirAll(configDir, 0o755); err != nil {
		log.Fatalf("create config dir %s: %v", configDir, err)
	}

	// a staged backup restore replaces the db before it opens
	if err := backup.SwapStaged(configDir); err != nil {
		log.Fatalf("apply staged restore: %v", err)
	}
	database, err := db.Open(filepath.Join(configDir, "booky.db"))
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer database.Close()

	cfg := settings.New(database)
	// credentials (SMTP passwords, provider tokens) are sealed at rest with a
	// key kept OUTSIDE the database, so backups of booky.db can't yield them
	keeper, err := secrets.Load(configDir)
	if err != nil {
		log.Fatalf("load secret key: %v", err)
	}
	cfg.UseKeeper(keeper)
	cat := catalog.New(database)
	covers := catalog.NewCoverCache(filepath.Join(configDir, "covers"))
	// author portraits live beside book covers, keyed by author id
	authorPhotos := catalog.NewCoverCache(filepath.Join(configDir, "covers", "authors"))
	// the default quality profile exists from first boot, not first library —
	// the Settings panel and wizard both edit it
	if _, err := cat.EnsureDefaultProfile(); err != nil {
		log.Fatalf("ensure default profile: %v", err)
	}

	// deletes drop cached images with their rows; the sweep clears files
	// orphaned before that behavior existed so reused ids start clean
	cat.Covers = covers
	cat.AuthorPhotos = authorPhotos
	if n, err := cat.SweepOrphanedCovers(); err != nil {
		log.Printf("cover cache sweep: %v", err)
	} else if n > 0 {
		log.Printf("cover cache: removed %d orphaned file(s)", n)
	}

	// Hardcover is the sole metadata authority — search, enrichment,
	// bibliographies, covers. Goodreads is only ever a list SOURCE (RSS
	// shelf feeds in the watcher) and the opt-in series-overlay client;
	// its metadata never populates books.
	hardcover := metadata.NewHardcover(func() string { return cfg.Get("hardcover_token") })
	chain := metadata.NewChain(func() []string { return []string{"hardcover"} }, hardcover)
	chain.Exclude = cfg.ExcludePatterns
	cat.Exclude = cfg.ExcludePatterns
	imp := importer.New(database, cat, chain)
	imp.Covers = covers
	engine := acquire.New(database, cat, cfg, imp, covers)
	backups := backup.New(database, configDir)
	watch := watcher.New(database, cat, chain, cfg, engine, covers, hardcover)
	watch.Backups = backups

	server, err := api.New(api.Deps{
		DB: database, Version: version, Dist: web.Dist,
		Catalog: cat, Chain: chain, Importer: imp, Covers: covers, AuthorPhotos: authorPhotos, Settings: cfg, Logs: logs,
		Acquire: engine, Watcher: watch,
		Auth: auth.New(database), KoReader: koreader.New(database), Kindle: kindle.New(database, keeper), Backups: backups,
		OPDS: opds.New(database, cat, covers),
	})
	if err != nil {
		log.Fatalf("init server: %v", err)
	}
	// new arrivals email themselves to auto-send Kindles; backgrounded so a
	// slow mail server never stalls a delivery
	imp.OnFileAdded = func(bookID, libraryID int64, path, format string) {
		go server.KindleAutoSend(bookID, libraryID, path, format)
	}

	// background loops: list polling, release-day triggers, download tracking
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go watch.Run(ctx)

	httpServer := &http.Server{
		Addr:              ":" + port,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()
	log.Printf("booky %s listening on :%s (config: %s)", version, port, configDir)
	if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
