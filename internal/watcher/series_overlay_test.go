package watcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/getbooky/booky/internal/metadata"
)

// grFixture serves a Goodreads book page (for series discovery) and a
// mutable series page, mirroring the live layout.
type grFixture struct {
	mu         sync.Mutex
	seriesJSON string
}

func (f *grFixture) setSeries(blocks string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.seriesJSON = blocks
}

func (f *grFixture) handler() http.Handler {
	escape := strings.NewReplacer(`"`, "&quot;", "<", "&lt;", ">", "&gt;")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/book/show/9001"):
			_, _ = fmt.Fprintf(w, `<html><script id="__NEXT_DATA__" type="application/json">
			{"props":{"pageProps":{"apolloState":{
			  "Book:b1":{"legacyId":9001,"webUrl":"https://www.goodreads.com/book/show/9001-first",
			    "bookSeries":[{"userPosition":"1","series":{"__ref":"Series:s1"}}]},
			  "Series:s1":{"title":"Testverse","webUrl":"https://www.goodreads.com/series/555-testverse"}
			}}}}</script></html>`)
		case strings.HasPrefix(r.URL.Path, "/series/555"):
			f.mu.Lock()
			blocks := f.seriesJSON
			f.mu.Unlock()
			_, _ = fmt.Fprintf(w, `<html><div data-react-class="ReactComponents.SeriesList" data-react-props="%s"></div></html>`,
				escape.Replace(blocks))
		default:
			http.NotFound(w, r)
		}
	})
}

func grEntry(id, title, author, year string, pos string, ratings int) string {
	return fmt.Sprintf(`{"book":{"bookId":"%s","bookTitleBare":"%s","imageUrl":"","ratingsCount":%d,
	  "publicationDate":"%s","author":{"name":"%s"},"description":{"html":"About %s."}}}`,
		id, title, ratings, year, author, title)
}

func seriesJSON(headers []string, entries []string) string {
	return fmt.Sprintf(`{"series":[%s],"seriesHeaders":["%s"]}`,
		strings.Join(entries, ","), strings.Join(headers, `","`))
}

