package watcher

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/getbooky/booky/internal/acquire"
	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/importer"
	"github.com/getbooky/booky/internal/metadata"
	"github.com/getbooky/booky/internal/settings"
)

func testWatcher(t *testing.T) (*Watcher, *catalog.Store, *settings.Store, int64) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cat := catalog.New(conn)
	cfg := settings.New(conn)
	// no source ever gets contacted during automatic searches in tests:
	// prowlarr/zlib are unconfigured; annas points at a dead local port
	if err := cfg.Set("annas_mirrors", "http://127.0.0.1:9"); err != nil {
		t.Fatal(err)
	}
	profile, _ := cat.EnsureDefaultProfile()
	libID, err := cat.CreateLibrary("Alex", t.TempDir(), profile, "alex", "x")
	if err != nil {
		t.Fatal(err)
	}
	chain := metadata.NewChain(func() []string { return nil }) // no providers: Enrich is a no-op
	imp := importer.New(conn, cat, chain)
	covers := catalog.NewCoverCache(t.TempDir())
	engine := acquire.New(conn, cat, cfg, imp, covers)
	hc := metadata.NewHardcover(func() string { return "" })
	w := New(conn, cat, chain, cfg, engine, covers, hc)
	w.Pace = 0
	// the Goodreads series overlay is exercised against a fixture server in
	// series_overlay_test.go; everywhere else it must never touch the network
	w.GRSeries = nil
	return w, cat, cfg, libID
}

const shelfRSS = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0"><channel>
  <title>Alex's bookshelf: to-read</title>
  %s
