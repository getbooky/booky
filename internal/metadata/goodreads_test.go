package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const grFixtureNextData = `<html><body>
<script id="__NEXT_DATA__" type="application/json">{
  "props": {"pageProps": {"apolloState": {
    "Book:kca://book/amzn1.gr.book.v1.abc": {
      "legacyId": 77345210,
      "title": "First Ember",
      "description": "Twenty-year-old Wren Castellan...",
      "imageUrl": "https://images-na.ssl-images-amazon.com/books/fe.jpg",
      "webUrl": "https://www.goodreads.com/book/show/77345210-first-ember",
      "details": {
        "publisher": "Emberfall Press",
        "isbn": "1650000013",
        "isbn13": "9781650000013",
        "publicationTime": 1683007200000,
        "language": {"name": "English"}
      },
      "primaryContributorEdge": {"node": {"__ref": "Contributor:kca://author/1"}},
      "bookSeries": [{"userPosition": "1", "series": {"__ref": "Series:kca://series/1"}}],
      "work": {"__ref": "Work:kca://work/amzn1.gr.work.v3.xyz"}
    },
    "Contributor:kca://author/1": {"name": "Mara Voss"},
    "Series:kca://series/1": {"title": "The Ember Cycle"},
    "Work:kca://work/amzn1.gr.work.v3.xyz": {"stats": {"ratingsCount": 1500000}}
  }}}
}</script></body></html>`

func grTestServer(t *testing.T) (*httptest.Server, *Goodreads) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/book/auto_complete", func(w http.ResponseWriter, r *http.Request) {
		items := []map[string]any{
			{"bookId": 77345210, "title": "First Ember (The Ember Cycle, #1)", "bookTitleBare": "First Ember",
				"author": map[string]any{"name": "Mara Voss"}, "ratingsCount": "1,500,000",
				"imageUrl": "https://x/img._SX50_.jpg", "bookUrl": "/book/show/77345210-first-ember"},
			{"bookId": 999, "title": "Summary of First Ember: A Study Guide",
				"author": "Some Grifter", "ratingsCount": "12", "bookUrl": "/book/show/999"},
			{"bookId": 888, "title": "The Ember Cycle Boxed Set (Books 1-3)",
				"author": map[string]any{"name": "Mara Voss"}, "ratingsCount": "9000", "bookUrl": "/book/show/888"},
		}
		_ = json.NewEncoder(w).Encode(items)
	})
	mux.HandleFunc("/book/show/77345210", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, grFixtureNextData)
	})
	mux.HandleFunc("/book/isbn/9781650000013", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<link rel="canonical" href="https://www.goodreads.com/book/show/77345210-first-ember" />`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	g := NewGoodreads()
	g.BaseURL = srv.URL
	g.Delay = 0
	return srv, g
}

func TestGoodreadsSearchDetailParsing(t *testing.T) {
	_, g := grTestServer(t)
	results, err := g.Search(context.Background(), SearchParams{Title: "First Ember", Author: "Mara Voss", Limit: 1})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("no results")
	}
	m := results[0]
	checks := map[string]string{
		"title":       m.Title,
		"isbn13":      m.ISBN13,
		"goodreadsId": m.GoodreadsID,
		"series":      m.SeriesName,
		"publisher":   m.Publisher,
		"language":    m.Language,
		"releaseDate": m.ReleaseDate,
	}
	want := map[string]string{
		"title":       "First Ember",
		"isbn13":      "9781650000013",
		"goodreadsId": "77345210",
		"series":      "The Ember Cycle",
		"publisher":   "Emberfall Press",
		"language":    "English",
		"releaseDate": "2023-05-02",
	}
	for k, got := range checks {
		if got != want[k] {
			t.Errorf("%s = %q, want %q", k, got, want[k])
		}
	}
	if len(m.Authors) != 1 || m.Authors[0] != "Mara Voss" {
		t.Errorf("authors = %v", m.Authors)
	}
	if m.SeriesIndex != 1 {
		t.Errorf("series index = %v, want 1", m.SeriesIndex)
	}
	// stats live on the linked Work record in the current page layout
	if m.RatingsCount != 1500000 {
		t.Errorf("ratings = %d, want 1500000 (from Work stats)", m.RatingsCount)
	}
}

func TestGoodreadsRankingBuriesJunk(t *testing.T) {
	_, g := grTestServer(t)
	metas, err := g.autocomplete(context.Background(), SearchParams{Title: "First Ember", Author: "Mara Voss"})
	if err != nil {
		t.Fatalf("autocomplete: %v", err)
	}
	if len(metas) < 3 {
		t.Fatalf("expected 3 candidates, got %d", len(metas))
	}
	if metas[0].GoodreadsID != "77345210" {
		t.Errorf("top result = %s (%s), want the real book", metas[0].GoodreadsID, metas[0].Title)
	}
	if metas[0].Compilation {
		t.Error("the real book should not be flagged as a compilation")
	}
	for _, m := range metas[1:] {
		if m.GoodreadsID == "77345210" {
			t.Error("real book ranked below junk")
		}
	}
}

func TestGoodreadsResolveISBN(t *testing.T) {
	_, g := grTestServer(t)
	id, err := g.ResolveISBN(context.Background(), "9781650000013")
	if err != nil {
		t.Fatalf("ResolveISBN: %v", err)
	}
	if id != "77345210" {
		t.Errorf("id = %q, want 77345210", id)
	}
}

func TestGoodreadsWAFDetection(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/book/show/1", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `<html><script src="challenge.js"></script></html>`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := NewGoodreads()
	g.BaseURL = srv.URL
	meta, reachable, err := g.FetchBook(context.Background(), "1")
	if err != nil {
		t.Fatalf("FetchBook: %v", err)
	}
	if reachable {
		t.Error("WAF challenge should report unreachable")
	}
	if meta != nil {
		t.Error("WAF challenge should not yield metadata")
	}
}

func TestParseSeriesSuffix(t *testing.T) {
	name, idx := parseSeriesSuffix("First Ember (The Ember Cycle, #1)")
	if name != "The Ember Cycle" || idx != 1 {
		t.Errorf("got (%q, %v)", name, idx)
	}
	name, idx = parseSeriesSuffix("The Silent Comet")
	if name != "" || idx != 0 {
		t.Errorf("standalone should have no series, got (%q, %v)", name, idx)
	}
}

func TestIsCompilation(t *testing.T) {
	// keyword titles are caught by the (removable) default term list
	yes := []string{
		"The Ember Cycle Boxed Set (Books 1-3)",
		"Vault Trilogy",
		"The Complete Series",
		"Ashveil Omnibus",
		"Galewatch Archive Collection",
	}
	no := []string{
		"First Ember",
		"The Way of Sparrows",
		"A Court of Briars and Frost",
	}
	for _, title := range yes {
		if !Excluded(title, DefaultExcludeTerms) {
			t.Errorf("Excluded(%q, defaults) = false, want true", title)
		}
	}
	for _, title := range no {
		if Excluded(title, DefaultExcludeTerms) {
			t.Errorf("Excluded(%q, defaults) = true, want false", title)
		}
	}
}
