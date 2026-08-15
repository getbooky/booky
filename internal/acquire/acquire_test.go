package acquire

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/importer"
	"github.com/getbooky/booky/internal/metadata"
	"github.com/getbooky/booky/internal/release"
	"github.com/getbooky/booky/internal/settings"
)

func testEngine(t *testing.T) (*Engine, *catalog.Store, *settings.Store, int64, int64) {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	cat := catalog.New(conn)
	cfg := settings.New(conn)
	profile, _ := cat.EnsureDefaultProfile()
	libID, err := cat.CreateLibrary("Alex", t.TempDir(), profile, "alex", "x")
	if err != nil {
		t.Fatal(err)
	}
	bookID, err := cat.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "First Ember", Authors: []string{"Mara Voss"}, ISBN13: "9781649374042",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := cat.AddToLibrary(bookID, libID, true); err != nil {
		t.Fatal(err)
	}
	chain := metadata.NewChain(func() []string { return nil })
	imp := importer.New(conn, cat, chain)
	covers := catalog.NewCoverCache(t.TempDir())
	return New(conn, cat, cfg, imp, covers), cat, cfg, bookID, libID
}

// fakeAnnas stands in for an Anna's Archive mirror + member API + file host.
func fakeAnnas(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<a href="/md5/0123456789abcdef0123456789abcdef"><h3>First Ember Retail EPUB</h3></a>`))
	})
	mux.HandleFunc("/dyn/api/fast_download.json", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "member-secret" {
			_, _ = w.Write([]byte(`{"error":"invalid key"}`))
			return
		}
		_, _ = w.Write([]byte(`{"download_url":"` + srv.URL + `/file/first-ember.epub"}`))
	})
	mux.HandleFunc("/file/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("epub-bytes"))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestSearchRanksAcrossSources(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	src := fakeAnnas(t)
	for _, kv := range [][2]string{{"annas_mirrors", src.URL}, {"annas_key", "member-secret"}} {
		if err := cfg.Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	rels, err := e.Search(context.Background(), bookID, libID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 || rels[0].Source != "annas" || rels[0].Format != "epub" {
		t.Fatalf("expected one annas epub, got %+v", rels)
	}
}

func TestGrabDirectDownloadsAndQueues(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	src := fakeAnnas(t)
	dl := t.TempDir()
	for _, kv := range [][2]string{
		{"annas_mirrors", src.URL}, {"annas_key", "member-secret"}, {"downloads_dir", dl},
	} {
		if err := cfg.Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	rel := release.Release{
		Title: "First Ember Retail EPUB", Source: "annas", Protocol: "direct",
		Format: "epub", DownloadURL: "md5:0123456789abcdef0123456789abcdef",
	}
	qid, err := e.Grab(context.Background(), bookID, libID, rel)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}

	items, err := e.Queue(nil)
	if err != nil || len(items) != 1 {
		t.Fatalf("Queue: %v %+v", err, items)
	}
	it := items[0]
	if it.ID != qid || it.Status != "importing" || it.ExternalID == "" {
		t.Fatalf("queue item = %+v", it)
	}
	body, err := os.ReadFile(it.ExternalID)
	if err != nil || string(body) != "epub-bytes" {
		t.Fatalf("downloaded file: %v %q", err, body)
	}

	pending, _ := e.PendingImports()
	if len(pending) != 1 {
		t.Fatalf("PendingImports = %+v", pending)
	}
	e.MarkDone(qid)
	pending, _ = e.PendingImports()
	if len(pending) != 0 {
		t.Fatalf("MarkDone should clear pending: %+v", pending)
	}
	// and the row itself is gone — an imported book has no business sitting in
	// the queue; history is where it lives on
	items, _ = e.Queue(nil)
	if len(items) != 0 {
		t.Fatalf("MarkDone should clear the queue row: %+v", items)
	}
}

func TestFailedGrabBlocklistsRelease(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	// mirror refuses the connection → resolve fails → blocklisted
	if err := cfg.Set("annas_mirrors", "http://127.0.0.1:1"); err != nil {
		t.Fatal(err)
	}
	rel := release.Release{
		Title: "First Ember Bad Copy EPUB", Source: "annas", Protocol: "direct",
		DownloadURL: "md5:ffff456789abcdef0123456789abcdef",
	}
	if _, err := e.Grab(context.Background(), bookID, libID, rel); err == nil {
		t.Fatal("expected grab failure")
	}

	kept, err := e.dropBlocked(bookID, []release.Release{rel, {Title: "Other EPUB"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 1 || kept[0].Title != "Other EPUB" {
		t.Fatalf("blocklist should filter the failed release: %+v", kept)
	}
}

func TestGrabUsenetWithoutSabFails(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	rel := release.Release{Title: "First Ember EPUB", Source: "prowlarr:ndx", Protocol: "usenet", DownloadURL: "http://x/nzb"}
	if _, err := e.Grab(context.Background(), bookID, libID, rel); err == nil {
		t.Fatal("expected failure without SAB configured")
	}
	items, _ := e.Queue(nil)
	if len(items) != 1 || items[0].Status != "failed" {
		t.Fatalf("queue should record the failure: %+v", items)
	}
}

// A completed SAB job is imported into the library AND removed from SAB
// (history entry + leftover files) so finished downloads never pile up.
func TestPollSabImportsAndCleansUp(t *testing.T) {
	e, cat, cfg, bookID, libID := testEngine(t)
	storage := t.TempDir()
	if err := os.WriteFile(filepath.Join(storage, "First Ember.epub"), []byte("epub-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case q.Get("mode") == "addurl":
			fmt.Fprint(w, `{"status":true,"nzo_ids":["nzo_1"]}`)
		case q.Get("mode") == "history" && q.Get("name") == "delete":
			if q.Get("value") != "nzo_1" || q.Get("del_files") != "1" {
				t.Errorf("delete params = %v, want nzo_1 with del_files=1", q)
			}
			deleted = true
			fmt.Fprint(w, `{"status":true}`)
		case q.Get("mode") == "history":
			_, _ = fmt.Fprintf(w, `{"history":{"slots":[{"nzo_id":"nzo_1","name":"First Ember","status":"Completed","storage":%q}]}}`, storage)
		default:
			t.Errorf("unexpected sab mode %q", q.Get("mode"))
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(srv.Close)
	for _, kv := range [][2]string{{"sab_url", srv.URL}, {"sab_api_key", "k"}} {
		if err := cfg.Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	rel := release.Release{Title: "First Ember NZB", Source: "prowlarr", Protocol: "usenet", Format: "epub", DownloadURL: "http://indexer/nzb"}
	if _, err := e.Grab(context.Background(), bookID, libID, rel); err != nil {
		t.Fatalf("Grab: %v", err)
	}
	e.PollSab(context.Background())

	// a completed import leaves the queue entirely
	items, err := e.Queue(nil)
	if err != nil || len(items) != 0 {
		t.Fatalf("queue after poll = %v %+v", err, items)
	}
	b, err := cat.GetBook(bookID)
	if err != nil || b.FilePath == "" {
		t.Fatalf("book has no file after import: %v %+v", err, b)
	}
	if !deleted {
		t.Error("completed SAB job was not deleted after import")
	}
}

// A verification wall on the free partners is transient — it must fail the
// grab WITHOUT blocklisting the release, or every candidate tried during a
// gated window stays poisoned after the wall lifts.
func TestGatedSlowPathDoesNotBlocklist(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	wall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = fmt.Fprint(w, `<html><body>Checking your browser before accessing</body></html>`)
	}))
	t.Cleanup(wall.Close)
	if err := cfg.Set("annas_mirrors", wall.URL); err != nil {
		t.Fatal(err)
	}

	rel := release.Release{
		Title: "First Ember Retail EPUB", Source: "annas", Protocol: "direct",
		Format: "epub", DownloadURL: "md5:0123456789abcdef0123456789abcdef",
	}
	if _, err := e.Grab(context.Background(), bookID, libID, rel); err == nil {
		t.Fatal("gated grab should fail")
	}

	var blocked int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM blocklist`).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if blocked != 0 {
		t.Errorf("transient wall failure blocklisted the release (%d rows)", blocked)
	}
	items, _ := e.Queue(nil)
	if len(items) != 1 || items[0].Status != "failed" {
		t.Fatalf("queue = %+v, want one failed row", items)
	}
	if !strings.Contains(items[0].Detail, "verification wall") {
		t.Errorf("failure detail should explain the wall: %q", items[0].Detail)
	}
}