</channel></rss>`

func feedItem(id, title, author, isbn string) string {
	return fmt.Sprintf(`<item>
	  <title><![CDATA[%s]]></title>
	  <book_id>%s</book_id>
	  <author_name>%s</author_name>
	  <isbn>%s</isbn>
	</item>`, title, id, author, isbn)
}

func TestParseShelfRSS(t *testing.T) {
	body := fmt.Sprintf(shelfRSS,
		feedItem("77345210", "First Ember (The Ember Cycle, #1)", "Mara Voss", "9781650000013")+
			feedItem("777", "No ISBN Book", "Someone", ""))
	entries, err := parseShelfRSS([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 entries, got %d", len(entries))
	}
	first := entries[0]
	if first.GoodreadsID != "77345210" || first.Author != "Mara Voss" || first.ISBN13 != "9781650000013" {
		t.Fatalf("bad entry: %+v", first)
	}
	if entries[1].ISBN13 != "" {
		t.Fatalf("10-digit/empty isbn must not land in ISBN13: %+v", entries[1])
	}
}

func TestParseGoodreadsRef(t *testing.T) {
	cases := []struct{ input, shelf, want string }{
		{"https://www.goodreads.com/user/show/12345-alex", "", "12345/to-read"},
		{"https://www.goodreads.com/review/list_rss/12345?shelf=currently-reading", "", "12345/currently-reading"},
		{"12345", "favorites", "12345/favorites"},
	}
	for _, c := range cases {
		got, err := ParseGoodreadsRef(c.input, c.shelf)
		if err != nil {
			t.Fatalf("%q: %v", c.input, err)
		}
		if got != c.want {
			t.Fatalf("%q: want %q got %q", c.input, c.want, got)
		}
	}
	if _, err := ParseGoodreadsRef("not a url at all", ""); err == nil {
		t.Fatal("expected an error for an unparseable ref")
	}
}

// shelfServer serves a mutable RSS feed with ETag support.
type shelfServer struct {
	items string
	etag  string
	hits  int
}

func (s *shelfServer) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.hits++
		if s.etag != "" && r.Header.Get("If-None-Match") == s.etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		if s.etag != "" {
			w.Header().Set("ETag", s.etag)
		}
		_, _ = fmt.Fprintf(w, shelfRSS, s.items)
	}
}

func TestPollListAddsAndDiffs(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	shelf := &shelfServer{items: feedItem("101", "First Ember", "Mara Voss", "9781650000013"), etag: `"v1"`}
	ts := httptest.NewServer(shelf.handler())
	defer ts.Close()
	w.goodreads.BaseURL = ts.URL

	listID, err := w.CreateList(WatchedList{
		Name: "Alex to-read", Kind: "goodreads_rss", SourceRef: "12345/to-read",
		LibraryID: libID, MonitorScope: "book", OnRemove: "unmonitor", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	added, err := w.PollList(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("first poll: want 1 added, got %d", added)
	}
	books, _ := cat.ListBooks(0, libID, 0)
	if len(books) != 1 || books[0].Title != "First Ember" || !books[0].Monitored {
		t.Fatalf("book not routed into library: %+v", books)
	}

	// second poll with unchanged ETag: 304, nothing re-added
	added, err = w.PollList(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("etag poll: want 0 added, got %d", added)
	}

	// new item appears, old one leaves → add + on_remove=unmonitor
	shelf.items = feedItem("202", "Second Ember", "Mara Voss", "9781650000020")
	shelf.etag = `"v2"`
	added, err = w.PollList(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("third poll: want 1 added, got %d", added)
	}
	books, _ = cat.ListBooks(0, libID, 0)
	byTitle := map[string]bool{}
	for _, b := range books {
		byTitle[b.Title] = b.Monitored
	}
	if mon, ok := byTitle["First Ember"]; !ok || mon {
		t.Fatalf("First Ember should remain in library but unmonitored: %v", byTitle)
	}
	if mon := byTitle["Second Ember"]; !mon {
		t.Fatalf("Second Ember should be monitored: %v", byTitle)
	}

	list, err := w.GetList(listID)
	if err != nil {
		t.Fatal(err)
	}
	if list.LastError != "" || list.LastChecked == "" || list.ItemCount != 1 {
		t.Fatalf("list state after polls: %+v", list)
	}
}

func TestPollListSeriesScopeAndFeedError(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	shelf := &shelfServer{items: feedItem("303", "First Ember (The Ember Cycle, #1)", "Mara Voss", "")}
	ts := httptest.NewServer(shelf.handler())
	defer ts.Close()
	w.goodreads.BaseURL = ts.URL

	// pre-create the book with its series so scope=series has one to monitor
	bookID, err := cat.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "First Ember", Authors: []string{"Mara Voss"},
		GoodreadsID: "303", SeriesName: "The Ember Cycle", SeriesIndex: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	listID, err := w.CreateList(WatchedList{
		Name: "series scope", Kind: "goodreads_rss", SourceRef: "12345/to-read",
		LibraryID: libID, MonitorScope: "series", Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.PollList(context.Background(), listID); err != nil {
		t.Fatal(err)
	}
	series, _ := cat.ListSeries(nil)
	if len(series) != 1 || !series[0].Monitored {
		t.Fatalf("series should be monitored: %+v", series)
	}
	books, _ := cat.ListBooks(0, libID, 0)
	if len(books) != 1 || books[0].ID != bookID {
		t.Fatalf("existing book should have been matched, not duplicated: %+v", books)
	}

	// a broken feed records the error on the list and returns it
	ts.Close()
	if _, err := w.PollList(context.Background(), listID); err == nil {
		t.Fatal("expected poll error after server close")
	}
	list, _ := w.GetList(listID)
	if list.LastError == "" {
		t.Fatal("poll failure should be recorded in last_error")
	}
}

func TestReleaseDayTaper(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	today := time.Now().UTC().Format("2006-01-02")
	lastWeek := time.Now().UTC().AddDate(0, 0, -7).Format("2006-01-02")
	offTaper := time.Now().UTC().AddDate(0, 0, -5).Format("2006-01-02")

	add := func(title, date string) int64 {
		id, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: title, Authors: []string{"A"}, ReleaseDate: date})
		if err != nil {
			t.Fatal(err)
		}
		if err := cat.AddToLibrary(id, libID, true); err != nil {
			t.Fatal(err)
		}
		return id
	}
	releasedToday := add("Released Today", today)
	taperDay7 := add("Taper Day 7", lastWeek)
	betweenTaper := add("Between Taper Days", offTaper)

	w.releaseDayPass(context.Background())

	attempted := func(id int64) bool { return w.attemptedToday(id) }
	if !attempted(releasedToday) || !attempted(taperDay7) {
		t.Fatal("day-0 and day-7 books should have been searched")
	}
	if attempted(betweenTaper) {
		t.Fatal("day-5 is off the taper — no search")
	}

	// a second pass the same day must not re-search
	w.releaseDayPass(context.Background())
	var n int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM search_attempts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("want 2 attempt rows, got %d", n)
	}
}

func TestBacklogPassRespectsOptIn(t *testing.T) {
	w, cat, cfg, libID := testWatcher(t)
	id, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Missing Book", Authors: []string{"A"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(id, libID, true); err != nil {
		t.Fatal(err)
	}

	// disabled (default): tick never runs the backlog
	w.maybeBacklog(context.Background())
	if w.attemptedToday(id) {
		t.Fatal("backlog ran while disabled")
	}

	if err := cfg.Set("backlog_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	w.maybeBacklog(context.Background())
	if !w.attemptedToday(id) {
		t.Fatal("backlog should search missing monitored books once enabled")
	}
}

func TestUserBlockedBookNotReAdded(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	shelf := &shelfServer{items: feedItem("404", "Blocked Book", "A", "")}
	ts := httptest.NewServer(shelf.handler())
	defer ts.Close()
	w.goodreads.BaseURL = ts.URL

	bookID, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Blocked Book", Authors: []string{"A"}, GoodreadsID: "404"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(bookID, libID, true); err != nil {
		t.Fatal(err)
	}
	if err := cat.RemoveBookFromLibrary(bookID, libID, "block"); err != nil {
		t.Fatal(err)
	}

	listID, err := w.CreateList(WatchedList{
		Name: "l", Kind: "goodreads_rss", SourceRef: "1/to-read", LibraryID: libID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	added, err := w.PollList(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	if added != 0 {
		t.Fatalf("blocked book must not be re-added, got %d", added)
	}
	books, _ := cat.ListBooks(0, libID, 0)
	if len(books) != 0 {
		t.Fatalf("library should stay empty: %+v", books)
	}
}

func TestFeedEntriesRespectExclusions(t *testing.T) {
	w, cat, cfg, libID := testWatcher(t)
	// the exclusion list replaces the defaults (they're removable), so this
	// list carries "box set" itself to keep the box-set entry filtered
	if err := cfg.Set("exclude_patterns", "large print\nbox set"); err != nil {
		t.Fatal(err)
	}
	shelf := &shelfServer{items: feedItem("1", "First Ember", "Mara Voss", "") +
		feedItem("2", "The Ember Cycle Box Set 1-3", "Mara Voss", "") +
		feedItem("3", "First Ember Large Print", "Mara Voss", "")}
	ts := httptest.NewServer(shelf.handler())
	defer ts.Close()
	w.goodreads.BaseURL = ts.URL

	listID, err := w.CreateList(WatchedList{
		Name: "l", Kind: "goodreads_rss", SourceRef: "1/to-read", LibraryID: libID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	added, err := w.PollList(context.Background(), listID)
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Fatalf("want 1 routed, got %d", added)
	}
	books, _ := cat.ListBooks(0, libID, 0)
	if len(books) != 1 || books[0].Title != "First Ember" {
		t.Fatalf("filtered titles leaked into the library: %+v", books)
	}

	// filtered entries are remembered, so the next poll doesn't retry them
	added, err = w.PollList(context.Background(), listID)
	if err != nil || added != 0 {
		t.Fatalf("second poll should add nothing: %d %v", added, err)
	}
}

// worksChain builds a chain whose only provider can list author works.
type worksStub struct {
	works []metadata.BookMeta
}

func (s *worksStub) Key() string { return "stub" }
func (s *worksStub) Search(ctx context.Context, p metadata.SearchParams) ([]metadata.BookMeta, error) {
	return nil, nil
}
func (s *worksStub) AuthorWorks(ctx context.Context, name string, limit int) ([]metadata.BookMeta, error) {
	return s.works, nil
}

func TestSyncBibliographies(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	w.chain = metadata.NewChain(func() []string { return []string{"stub"} }, &worksStub{
		works: []metadata.BookMeta{
			{Provider: "stub", Title: "Loom", Authors: []string{"Tess Arden"}},
			{Provider: "stub", Title: "Drift", Authors: []string{"Tess Arden"}},
			{Provider: "stub", Title: "Vault Box Set 1-3", Authors: []string{"Tess Arden"}},
		},
	})

	// user adds one book → author exists, unsynced
	bookID, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Loom", Authors: []string{"Tess Arden"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(bookID, libID, true); err != nil {
		t.Fatal(err)
	}

	// one tick of the background sync fills in the bibliography, catalog-only
	w.syncBibliographies(context.Background())

	all, _ := cat.ListBooks(1, 0, 0)
	titles := map[string]int64{}
	for _, b := range all {
		titles[b.Title] = b.LibraryID
	}
	if len(all) != 2 || titles["Drift"] != 0 {
		t.Fatalf("bibliography should arrive catalog-only: %+v", all)
	}
	if _, ok := titles["Vault Box Set 1-3"]; ok {
		t.Fatal("box set leaked through the background sync")
	}
	libBooks, _ := cat.ListBooks(0, libID, 0)
	if len(libBooks) != 1 || libBooks[0].Title != "Loom" {
		t.Fatalf("library must stay curated: %+v", libBooks)
	}

	// the author is stamped: the next tick has nobody due and changes nothing
	var stamp string
	if err := w.db.QueryRow(`SELECT COALESCE(works_synced_at, '') FROM authors WHERE id = 1`).Scan(&stamp); err != nil || stamp == "" {
		t.Fatalf("works_synced_at not stamped: %q %v", stamp, err)
	}
	w.syncBibliographies(context.Background())
	all2, _ := cat.ListBooks(1, 0, 0)
	if len(all2) != len(all) {
		t.Fatalf("second tick must be a no-op, got %d books", len(all2))
	}
}

// TestExpandPrunesStaleCatalogBooks: a fresh sync is authoritative for
// catalog-only rows — leftovers from before the language/dedupe filters
// (foreign-language records, edition variants) disappear, while library
// books, user-blocked books and user-edited rows always survive.
func TestExpandPrunesStaleCatalogBooks(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	w.chain = metadata.NewChain(func() []string { return []string{"stub"} }, &worksStub{
		works: []metadata.BookMeta{{Provider: "stub", Title: "Loom", Authors: []string{"Tess Arden"}}},
	})

	// a curated library book the sync no longer returns — must survive
	libBook, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Drift", Authors: []string{"Tess Arden"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(libBook, libID, true); err != nil {
		t.Fatal(err)
	}
	authorID := int64(1)
	// a stale catalog-only leftover (e.g. a foreign-language record synced
	// before the filters existed) — must be pruned
	stale, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: "El registro final", Authors: []string{"Tess Arden"}})
	if err != nil {
		t.Fatal(err)
	}
	// a user-edited catalog-only row — must survive
	edited, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Pylon 12", Authors: []string{"Tess Arden"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.db.Exec(`UPDATE books SET field_locks = '{"title":true}' WHERE id = ?`, edited); err != nil {
		t.Fatal(err)
	}
	// a delete-and-blocked book — row and block must survive so the block works
	blocked, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Halfway to Nowhere", Authors: []string{"Tess Arden"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(blocked, libID, true); err != nil {
		t.Fatal(err)
	}
	if err := cat.RemoveBookFromLibrary(blocked, libID, "block"); err != nil {
		t.Fatal(err)
	}

	if _, err := w.ExpandAuthor(context.Background(), authorID, Attach{}); err != nil {
		t.Fatal(err)
	}

	if _, err := cat.GetBook(stale); err == nil {
		t.Error("stale catalog-only book should be pruned")
	}
	for name, id := range map[string]int64{"library": libBook, "edited": edited, "blocked": blocked} {
		if _, err := cat.GetBook(id); err != nil {
			t.Errorf("%s book must survive the prune: %v", name, err)
		}
	}
	if !w.userBlocked(blocked) {
		t.Error("user block must outlive the prune")
	}

	// second sync with an unchanged bibliography is a no-op
	var total int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if _, err := w.ExpandAuthor(context.Background(), authorID, Attach{}); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM books`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != total {
		t.Fatalf("re-sync changed the catalog: %d -> %d", total, after)
	}
}

