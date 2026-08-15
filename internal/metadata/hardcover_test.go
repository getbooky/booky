package metadata

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeHardcover serves a canned GraphQL data payload and records the last
// query string the client sent.
func fakeHardcover(t *testing.T, data string) (*Hardcover, *string) {
	t.Helper()
	var lastQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		lastQuery = req.Query
		w.Header().Set("Content-Type", "application/json")
		if _, err := io.WriteString(w, `{"data":`+data+`}`); err != nil {
			t.Fatal(err)
		}
	}))
	t.Cleanup(srv.Close)
	h := NewHardcover(func() string { return "test-token" })
	h.BaseURL = srv.URL
	return h, &lastQuery
}

func TestHardcoverAuthorWorksFiltersToEnglish(t *testing.T) {
	// Shapes mirror the live schema: language lives on the default editions
	// (books have no language column), as language {language code3}.
	h, lastQuery := fakeHardcover(t, `{"books": [
		{"id": 1, "title": "The Final Ledger",
		 "default_cover_edition": {"language": {"language": "English", "code3": "eng"}}},
		{"id": 2, "title": "Rote Warnung",
		 "default_cover_edition": {"language": {"language": "German", "code3": "deu"}}},
		{"id": 3, "title": "No Language Recorded",
		 "default_cover_edition": {"language": null}},
		{"id": 4, "title": "No Cover Edition",
		 "default_cover_edition": null},
		{"id": 5, "title": "Ebook Says French",
		 "default_cover_edition": null,
		 "default_ebook_edition": {"language": {"language": "French", "code3": "fra"}}}
	]}`)

	metas, err := h.AuthorWorks(context.Background(), "Cade Rennick", 100)
	if err != nil {
		t.Fatal(err)
	}

	var titles []string
	for _, m := range metas {
		titles = append(titles, m.Title)
	}
	want := []string{"The Final Ledger", "No Language Recorded", "No Cover Edition"}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Errorf("filtered titles = %v, want %v", titles, want)
	}
	if metas[0].Language != "English" {
		t.Errorf("Language = %q, want English", metas[0].Language)
	}

	// The query must request edition languages and pre-filter server-side to
	// English-or-unknown so foreign records don't consume the limit. The
	// no-cover-edition arm matches the absent RELATION, not a null id —
	// dangling default_cover_edition_id values exist on live records
	// (a dangling cover-edition id) and an id-null check drops those books.
	for _, frag := range []string{
		"default_cover_edition { language { language code3 } }",
		`{_not: {default_cover_edition: {}}}`,
		`{default_cover_edition: {language_id: {_is_null: true}}}`,
		`{default_cover_edition: {language: {code3: {_eq: "eng"}}}}`,
	} {
		if !strings.Contains(*lastQuery, frag) {
			t.Errorf("query missing %q\nquery: %s", frag, *lastQuery)
		}
	}
}

// Ghost records — near-zero-usage duplicates for translations and retitled
// reprints, carrying no language anywhere — are dropped once the author has
// an established readership, while explicitly-English records always pass.
func TestHardcoverAuthorWorksDropsGhostRecords(t *testing.T) {
	h, _ := fakeHardcover(t, `{"books": [
		{"id": 1, "title": "The Final Ledger", "users_count": 490,
		 "default_cover_edition": {"language": {"language": "English", "code3": "eng"}}},
		{"id": 2, "title": "True Believer", "users_count": 259},
		{"id": 3, "title": "Targeted: Beirut", "users_count": 4,
		 "default_cover_edition": {"language": {"language": "English", "code3": "eng"}}},
		{"id": 4, "title": "Na seznamu smrti", "users_count": 1},
		{"id": 5, "title": "Cold Signal: A Final Ledger Thriller", "users_count": 0}
	]}`)
	metas, err := h.AuthorWorks(context.Background(), "Cade Rennick", 100)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, m := range metas {
		titles = append(titles, m.Title)
	}
	// cutoff = min(490/100, 5) = 4: unknown-language records need >4 users,
	// explicitly-English ones ("Targeted: Beirut" at 4) pass regardless
	want := []string{"The Final Ledger", "True Believer", "Targeted: Beirut"}
	if strings.Join(titles, "|") != strings.Join(want, "|") {
		t.Errorf("titles = %v, want %v", titles, want)
	}
	if metas[0].RatingsCount != 490 {
		t.Errorf("users_count not carried into RatingsCount: %+v", metas[0])
	}
}

