package metadata

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Live integration tests against the real Goodreads site (no key — the
// scraper paths: autocomplete JSON, detail-page __NEXT_DATA__, ISBN
// redirect). Opt in with:
//
//	GOODREADS_LIVE=1 go test ./internal/metadata/ -run TestGoodreadsLive -v
//
// Goodreads detail pages sit behind an intermittent AWS WAF challenge, so
// detail-dependent tests SKIP (not fail) when blocked — a block is an
// environment condition, not a code defect. TestGoodreadsLiveEnrichBridge
// additionally uses HARDCOVER_TOKEN, when set, to exercise the real
// Hardcover→Goodreads enrichment path the app runs in production.

func liveGoodreads(t *testing.T) *Goodreads {
	t.Helper()
	if os.Getenv("GOODREADS_LIVE") == "" {
		t.Skip("GOODREADS_LIVE not set — skipping live Goodreads test")
	}
	return NewGoodreads()
}

func TestGoodreadsLiveAutocomplete(t *testing.T) {
	g := liveGoodreads(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	metas, err := g.autocomplete(ctx, SearchParams{Title: "The Terminal List", Author: "Jack Carr"})
	if err != nil {
		t.Fatalf("autocomplete: %v", err)
	}
	if len(metas) == 0 {
		t.Fatal("autocomplete returned nothing for a bestseller")
	}
	top := metas[0]
	if !strings.Contains(strings.ToLower(top.Title), "terminal list") {
		t.Errorf("top result %q does not match the query", top.Title)
	}
	if top.GoodreadsID == "" {
		t.Error("top result has no Goodreads id")
	}
	// autocomplete results must be unique by id (the id is the identity the
	// catalog dedupes watched-list entries on)
	seen := map[string]bool{}
	for _, m := range metas {
		if seen[m.GoodreadsID] {
			t.Errorf("duplicate Goodreads id %s in autocomplete results", m.GoodreadsID)
		}
		seen[m.GoodreadsID] = true
		t.Logf("result %q id=%s series=%q ratings=%d", m.Title, m.GoodreadsID, m.SeriesName, m.RatingsCount)
	}
}

func TestGoodreadsLiveFetchBook(t *testing.T) {
	g := liveGoodreads(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Fourth Wing — a stable, heavily-trafficked record
	meta, reachable, err := g.FetchBook(ctx, "61431922")
	if err != nil {
		t.Fatalf("FetchBook: %v", err)
	}
	if !reachable {
		t.Skip("Goodreads WAF challenge active — detail pages unreachable from here")
	}
	if meta == nil {
		t.Fatal("detail page loaded but did not parse — __NEXT_DATA__ layout drifted?")
	}
	if meta.Title != "Fourth Wing" {
		t.Errorf("Title = %q, want Fourth Wing", meta.Title)
	}
	if len(meta.Authors) == 0 || meta.Authors[0] != "Rebecca Yarros" {
		t.Errorf("Authors = %v, want [Rebecca Yarros]", meta.Authors)
	}
	if meta.SeriesName != "The Empyrean" || meta.SeriesIndex != 1 {
		t.Errorf("Series = %q #%g, want The Empyrean #1", meta.SeriesName, meta.SeriesIndex)
	}
	if !englishOrUnknown(meta.Language) {
		t.Errorf("Language = %q — an English record must never read as foreign", meta.Language)
	}
	if meta.ISBN13 == "" && meta.ISBN10 == "" {
		t.Error("detail fetch carried no ISBN")
	}
	if meta.Description == "" {
		t.Error("detail fetch carried no description")
	}
	if meta.RatingsCount == 0 {
		t.Error("ratings count is 0 for a bestseller — stats layout drifted again?")
	}
	if len(meta.Genres) == 0 {
		t.Error("detail fetch carried no genres")
	}
	t.Logf("full record: lang=%q isbn13=%s release=%s ratings=%d genres=%v",
		meta.Language, meta.ISBN13, meta.ReleaseDate, meta.RatingsCount, meta.Genres)
}

func TestGoodreadsLiveResolveISBN(t *testing.T) {
	g := liveGoodreads(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id, err := g.ResolveISBN(ctx, "9781649374042")
	if err != nil {
		t.Fatalf("ResolveISBN: %v", err)
	}
	if id == "" {
		t.Skip("ISBN redirect gave no canonical id — likely WAF challenge")
	}
	if id != "61431922" {
		t.Errorf("ResolveISBN = %s, want 61431922", id)
	}
}

// TestGoodreadsLiveWatchedListEntry simulates exactly what the watcher hands
// the chain when a Goodreads shelf feed item arrives: a thin reference
// (Goodreads id + title + author, usually no ISBN), provider "goodreads_rss".
// Enrichment must resolve the full Goodreads record by id AND cross-match the
// correct Hardcover book so the catalog converges on both identities.
func TestGoodreadsLiveWatchedListEntry(t *testing.T) {
	g := liveGoodreads(t)
	token := os.Getenv("HARDCOVER_TOKEN")
	if token == "" {
		t.Skip("HARDCOVER_TOKEN not set — skipping watched-list simulation")
	}
	h := NewHardcover(func() string { return token })
	chain := NewChain(func() []string { return []string{"hardcover", "goodreads"} }, h, g)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	// mirror of watcher.routeBook's seed for an RSS entry
	seed := BookMeta{
		Provider:    "goodreads_rss",
		Title:       "The Terminal List",
		Authors:     []string{"Jack Carr"},
		GoodreadsID: "35297106",
	}
	enriched := chain.Enrich(ctx, seed)
	t.Logf("enriched: goodreads=%s hardcover=%s isbn13=%s series=%q #%g lang=%q genres=%v ratings=%d",
		enriched.GoodreadsID, enriched.HardcoverID, enriched.ISBN13,
		enriched.SeriesName, enriched.SeriesIndex, enriched.Language,
		enriched.Genres, enriched.RatingsCount)

	if enriched.HardcoverID != "375707" {
		t.Errorf("Hardcover matched id %q, want 375707 (The Terminal List)", enriched.HardcoverID)
	}
	if enriched.GoodreadsID != "35297106" {
		t.Error("enrichment must never lose the feed's Goodreads id")
	}
	if enriched.SeriesName == "" {
		t.Error("series info did not arrive from either provider")
	}
	if enriched.Description == "" {
		t.Error("description did not arrive from either provider")
	}
	if !englishOrUnknown(enriched.Language) {
		t.Errorf("language resolved foreign: %q", enriched.Language)
	}
}

// TestGoodreadsLiveEnrichBridge exercises the production path: Hardcover
// originates the record, Goodreads fills the gaps (Goodreads id, genres,
// ratings) via the chain's title/author fallback lookup.
func TestGoodreadsLiveEnrichBridge(t *testing.T) {
	g := liveGoodreads(t)
	token := os.Getenv("HARDCOVER_TOKEN")
	if token == "" {
		t.Skip("HARDCOVER_TOKEN not set — skipping cross-provider enrich test")
	}
	h := NewHardcover(func() string { return token })
	chain := NewChain(func() []string { return []string{"hardcover", "goodreads"} }, h, g)

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	seeds, err := chain.Search(ctx, SearchParams{Title: "The Terminal List", Author: "Jack Carr", Limit: 3})
	if err != nil {
		t.Fatalf("chain search: %v", err)
	}
	if len(seeds) == 0 {
		t.Fatal("chain search returned nothing")
	}
	seed := seeds[0]
	if seed.Provider != "hardcover" || seed.HardcoverID == "" {
		t.Fatalf("expected Hardcover to originate the result, got %+v", seed)
	}

	enriched := chain.Enrich(ctx, seed)
	t.Logf("seed:     hardcover=%s goodreads=%s isbn13=%s genres=%v",
		seed.HardcoverID, seed.GoodreadsID, seed.ISBN13, seed.Genres)
	t.Logf("enriched: hardcover=%s goodreads=%s isbn13=%s ratings=%d genres=%v lang=%q",
		enriched.HardcoverID, enriched.GoodreadsID, enriched.ISBN13,
		enriched.RatingsCount, enriched.Genres, enriched.Language)

	if enriched.GoodreadsID == "" {
		t.Error("enrichment did not bridge to a Goodreads id")
	}
	if enriched.HardcoverID != seed.HardcoverID {
		t.Error("enrichment must never overwrite the seed's Hardcover id")
	}
	if !englishOrUnknown(enriched.Language) {
		t.Errorf("enrichment pulled in a foreign language: %q", enriched.Language)
	}
}