func TestSeriesOverlayAddsAnnouncedAndFiltersJunk(t *testing.T) {
	w, cat, cfg, libID := testWatcher(t)
	fixture := &grFixture{}
	srv := httptest.NewServer(fixture.handler())
	t.Cleanup(srv.Close)
	w.GRSeries = metadata.NewGoodreads()
	w.GRSeries.BaseURL = srv.URL
	w.GRSeries.Delay = 0
	if err := cfg.Set("series_overlay", "true"); err != nil {
		t.Fatal(err)
	}

	// the author's synced bibliography: book #1 of Testverse, on a shelf
	seed := metadata.BookMeta{
		Provider: "stub", Title: "First Spark", Authors: []string{"Terra Nova"},
		SeriesName: "Testverse", SeriesIndex: 1, GoodreadsID: "9001",
	}
	bookID, err := cat.UpsertBook(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(bookID, libID, false); err != nil {
		t.Fatal(err)
	}
	b, err := cat.GetBook(bookID)
	if err != nil {
		t.Fatal(err)
	}
	w.chain = metadata.NewChain(func() []string { return []string{"stub"} }, &worksStub{
		works: []metadata.BookMeta{seed},
	})

	nextYear := fmt.Sprintf("%d", time.Now().Year()+1)
	farYear := fmt.Sprintf("%d", time.Now().Year()+3)
	fixture.setSeries(seriesJSON(
		[]string{"Book 1", "Book 1.5", "Book 2", "Book 1-3", "Book 2, part 1", "Book 3", "Book 4", "Book 5"},
		[]string{
			grEntry("9001", "First Spark", "Terra Nova", "2020", "1", 5000),         // already present
			grEntry("9002", "The Interlude", "Terra Nova", "2021", "1.5", 900),      // novella with readership -> add
			grEntry("9003", "Second Strike", "Someone Else", "2022", "2", 8000),     // wrong author -> skip
			grEntry("9004", "Testverse Collection", "Terra Nova", "", "1-3", 2000),  // box set position -> skip
			grEntry("9005", "Zweiter Teil", "Terra Nova", "2022", "2, part 1", 500), // split position -> skip
			grEntry("9006", "Obscure Reprint", "Terra Nova", "2019", "3", 12),       // backlist, no readership -> skip
			grEntry("9007", "The Announced One", "Terra Nova", nextYear, "4", 0),    // due within the horizon -> add
			grEntry("9008", "Distant Vapor", "Terra Nova", farYear, "5", 0),         // years out -> skip until it nears
		}))

	added, err := w.ExpandAuthor(context.Background(), b.AuthorID, Attach{})
	if err != nil {
		t.Fatal(err)
	}
	_ = added

	books, err := cat.ListBooks(b.AuthorID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]bool{}
	var announcedYear string
	for _, bk := range books {
		byTitle[bk.Title] = true
		if bk.Title == "The Announced One" {
			announcedYear = bk.ReleaseDate
		}
	}
	for _, want := range []string{"First Spark", "The Interlude", "The Announced One"} {
		if !byTitle[want] {
			t.Errorf("missing %q after overlay; have %v", want, byTitle)
		}
	}
	for _, junk := range []string{"Second Strike", "Testverse Collection", "Zweiter Teil", "Obscure Reprint", "Distant Vapor"} {
		if byTitle[junk] {
			t.Errorf("junk %q slipped through the overlay", junk)
		}
	}
	if announcedYear != nextYear {
		t.Errorf("announced book release = %q, want %q", announcedYear, nextYear)
	}

	// the discovered Goodreads series id is remembered — no rediscovery fetch
	var grID string
	if err := w.db.QueryRow(`SELECT COALESCE(goodreads_id,'') FROM series WHERE name = 'Testverse'`).Scan(&grID); err != nil {
		t.Fatal(err)
	}
	if grID != "555" {
		t.Errorf("series goodreads_id = %q, want 555", grID)
	}

	// second sync: overlay rows are touched, so the stale prune keeps them
	if _, err := w.ExpandAuthor(context.Background(), b.AuthorID, Attach{}); err != nil {
		t.Fatal(err)
	}
	books, _ = cat.ListBooks(b.AuthorID, 0, 0)
	if len(books) != 3 {
		t.Fatalf("after resync want 3 books, got %d: %+v", len(books), books)
	}

	// Goodreads drops the announced book (retitled, pulled): the next sync's
	// prune removes the catalog-only leftover — the overlay self-heals
	fixture.setSeries(seriesJSON(
		[]string{"Book 1", "Book 1.5"},
		[]string{
			grEntry("9001", "First Spark", "Terra Nova", "2020", "1", 5000),
			grEntry("9002", "The Interlude", "Terra Nova", "2021", "1.5", 900),
		}))
	if _, err := w.ExpandAuthor(context.Background(), b.AuthorID, Attach{}); err != nil {
		t.Fatal(err)
	}
	books, _ = cat.ListBooks(b.AuthorID, 0, 0)
	for _, bk := range books {
		if bk.Title == "The Announced One" {
			t.Errorf("withdrawn announced book survived the prune")
		}
	}
}

// Turning the setting off doesn't just stop new adds: the rows the overlay
// added stop being confirmed, so each author's next sync prunes them — the
// catalog returns to the pure Hardcover view on its own. Books the user
// shelved stay: they're the user's curation, not overlay residue.
func TestSeriesOverlayDisableCleansUp(t *testing.T) {
	w, cat, cfg, libID := testWatcher(t)
	fixture := &grFixture{}
	srv := httptest.NewServer(fixture.handler())
	t.Cleanup(srv.Close)
	w.GRSeries = metadata.NewGoodreads()
	w.GRSeries.BaseURL = srv.URL
	w.GRSeries.Delay = 0
	if err := cfg.Set("series_overlay", "true"); err != nil {
		t.Fatal(err)
	}

	seed := metadata.BookMeta{
		Provider: "stub", Title: "First Spark", Authors: []string{"Terra Nova"},
		SeriesName: "Testverse", SeriesIndex: 1, GoodreadsID: "9001",
	}
	bookID, err := cat.UpsertBook(seed)
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(bookID, libID, false); err != nil {
		t.Fatal(err)
	}
	b, err := cat.GetBook(bookID)
	if err != nil {
		t.Fatal(err)
	}
	w.chain = metadata.NewChain(func() []string { return []string{"stub"} }, &worksStub{
		works: []metadata.BookMeta{seed},
	})
	fixture.setSeries(seriesJSON(
		[]string{"Book 1", "Book 1.5", "Book 2"},
		[]string{
			grEntry("9001", "First Spark", "Terra Nova", "2020", "1", 5000),
			grEntry("9002", "The Interlude", "Terra Nova", "2021", "1.5", 900),
			grEntry("9003", "Kept By Hand", "Terra Nova", "2022", "2", 900),
		}))

	if _, err := w.ExpandAuthor(context.Background(), b.AuthorID, Attach{}); err != nil {
		t.Fatal(err)
	}
	books, _ := cat.ListBooks(b.AuthorID, 0, 0)
	if len(books) != 3 {
		t.Fatalf("overlay on: want 3 books, got %d", len(books))
	}
	// the user shelves one overlay find before switching the feature off
	for _, bk := range books {
		if bk.Title == "Kept By Hand" {
			if err := cat.AddToLibrary(bk.ID, libID, false); err != nil {
				t.Fatal(err)
			}
		}
	}

	if err := cfg.Set("series_overlay", "false"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ExpandAuthor(context.Background(), b.AuthorID, Attach{}); err != nil {
		t.Fatal(err)
	}
	books, _ = cat.ListBooks(b.AuthorID, 0, 0)
	titles := map[string]bool{}
	for _, bk := range books {
		titles[bk.Title] = true
	}
	if titles["The Interlude"] {
		t.Error("catalog-only overlay row survived after the setting was turned off")
	}
	if !titles["Kept By Hand"] {
		t.Error("shelved overlay find was pruned — user curation must survive")
	}
	if !titles["First Spark"] {
		t.Error("the ordinary bibliography book disappeared")
	}
}