// A niche author (low readership everywhere) must keep zero-user works —
// the ghost filter only arms once the author has real readership.
func TestHardcoverAuthorWorksKeepsNicheAuthorWorks(t *testing.T) {
	h, _ := fakeHardcover(t, `{"books": [
		{"id": 1, "title": "Obscure Debut", "users_count": 12},
		{"id": 2, "title": "Forgotten Sequel", "users_count": 0}
	]}`)
	metas, err := h.AuthorWorks(context.Background(), "Nobody Famous", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 {
		t.Errorf("niche author lost works: %v", metas)
	}
}

// Title search goes through the search endpoint (the live API rejects
// _ilike), then hydrates the ranked hit ids with a books-by-id query.
func TestHardcoverByTitleUsesSearchEndpoint(t *testing.T) {
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		queries = append(queries, req.Query)
		w.Header().Set("Content-Type", "application/json")
		var data string
		switch {
		case strings.Contains(req.Query, "search("):
			// relevance order: 7 first, then 9
			data = `{"search": {"results": {"hits": [
				{"document": {"id": "7"}}, {"document": {"id": "9"}}
			]}}}`
		case strings.Contains(req.Query, "_in"):
			// by-id hydration returns arbitrary order
			data = `{"books": [
				{"id": 9, "title": "The Final Ledger: A Thriller"},
				{"id": 7, "title": "The Final Ledger"}
			]}`
		default:
			t.Errorf("unexpected query: %s", req.Query)
			data = `{}`
		}
		if _, err := io.WriteString(w, `{"data":`+data+`}`); err != nil {
			t.Fatal(err)
		}
	}))
	t.Cleanup(srv.Close)
	h := NewHardcover(func() string { return "test-token" })
	h.BaseURL = srv.URL

	metas, err := h.Search(context.Background(), SearchParams{Title: "The Final Ledger", Author: "Cade Rennick", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(queries) != 2 || !strings.Contains(queries[0], "search(") {
		t.Fatalf("expected search + hydration queries, got %d: %v", len(queries), queries)
	}
	if strings.Contains(queries[0], "_ilike") || strings.Contains(queries[1], "_ilike") {
		t.Error("query still uses the banned _ilike operator")
	}
	if len(metas) != 2 || metas[0].HardcoverID != "7" || metas[1].HardcoverID != "9" {
		t.Errorf("results not in search relevance order: %+v", metas)
	}
}

// cached_tags carries genre data under the "Genre" category, either as a
// JSON object or double-encoded as a string; both shapes must parse.
func TestHardcoverGenresFromCachedTags(t *testing.T) {
	obj := `{"Genre": [{"tag": "Thriller"}, {"tag": "Fiction"}, {"tag": ""}], "Mood": [{"tag": "tense"}]}`
	want := "Thriller|Fiction"
	if got := strings.Join(hcGenres(json.RawMessage(obj)), "|"); got != want {
		t.Errorf("object form: genres = %q, want %q", got, want)
	}
	encoded, _ := json.Marshal(obj)
	if got := strings.Join(hcGenres(json.RawMessage(encoded)), "|"); got != want {
		t.Errorf("string form: genres = %q, want %q", got, want)
	}
	if hcGenres(nil) != nil || hcGenres(json.RawMessage(`"not json at all`)) != nil {
		t.Error("malformed cached_tags should yield no genres, not an error")
	}
}

func TestHardcoverBookLanguageFallsBackAcrossEditions(t *testing.T) {
	var b hcBook
	if got := b.language(); got != "" {
		t.Errorf("no editions: language() = %q, want empty", got)
	}
	if err := json.Unmarshal([]byte(`{
		"default_cover_edition": {"language": null},
		"default_ebook_edition": {"language": {"language": "", "code3": "eng"}},
		"default_physical_edition": {"language": {"language": "Spanish", "code3": "spa"}}
	}`), &b); err != nil {
		t.Fatal(err)
	}
	// cover has none → ebook consulted first, its code3 wins over physical
	if got := b.language(); got != "eng" {
		t.Errorf("language() = %q, want eng", got)
	}
}

func TestEnglishOrUnknown(t *testing.T) {
	for lang, want := range map[string]bool{
		"":         true,
		"English":  true,
		"english":  true,
		"eng":      true,
		"en":       true,
		"German":   false,
		"deu":      false,
		"fra":      false,
		"Spanish":  false,
		"  ":       true,
		"en-US":    true,
		"Japanese": false,
	} {
		if got := englishOrUnknown(lang); got != want {
			t.Errorf("englishOrUnknown(%q) = %v, want %v", lang, got, want)
		}
	}
}

// The root-cause regression for watched-list matching: the Typesense search
// endpoint returns zero hits when the author is concatenated onto a noisy
// Goodreads title. The search query must be the bare title with its series
// parenthetical stripped; the author only verifies the hydrated results.
func TestHardcoverByTitleSearchesCleanTitleWithoutAuthor(t *testing.T) {
	var searchQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		var data string
		switch {
		case strings.Contains(req.Query, "search("):
			q, _ := req.Variables["q"].(string)
			searchQueries = append(searchQueries, q)
			data = `{"search": {"results": {"hits": [{"document": {"id": "42"}}]}}}`
		case strings.Contains(req.Query, "_in"):
			data = `{"books": [{"id": 42, "title": "The Wolves of Calder",
				"contributions": [{"author": {"name": "D.K. Vale"}}]}]}`
		default:
			t.Errorf("unexpected query: %s", req.Query)
			data = `{}`
		}
		if _, err := io.WriteString(w, `{"data":`+data+`}`); err != nil {
			t.Fatal(err)
		}
	}))
	t.Cleanup(srv.Close)
	h := NewHardcover(func() string { return "test-token" })
	h.BaseURL = srv.URL

	metas, err := h.Search(context.Background(), SearchParams{
		Title:  "The Wolves of Calder (The Silas Calder Series Book 1)",
		Author: "D.K. Vale",
		Limit:  5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(searchQueries) != 1 || searchQueries[0] != "The Wolves of Calder" {
		t.Errorf("search query = %v, want just the cleaned title", searchQueries)
	}
	if len(metas) != 1 || metas[0].HardcoverID != "42" {
		t.Errorf("verified result lost: %+v", metas)
	}
}

// Very generic titles can crowd the right book out of a bare-title search;
// when nothing verifies, one retry appends the author to disambiguate.
func TestHardcoverByTitleRetriesWithAuthor(t *testing.T) {
	var searchQueries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query     string         `json:"query"`
			Variables map[string]any `json:"variables"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		var data string
		switch {
		case strings.Contains(req.Query, "search("):
			q, _ := req.Variables["q"].(string)
			searchQueries = append(searchQueries, q)
			if len(searchQueries) == 1 {
				data = `{"search": {"results": {"hits": []}}}` // bare title finds nothing
			} else {
				data = `{"search": {"results": {"hits": [{"document": {"id": "9"}}]}}}`
			}
		case strings.Contains(req.Query, "_in"):
			data = `{"books": [{"id": 9, "title": "The Return",
				"contributions": [{"author": {"name": "Nicholas Sparks"}}]}]}`
		default:
			t.Errorf("unexpected query: %s", req.Query)
			data = `{}`
		}
		if _, err := io.WriteString(w, `{"data":`+data+`}`); err != nil {
			t.Fatal(err)
		}
	}))
	t.Cleanup(srv.Close)
	h := NewHardcover(func() string { return "test-token" })
	h.BaseURL = srv.URL

	metas, err := h.Search(context.Background(), SearchParams{Title: "The Return", Author: "Nicholas Sparks", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"The Return", "The Return Nicholas Sparks"}
	if strings.Join(searchQueries, "|") != strings.Join(want, "|") {
		t.Errorf("search queries = %v, want %v", searchQueries, want)
	}
	if len(metas) != 1 || metas[0].HardcoverID != "9" {
		t.Errorf("retry result lost: %+v", metas)
	}
}

// AuthorInfo prefers a record carrying presentation data and survives the
// beta schema rejecting the users_count ordering (fallback to unordered).
func TestHardcoverAuthorInfo(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		calls++
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(req.Query, "order_by") {
			_, _ = io.WriteString(w, `{"errors":[{"message":"field 'users_count' not found in type: 'authors_order_by'"}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{"authors":[
			{"id": 1, "name": "Tess Arden", "bio": "", "cached_image": null},
			{"id": 2, "name": "Tess Arden", "bio": "Wrote Loom in his spare time.",
			 "cached_image": {"url": "https://assets.hardcover.app/authors/2.jpg"}}
		]}}`)
	}))
	t.Cleanup(srv.Close)
	h := NewHardcover(func() string { return "test-token" })
	h.BaseURL = srv.URL

	info, err := h.AuthorInfo(context.Background(), "Tess Arden")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("expected ordered query + unordered fallback, got %d calls", calls)
	}
	if info == nil || info.Bio != "Wrote Loom in his spare time." || info.ImageURL != "https://assets.hardcover.app/authors/2.jpg" {
		t.Errorf("info = %+v — should prefer the record with bio/portrait", info)
	}

	// unknown author: empty result, no error
	h2, _ := fakeHardcover(t, `{"authors": []}`)
	info2, err := h2.AuthorInfo(context.Background(), "Nobody At All")
	if err != nil || info2 != nil {
		t.Errorf("unknown author should yield nil info, got %+v err %v", info2, err)
	}
}

// A bare search-bar query mixing title and author words — or a plain author
// name — must keep Hardcover's correct hits instead of title-scoring them to
// zero and falling through to Open Library.
func TestHardcoverSearchBareQueryMatchesAuthor(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("bad request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		var data string
		switch {
		case strings.Contains(req.Query, "search("):
			data = `{"search": {"results": {"hits": [
				{"document": {"id": "1"}}, {"document": {"id": "2"}}, {"document": {"id": "3"}}
			]}}}`
		case strings.Contains(req.Query, "_in"):
			data = `{"books": [
				{"id": 1, "title": "The Final Ledger",
				 "contributions": [{"author": {"id": 5, "name": "Cade Rennick"}}]},
				{"id": 2, "title": "True Believer",
				 "contributions": [{"author": {"id": 5, "name": "Cade Rennick"}}]},
				{"id": 3, "title": "Unrelated Novel",
				 "contributions": [{"author": {"id": 6, "name": "Somebody Else"}}]}
			]}`
		default:
			t.Errorf("unexpected query: %s", req.Query)
			data = `{}`
		}
		if _, err := io.WriteString(w, `{"data":`+data+`}`); err != nil {
			t.Fatal(err)
		}
	}))
	t.Cleanup(srv.Close)
	h := NewHardcover(func() string { return "test-token" })
	h.BaseURL = srv.URL

	// plain author name: both of the author's books survive, the stranger dies
	metas, err := h.Search(context.Background(), SearchParams{Query: "cade rennick", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 2 || metas[0].Title != "The Final Ledger" || metas[1].Title != "True Believer" {
		t.Errorf("author-name query results = %+v, want the author's two books", metas)
	}

	// title + author mash: the matching book survives
	metas, err = h.Search(context.Background(), SearchParams{Query: "final ledger cade rennick", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas) != 1 || metas[0].Title != "The Final Ledger" {
		t.Errorf("mash query results = %+v, want just The Final Ledger", metas)
	}

	// a structured title+author search keeps the strict gate
	metas, err = h.Search(context.Background(), SearchParams{Title: "The Final Ledger", Author: "Cade Rennick", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range metas {
		if m.Title == "Unrelated Novel" {
			t.Errorf("structured search admitted an unrelated hit: %+v", metas)
		}
	}
}

func TestHardcoverFetchByHardcoverID(t *testing.T) {
	h, lastQuery := fakeHardcover(t, `{"books": [
		{"id": 42, "title": "The Current", "description": "A guide on the water.",
		 "release_date": "2011-09-01",
		 "contributions": [{"author": {"id": 7, "name": "Nora Whitfield"}}],
		 "book_series": [{"position": 1, "series": {"id": 3, "name": "Current"}}]}
	]}`)

	m, err := h.FetchByHardcoverID(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.HardcoverID != "42" || m.Title != "The Current" || m.SeriesName != "Current" {
		t.Fatalf("meta = %+v", m)
	}
	if !strings.Contains(*lastQuery, "id: {_eq: $id}") {
		t.Errorf("query should fetch by exact id: %s", *lastQuery)
	}
}

// A dangling id (deleted/merged record) is a soft miss, not an error — the
// caller falls back to its stored metadata.
func TestHardcoverFetchByHardcoverIDMissing(t *testing.T) {
	h, _ := fakeHardcover(t, `{"books": []}`)
	m, err := h.FetchByHardcoverID(context.Background(), "42")
	if err != nil || m != nil {
		t.Fatalf("missing id should be (nil, nil), got %+v, %v", m, err)
	}
}

func TestHardcoverFetchByHardcoverIDRejectsGarbage(t *testing.T) {
	h, _ := fakeHardcover(t, `{"books": []}`)
	if _, err := h.FetchByHardcoverID(context.Background(), "not-a-number"); err == nil {
		t.Fatal("non-numeric id should error, not query")
	}
}