// TestExpandNeverMintsAuthors: bibliography results crediting co-authors or
// name variants must attach to the expanded author — never create new author
// rows (that once cascaded into an author explosion on library scans).
func TestExpandNeverMintsAuthors(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	w.chain = metadata.NewChain(func() []string { return []string{"stub"} }, &worksStub{
		works: []metadata.BookMeta{
			{Provider: "stub", Title: "Loom", Authors: []string{"Tess Arden"}},
			{Provider: "stub", Title: "Silt", Authors: []string{"Tess  Arden"}},                  // spacing variant
			{Provider: "stub", Title: "Co-Written Thing", Authors: []string{"Somebody Else"}},    // not his — dropped
			{Provider: "stub", Title: "Joint Novella", Authors: []string{"Other", "Tess Arden"}}, // co-credit — kept, pinned
		},
	})
	bookID, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Loom", Authors: []string{"Tess Arden"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(bookID, libID, true); err != nil {
		t.Fatal(err)
	}

	w.syncBibliographies(context.Background())

	var authorCount int
	if err := w.db.QueryRow(`SELECT COUNT(*) FROM authors`).Scan(&authorCount); err != nil {
		t.Fatal(err)
	}
	if authorCount != 1 {
		t.Fatalf("expand minted extra authors: %d rows", authorCount)
	}
	books, _ := cat.ListBooks(1, 0, 0)
	titles := map[string]bool{}
	for _, b := range books {
		titles[b.Title] = true
	}
	if !titles["Silt"] || !titles["Joint Novella"] || titles["Co-Written Thing"] {
		t.Fatalf("wrong works attributed: %v", titles)
	}
}

