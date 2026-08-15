package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/getbooky/booky/internal/metadata"
)

// Library metadata refresh runs in the background: every book is re-resolved
// across the provider chain (locked fields survive — UpsertBook enforces
// that) and missing covers are fetched. One refresh per library at a time.

func (s *Server) handleLibraryRefresh(w http.ResponseWriter, r *http.Request) {
	libraryID, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	// re-pulling metadata from the providers is everyday work inside a
	// library, so a scoped user may run it on theirs
	if !s.requireLibrary(w, r, libraryID) {
		return
	}
	if _, loaded := s.refreshing.LoadOrStore(libraryID, true); loaded {
		writeErr(w, http.StatusConflict, errors.New("refresh already running for this library"))
		return
	}
	go s.runLibraryRefresh(libraryID)
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}

func (s *Server) runLibraryRefresh(libraryID int64) {
	defer s.refreshing.Delete(libraryID)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	books, err := s.Catalog.ListBooks(0, libraryID, 0)
	if err != nil {
		log.Printf("refresh: list books for library %d: %v", libraryID, err)
		return
	}
	updated := 0
	for _, b := range books {
		if ctx.Err() != nil {
			break
		}
		seed := metadata.BookMeta{
			Provider: "refresh", // never matches a provider key, so all are queried
			Title:    b.Title,
			Authors:  []string{b.Author},
			// the stored series rides along so a fuzzy provider answer can
			// only fill an empty series, never replace the one on the book
			SeriesName:  b.SeriesName,
			SeriesIndex: b.SeriesNum,
			ISBN13:      b.ISBN13,
			GoodreadsID: b.GoodreadsID,
			HardcoverID: b.HardcoverID,
		}
		enriched := s.Chain.Enrich(ctx, seed)
		if _, err := s.Catalog.UpsertBook(enriched); err != nil {
			log.Printf("refresh: upsert %q: %v", b.Title, err)
			continue
		}
		if err := s.Covers.Ensure(ctx, b.ID, enriched.CoverURL); err != nil {
			log.Printf("refresh: cover for %q: %v", b.Title, err)
		}
		updated++
		// politeness gap between books — provider clients pace themselves
		// per-request, this spreads the whole-library load
		select {
		case <-ctx.Done():
		case <-time.After(400 * time.Millisecond):
		}
	}
	detail := fmt.Sprintf("metadata refresh: %d of %d books checked", updated, len(books))
	if err := s.Catalog.AddHistory(0, libraryID, "refreshed", detail); err != nil {
		log.Printf("refresh: history: %v", err)
	}
	log.Printf("refresh: library %d done — %s", libraryID, detail)
}