// A disabled source is skipped even when fully configured, and the stats say
// so instead of pretending it wasn't set up.
func TestSourceToggleExcludesConfiguredSource(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	src := fakeAnnas(t)
	for _, kv := range [][2]string{
		{"annas_mirrors", src.URL}, {"annas_key", "member-secret"}, {"annas_enabled", "false"},
	} {
		if err := cfg.Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	rels, stats, err := e.SearchDetailed(context.Background(), bookID, libID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 0 {
		t.Fatalf("disabled source still returned releases: %+v", rels)
	}
	for _, st := range stats {
		if st.Name == "annas" && (!st.Disabled || !st.Configured) {
			t.Fatalf("annas stat should be configured+disabled: %+v", st)
		}
	}

	if err := cfg.Set("annas_enabled", "true"); err != nil {
		t.Fatal(err)
	}
	rels, _, err = e.SearchDetailed(context.Background(), bookID, libID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 1 {
		t.Fatalf("re-enabled source should search again: %+v", rels)
	}
}

// A download that dies inside SAB blocklists the release and automatically
// grabs the next-best candidate — the cascade the wanted list depends on.
func TestPollSabFailureCascadesToNextCandidate(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	next := fakeAnnas(t) // the fallback candidate AutoGrab should reach for
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("mode") {
		case "addurl":
			fmt.Fprint(w, `{"status":true,"nzo_ids":["nzo_9"]}`)
		case "history":
			fmt.Fprint(w, `{"history":{"slots":[{"nzo_id":"nzo_9","name":"First Ember","status":"Failed","fail_message":"article not found"}]}}`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(sab.Close)
	for _, kv := range [][2]string{
		{"sab_url", sab.URL}, {"sab_api_key", "k"},
		{"annas_mirrors", next.URL}, {"annas_key", "member-secret"}, {"downloads_dir", t.TempDir()},
	} {
		if err := cfg.Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	rel := release.Release{Title: "First Ember NZB", Source: "prowlarr", Protocol: "usenet", Format: "epub", DownloadURL: "http://indexer/nzb"}
	if _, err := e.Grab(context.Background(), bookID, libID, rel); err != nil {
		t.Fatalf("Grab: %v", err)
	}
	e.PollSab(context.Background())

	items, err := e.Queue(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("cascade should add a second grab: %+v", items)
	}
	// Queue() returns newest first
	if items[0].Source != "annas" || items[0].Status != "importing" {
		t.Fatalf("next candidate not grabbed: %+v", items[0])
	}
	if items[1].Status != "failed" {
		t.Fatalf("original grab should be failed: %+v", items[1])
	}
	kept, err := e.dropBlocked(bookID, []release.Release{rel})
	if err != nil {
		t.Fatal(err)
	}
	if len(kept) != 0 {
		t.Fatal("failed SAB release should be blocklisted")
	}
}

// A failed IMPORT (file arrived, delivery broke) surfaces its reason in the
// history feed — and does NOT blocklist or cascade, unlike a failed download.
func TestFailedImportRecordsHistory(t *testing.T) {
	e, _, cfg, bookID, libID := testEngine(t)
	storage := t.TempDir()
	if err := os.WriteFile(filepath.Join(storage, "First Ember.epub"), []byte("epub-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	// break delivery: the library root becomes a regular file, so MkdirAll
	// inside Deliver fails with ENOTDIR (even as root)
	var root string
	if err := e.db.QueryRow(`SELECT root_path FROM libraries WHERE id = ?`, libID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Query().Get("mode") {
		case "addurl":
			fmt.Fprint(w, `{"status":true,"nzo_ids":["nzo_2"]}`)
		case "history":
			_, _ = fmt.Fprintf(w, `{"history":{"slots":[{"nzo_id":"nzo_2","name":"First Ember","status":"Completed","storage":%q}]}}`, storage)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	t.Cleanup(sab.Close)
	for _, kv := range [][2]string{{"sab_url", sab.URL}, {"sab_api_key", "k"}} {
		if err := cfg.Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	rel := release.Release{Title: "First Ember NZB", Source: "prowlarr", Protocol: "usenet", Format: "epub", DownloadURL: "http://indexer/nzb"}
	if _, err := e.Grab(context.Background(), bookID, libID, rel); err != nil {
		t.Fatalf("Grab: %v", err)
	}
	e.PollSab(context.Background())

	items, _ := e.Queue(nil)
	if len(items) != 1 || items[0].Status != "import_failed" || items[0].Detail == "" {
		t.Fatalf("queue should hold one import_failed row with a reason: %+v", items)
	}
	var kind, detail string
	err := e.db.QueryRow(`SELECT kind, detail FROM history WHERE kind = 'import failed'`).Scan(&kind, &detail)
	if err != nil {
		t.Fatalf("no 'import failed' history row: %v", err)
	}
	if !strings.Contains(detail, "First Ember NZB") {
		t.Errorf("history detail should name the release: %q", detail)
	}
	var blocked int
	if err := e.db.QueryRow(`SELECT COUNT(*) FROM blocklist`).Scan(&blocked); err != nil {
		t.Fatal(err)
	}
	if blocked != 0 {
		t.Error("import failure must not blocklist the release")
	}
}

// RetryImport delivers the already-downloaded file again once the user has
// fixed the local problem — no re-download, no new grab.
func TestRetryImportDeliversWithoutRedownload(t *testing.T) {
	e, cat, cfg, bookID, libID := testEngine(t)
	src := fakeAnnas(t)
	dl := t.TempDir()
	for _, kv := range [][2]string{
		{"annas_mirrors", src.URL}, {"annas_key", "member-secret"}, {"downloads_dir", dl},
	} {
		if err := cfg.Set(kv[0], kv[1]); err != nil {
			t.Fatal(err)
		}
	}

	// break the library root so the first delivery fails
	var root string
	if err := e.db.QueryRow(`SELECT root_path FROM libraries WHERE id = ?`, libID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root, []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}

	rel := release.Release{
		Title: "First Ember Retail EPUB", Source: "annas", Protocol: "direct",
		Format: "epub", DownloadURL: "md5:0123456789abcdef0123456789abcdef",
	}
	qid, err := e.Grab(context.Background(), bookID, libID, rel)
	if err != nil {
		t.Fatalf("Grab: %v", err)
	}
	e.DeliverPending()
	items, _ := e.Queue(nil)
	if len(items) != 1 || items[0].Status != "import_failed" {
		t.Fatalf("first import should fail: %+v", items)
	}

	// fix the library root, then retry
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.RetryImport(context.Background(), qid); err != nil {
		t.Fatalf("RetryImport: %v", err)
	}
	items, _ = e.Queue(nil)
	if len(items) != 0 {
		t.Fatalf("retry should import and clear the row: %+v", items)
	}
	b, err := cat.GetBook(bookID)
	if err != nil || b.FilePath == "" {
		t.Fatalf("book has no file after retried import: %v %+v", err, b)
	}
}

// A second AutoGrab for a book whose grab is still in flight must be a no-op
// success: two list entries converging on one book (or a monitor toggle
// racing a list poll) previously downloaded the same book twice.
func TestAutoGrabSkipsWhenGrabInFlight(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	// no sources are configured, so without a queue row AutoGrab finds
	// nothing and reports no grab
	grabbed, err := e.AutoGrab(context.Background(), bookID, libID, 0)
	if err != nil || grabbed {
		t.Fatalf("baseline: grabbed=%v err=%v, want false/nil", grabbed, err)
	}
	// an active queue row flips the same call to grabbed=true without any
	// source being consulted — the in-flight grab IS the outcome
	if _, err := e.db.Exec(`INSERT INTO queue (book_id, library_id, release_title, source, protocol, status)
		VALUES (?, ?, 'First Ember EPUB', 'annas', 'direct', 'downloading')`, bookID, libID); err != nil {
		t.Fatal(err)
	}
	grabbed, err = e.AutoGrab(context.Background(), bookID, libID, 0)
	if err != nil || !grabbed {
		t.Fatalf("in-flight guard: grabbed=%v err=%v, want true/nil", grabbed, err)
	}
}