// TestSyncSkipsStrayAuthors: authors with no library presence and no
// monitored flag never trigger provider traffic, and stay off the Authors
// page.
func TestSyncSkipsStrayAuthors(t *testing.T) {
	w, cat, _, _ := testWatcher(t)
	w.chain = metadata.NewChain(func() []string { return []string{"stub"} }, &worksStub{
		works: []metadata.BookMeta{{Provider: "stub", Title: "Junk", Authors: []string{"Stray Author"}}},
	})
	// a stray author: exists, unmonitored, no books anywhere
	if _, err := cat.EnsureAuthor("Stray Author"); err != nil {
		t.Fatal(err)
	}

	w.syncBibliographies(context.Background())

	var stamp string
	if err := w.db.QueryRow(`SELECT COALESCE(works_synced_at, '') FROM authors WHERE name = 'Stray Author'`).Scan(&stamp); err != nil {
		t.Fatal(err)
	}
	if stamp != "" {
		t.Fatal("stray author must not be synced")
	}
	authors, err := cat.ListAuthors(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 0 {
		t.Fatalf("stray author must be hidden from the Authors page: %+v", authors)
	}
}

// TestAuthorMonitorModes: mode "new" leaves the backlist catalog-only and
// auto-monitors only books discovered after the first sync; mode "all"
// pulls the whole bibliography into the library monitored.
// A bibliography sync leaves everything it finds catalog-only. There used to
// be a per-author mode that could attach new books to a library, which meant
// guessing WHICH library — the guess that could drop books onto a shelf its
// owner never asked for. Books now wait on the author page until somebody
// shelves them.
func TestBibliographySyncNeverShelves(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	stub := &worksStub{works: []metadata.BookMeta{
		{Provider: "stub", Title: "Loom", Authors: []string{"Tess Arden"}},
		{Provider: "stub", Title: "Drift", Authors: []string{"Tess Arden"}},
	}}
	w.chain = metadata.NewChain(func() []string { return []string{"stub"} }, stub)

	bookID, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Loom", Authors: []string{"Tess Arden"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(bookID, libID, true); err != nil {
		t.Fatal(err)
	}

	// first sync: the backlist must not sweep into the library
	w.syncBibliographies(context.Background())
	libBooks, _ := cat.ListBooks(0, libID, 0)
	if len(libBooks) != 1 || libBooks[0].Title != "Loom" {
		t.Fatalf("backlist leaked into the library on first sync: %+v", libBooks)
	}

	// a new release appears upstream; it belongs on the author page, not a shelf
	stub.works = append(stub.works, metadata.BookMeta{Provider: "stub", Title: "Haze", Authors: []string{"Tess Arden"}})
	if _, err := w.db.Exec(`UPDATE authors SET works_synced_at = datetime('now', '-8 days') WHERE id = 1`); err != nil {
		t.Fatal(err)
	}
	w.syncBibliographies(context.Background())

	libBooks, _ = cat.ListBooks(0, libID, 0)
	if len(libBooks) != 1 {
		t.Fatalf("a sync shelved books nobody asked for: %+v", libBooks)
	}
	// ...but it IS in the catalog, ready to be added
	all, _ := cat.ListBooks(0, 0, 0)
	found := false
	for _, b := range all {
		if b.Title == "Haze" {
			found = true
			if b.LibraryID != 0 {
				t.Fatalf("new release should be catalog-only, got library %d", b.LibraryID)
			}
		}
	}
	if !found {
		t.Fatal("the sync should still have discovered the new release")
	}
}

// fakeHardcoverServer answers the search + hydration + isbn queries the
// adoption path issues, with one clean canonical book.
func fakeHardcoverServer(t *testing.T, hcID, title, author string, users int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		req := string(body)
		w.Header().Set("Content-Type", "application/json")
		var data string
		switch {
		case strings.Contains(req, "search("):
			data = fmt.Sprintf(`{"search": {"results": {"hits": [{"document": {"id": "%s"}}]}}}`, hcID)
		case strings.Contains(req, "_in"):
			data = fmt.Sprintf(`{"books": [{"id": %s, "title": %q, "users_count": %d,
				"description": "A clean canonical description.",
				"cached_image": {"url": "https://assets.hardcover.app/external_data/%s.jpg"},
				"contributions": [{"author": {"name": %q}}],
				"book_series": [{"position": 1, "series": {"name": "The Silas Calder Series"}}]}]}`,
				hcID, title, users, hcID, author)
		case strings.Contains(req, "editions("):
			data = `{"editions": []}`
		default:
			data = `{}`
		}
		_, _ = fmt.Fprintf(w, `{"data":%s}`, data)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A confident Hardcover match overrides the noisy Goodreads feed title and
// cover — Hardcover becomes canonical while the Goodreads id stays for
// identity.
func TestPollListAdoptsHardcoverCanonical(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	shelf := &shelfServer{items: feedItem("555", "The Wolves of Calder (The Silas Calder Series Book 1)", "D.K. Vale", "")}
	ts := httptest.NewServer(shelf.handler())
	defer ts.Close()
	w.goodreads.BaseURL = ts.URL

	hcSrv := fakeHardcoverServer(t, "42", "The Wolves of Calder", "D.K. Vale", 120)
	w.hardcover = metadata.NewHardcover(func() string { return "test-token" })
	w.hardcover.BaseURL = hcSrv.URL

	listID, err := w.CreateList(WatchedList{
		Name: "l", Kind: "goodreads_rss", SourceRef: "1/to-read", LibraryID: libID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.PollList(context.Background(), listID); err != nil {
		t.Fatal(err)
	}
	books, _ := cat.ListBooks(0, libID, 0)
	if len(books) != 1 {
		t.Fatalf("want 1 book, got %+v", books)
	}
	b := books[0]
	if b.Title != "The Wolves of Calder" {
		t.Errorf("title not adopted from hardcover: %q", b.Title)
	}
	if b.HardcoverID != "42" || b.GoodreadsID != "555" {
		t.Errorf("identity wrong: hc=%q gr=%q", b.HardcoverID, b.GoodreadsID)
	}
	if b.SeriesName != "The Silas Calder Series" {
		t.Errorf("series not adopted: %q", b.SeriesName)
	}
	if b.Description == "" {
		t.Error("description not adopted")
	}
}

// Without a confident match (author disagrees), the Goodreads seed must land
// unchanged — never adopt the wrong book.
func TestPollListKeepsSeedWithoutConfidentMatch(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	shelf := &shelfServer{items: feedItem("556", "The Wolves of Calder (The Silas Calder Series Book 1)", "D.K. Vale", "")}
	ts := httptest.NewServer(shelf.handler())
	defer ts.Close()
	w.goodreads.BaseURL = ts.URL

	hcSrv := fakeHardcoverServer(t, "43", "The Wolves of Calder", "Somebody Else", 9000)
	w.hardcover = metadata.NewHardcover(func() string { return "test-token" })
	w.hardcover.BaseURL = hcSrv.URL

	listID, err := w.CreateList(WatchedList{
		Name: "l", Kind: "goodreads_rss", SourceRef: "1/to-read", LibraryID: libID, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.PollList(context.Background(), listID); err != nil {
		t.Fatal(err)
	}
	books, _ := cat.ListBooks(0, libID, 0)
	if len(books) != 1 {
		t.Fatalf("want 1 book, got %+v", books)
	}
	if books[0].Title != "The Wolves of Calder (The Silas Calder Series Book 1)" {
		t.Errorf("seed title should survive an unconfident match: %q", books[0].Title)
	}
	if books[0].HardcoverID != "" {
		t.Errorf("wrong-author candidate must not be adopted: hc=%q", books[0].HardcoverID)
	}
}

// A list mentioning a book the library already holds must not re-add it: no
// list_add history and, above all, no search — a "read" shelf full of owned
// books must never re-download the shelf.
func TestListSkipsBooksAlreadyInLibrary(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	bookID, err := cat.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "First Ember", Authors: []string{"Mara Voss"}, ISBN13: "9781650000013",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(bookID, libID, false); err != nil {
		t.Fatal(err)
	}
	if err := cat.SetFile(libID, bookID, "/data/lib/first-ember.epub", "epub", 42); err != nil {
		t.Fatal(err)
	}

	shelf := &shelfServer{items: feedItem("101", "First Ember", "Mara Voss", "9781650000013"), etag: `"v1"`}
	ts := httptest.NewServer(shelf.handler())
	defer ts.Close()
	w.goodreads.BaseURL = ts.URL
	listID, err := w.CreateList(WatchedList{
		Name: "Alex read", Kind: "goodreads_rss", SourceRef: "12345/read",
		LibraryID: libID, MonitorScope: "book", OnRemove: "nothing", SearchOnAdd: true, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.PollList(context.Background(), listID); err != nil {
		t.Fatal(err)
	}

	books, _ := cat.ListBooks(0, libID, 0)
	if len(books) != 1 {
		t.Fatalf("shelved book duplicated by list: %d rows", len(books))
	}
	if books[0].FilePath == "" {
		t.Fatal("membership lost its file")
	}
	hist, err := cat.ListHistory(50, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hist {
		if h.Kind == "list_add" && h.BookID == bookID {
			t.Fatalf("already-shelved book recorded as a list add: %+v", h)
		}
	}
}

// failingLister is a metadata provider whose bibliography always errors —
// the shape of a rate-limited or flaky Hardcover.
type failingLister struct{}

func (failingLister) Key() string { return "flaky" }
func (failingLister) Search(ctx context.Context, p metadata.SearchParams) ([]metadata.BookMeta, error) {
	return nil, nil
}
func (failingLister) AuthorWorks(ctx context.Context, name string, limit int) ([]metadata.BookMeta, error) {
	return nil, fmt.Errorf("429 too many requests")
}

// A failed bibliography sync must NOT permanently stamp the author as synced:
// stamp-on-error disqualified unmonitored (scanned-in) authors forever, so
// their pages stayed empty until a manual refresh. Failures back off and
// retry instead.
func TestBibliographySyncRetriesAfterProviderError(t *testing.T) {
	w, cat, _, libID := testWatcher(t)
	w.chain = metadata.NewChain(func() []string { return []string{"flaky"} }, failingLister{})
	bookID, err := cat.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "Loom", Authors: []string{"Tess Arden"},
	})
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

	w.syncBibliographies(context.Background())

	var stamped sql.NullString
	if err := w.db.QueryRow(`SELECT works_synced_at FROM authors WHERE id = ?`, b.AuthorID).Scan(&stamped); err != nil {
		t.Fatal(err)
	}
	if stamped.Valid {
		t.Fatalf("failed sync stamped the author as synced (%q) — it would never retry", stamped.String)
	}
	until, ok := w.syncFail[b.AuthorID]
	if !ok || time.Until(until) <= 0 {
		t.Fatalf("author not backing off after failure: %v %v", until, ok)
	}
	// while backing off, ticks skip it without touching the stamp
	w.syncBibliographies(context.Background())
	if err := w.db.QueryRow(`SELECT works_synced_at FROM authors WHERE id = ?`, b.AuthorID).Scan(&stamped); err != nil || stamped.Valid {
		t.Fatalf("backoff tick altered sync state: %v %v", stamped, err)
	}
}
