package acquire

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/getbooky/booky/internal/release"
)

// fakeAudioIndexers stands in for Prowlarr (answering only in the
// audiobook category) and SABnzbd. jobStatus says how each grabbed job
// ends up in SAB's history.
type fakeAudioIndexers struct {
	mu        sync.Mutex
	added     []string // NZB URLs sent to SAB, in order
	deleted   []string // SAB jobs cleared
	jobStatus func(nzo string) string
}

func (f *fakeAudioIndexers) start(t *testing.T, e *Engine) {
	t.Helper()
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("categories"); got != "3030" {
			t.Errorf("audiobook search in category %q, want 3030", got)
		}
		_, _ = w.Write([]byte(`[
			{"title":"Mara Voss - First Ember (Abridged) M4B","size":200000000,"downloadUrl":"http://x/nzb/abridged","indexer":"ndx","protocol":"usenet"},
			{"title":"Mara Voss - First Ember (Unabridged) M4B","size":400000000,"downloadUrl":"http://x/nzb/best","indexer":"ndx","protocol":"usenet"},
			{"title":"Mara Voss - First Ember MP3","size":300000000,"downloadUrl":"http://x/nzb/mp3","indexer":"ndx","protocol":"usenet"},
			{"title":"Mara Voss - First Ember M4B","size":350000000,"downloadUrl":"magnet:x","indexer":"tracker","protocol":"torrent"},
			{"title":"Mara Voss - First Ember EPUB","size":2000000,"downloadUrl":"http://x/nzb/epub","indexer":"ndx","protocol":"usenet"}]`))
	}))
	t.Cleanup(prowlarr.Close)
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case q.Get("mode") == "addurl":
			f.added = append(f.added, q.Get("name"))
			_, _ = fmt.Fprintf(w, `{"status":true,"nzo_ids":["nzo_%d"]}`, len(f.added))
		case q.Get("mode") == "history" && q.Get("name") == "delete":
			f.deleted = append(f.deleted, q.Get("value"))
			_, _ = w.Write([]byte(`{"status":true}`))
		case q.Get("mode") == "history":
			var slots []string
			for i := range f.added {
				nzo := fmt.Sprintf("nzo_%d", i+1)
				slots = append(slots, fmt.Sprintf(`{"nzo_id":%q,"status":%q,"storage":"/downloads/%s","fail_message":"broken"}`,
					nzo, f.jobStatus(nzo), nzo))
			}
			_, _ = fmt.Fprintf(w, `{"history":{"slots":[%s]}}`, joinComma(slots))
		default:
			_, _ = w.Write([]byte(`{}`))
		}
	}))
	t.Cleanup(sab.Close)
	for k, v := range map[string]string{
		"prowlarr_url": prowlarr.URL, "prowlarr_api_key": "k",
		"sab_url": sab.URL, "sab_api_key": "k",
	} {
		if err := e.settings.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
}

func joinComma(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += ","
		}
		out += x
	}
	return out
}

func waitImports(t *testing.T, e *Engine) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := e.waitAudioImports(ctx); err != nil {
		t.Fatal("audiobook import didn't finish")
	}
}

func TestSearchAudioRanksByTheAudiobookProfile(t *testing.T) {
	e, _, _, bookID, _ := testEngine(t)
	f := &fakeAudioIndexers{jobStatus: func(string) string { return "Downloading" }}
	f.start(t, e)
	rels, stats, err := e.SearchAudio(context.Background(), bookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Name != "prowlarr" || stats[0].Count != 5 {
		t.Fatalf("stats = %+v", stats)
	}
	var titles []string
	for _, r := range rels {
		titles = append(titles, r.Title)
	}
	// unabridged M4B first; the abridged reading, torrents and the ebook
	// never make the list
	want := []string{
		"Mara Voss - First Ember (Unabridged) M4B",
		"Mara Voss - First Ember MP3",
	}
	if fmt.Sprint(titles) != fmt.Sprint(want) {
		t.Fatalf("ranked = %q", titles)
	}
}

func TestAudiobookGrabImportsAndCleansUp(t *testing.T) {
	e, cat, _, bookID, libID := testEngine(t)
	f := &fakeAudioIndexers{jobStatus: func(string) string { return "Completed" }}
	f.start(t, e)
	var imported []string
	e.ImportAudio = func(ctx context.Context, id int64, path string) error {
		if id != bookID {
			t.Errorf("imported for book %d", id)
		}
		imported = append(imported, path)
		return nil
	}

	grabbed, err := e.AutoGrabAudio(context.Background(), bookID, libID)
	if err != nil || !grabbed {
		t.Fatalf("AutoGrabAudio = %v, %v", grabbed, err)
	}
	if len(f.added) != 1 || f.added[0] != "http://x/nzb/best" {
		t.Fatalf("sent to SAB: %q", f.added)
	}
	items, _ := e.Queue(nil)
	if len(items) != 1 || items[0].Kind != KindAudiobook {
		t.Fatalf("queue = %+v", items)
	}
	// a second auto-grab while this one is on its way does nothing
	if _, err := e.AutoGrabAudio(context.Background(), bookID, libID); err != nil || len(f.added) != 1 {
		t.Fatalf("second auto-grab sent %q (%v)", f.added, err)
	}
	// ...and doesn't stop the ebook being searched for
	if e.inFlight(bookID, libID, KindEbook) {
		t.Fatal("an audiobook grab counts as an ebook grab")
	}

	e.PollSab(context.Background())
	waitImports(t, e)
	if fmt.Sprint(imported) != "[/downloads/nzo_1]" {
		t.Fatalf("imported %q", imported)
	}
	if items, _ := e.Queue(nil); len(items) != 0 {
		t.Fatalf("queue after import = %+v", items)
	}
	if fmt.Sprint(f.deleted) != "[nzo_1]" {
		t.Fatalf("SAB jobs cleared: %q", f.deleted)
	}
	// the ebook side is untouched
	if b, _ := cat.GetBook(bookID); b.FilePath != "" {
		t.Fatalf("audiobook landed as the ebook: %q", b.FilePath)
	}
}

func TestAudiobookImportFailureKeepsTheJob(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	f := &fakeAudioIndexers{jobStatus: func(string) string { return "Completed" }}
	f.start(t, e)
	e.ImportAudio = func(context.Context, int64, string) error { return errors.New("disk full") }
	if _, err := e.AutoGrabAudio(context.Background(), bookID, libID); err != nil {
		t.Fatal(err)
	}
	e.PollSab(context.Background())
	waitImports(t, e)
	items, _ := e.Queue(nil)
	if len(items) != 1 || items[0].Status != "import_failed" || items[0].Detail != "disk full" {
		t.Fatalf("queue = %+v", items)
	}
	if len(f.deleted) != 0 {
		t.Fatal("a job that failed to import was cleared from SAB")
	}
	// a local failure isn't the release's fault: nothing else is grabbed
	if len(f.added) != 1 {
		t.Fatalf("grabbed again after an import failure: %q", f.added)
	}
}

func TestFailedAudiobookDownloadCascadesToTheNextAudiobook(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	f := &fakeAudioIndexers{jobStatus: func(nzo string) string {
		if nzo == "nzo_1" {
			return "Failed"
		}
		return "Downloading"
	}}
	f.start(t, e)
	e.ImportAudio = func(context.Context, int64, string) error { return nil }
	if _, err := e.AutoGrabAudio(context.Background(), bookID, libID); err != nil {
		t.Fatal(err)
	}
	e.PollSab(context.Background())
	// the next AUDIOBOOK is grabbed — not an ebook
	if len(f.added) != 2 || f.added[1] != "http://x/nzb/mp3" {
		t.Fatalf("sent to SAB: %q", f.added)
	}
	items, _ := e.Queue(nil)
	if len(items) != 2 || items[0].Kind != KindAudiobook || items[1].Status != "failed" {
		t.Fatalf("queue = %+v", items)
	}
}

func TestInterruptedAudiobookImportResumes(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	f := &fakeAudioIndexers{jobStatus: func(string) string { return "Completed" }}
	f.start(t, e)
	if _, err := e.AutoGrabAudio(context.Background(), bookID, libID); err != nil {
		t.Fatal(err)
	}
	// a restart mid-import leaves the row "importing" with nothing running
	if _, err := e.db.Exec(`UPDATE queue SET status = 'importing'`); err != nil {
		t.Fatal(err)
	}
	// it must never be mistaken for a direct download waiting on disk
	if pending, _ := e.PendingImports(); len(pending) != 0 {
		t.Fatalf("pending direct imports = %+v", pending)
	}
	done := 0
	e.ImportAudio = func(context.Context, int64, string) error { done++; return nil }
	e.PollSab(context.Background())
	waitImports(t, e)
	if done != 1 {
		t.Fatalf("resumed imports = %d", done)
	}
}

func TestAudiobookGrabRefusesDirectSources(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t)
	rels, _, _ := e.SearchAudio(context.Background(), bookID) // nothing configured
	if len(rels) != 0 {
		t.Fatalf("results with no Prowlarr: %+v", rels)
	}
	_, err := e.GrabAudio(context.Background(), bookID, libID, releaseDirect())
	if err == nil {
		t.Fatal("a direct release was grabbed as an audiobook")
	}
}

func releaseDirect() release.Release {
	return release.Release{Title: "First Ember M4B", Source: "annas", Protocol: "direct", DownloadURL: "md5:abc"}
}

// A search that finds only the author's other books grabs nothing, and says
// so: the releases come back flagged and counted, not ranked as hits.
func TestOtherTitlesAreNeverGrabbed(t *testing.T) {
	e, _, _, bookID, libID := testEngine(t) // the book is "First Ember" by Mara Voss
	prowlarr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"title":"Mara Voss - Last Ember (First Ember #2) M4B","size":400000000,"downloadUrl":"http://x/nzb/2","indexer":"ndx","protocol":"usenet"},
			{"title":"First Ember 3: The Cinder Crown - Mara Voss M4B","size":400000000,"downloadUrl":"http://x/nzb/3","indexer":"ndx","protocol":"usenet"}]`))
	}))
	t.Cleanup(prowlarr.Close)
	sabCalls := 0
	sab := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sabCalls++
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(sab.Close)
	for k, v := range map[string]string{"prowlarr_url": prowlarr.URL, "prowlarr_api_key": "k", "sab_url": sab.URL, "sab_api_key": "k"} {
		if err := e.settings.Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	rels, stats, err := e.SearchAudio(context.Background(), bookID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rels) != 2 || !rels[0].TitleMismatch || !rels[1].TitleMismatch || rels[0].Score > 0 {
		t.Fatalf("releases = %+v", rels)
	}
	if stats[0].Count != 2 || stats[0].Mismatched != 2 {
		t.Fatalf("stats = %+v", stats)
	}
	grabbed, err := e.AutoGrabAudio(context.Background(), bookID, libID)
	if err != nil || grabbed || sabCalls != 0 {
		t.Fatalf("auto-grab took another book: grabbed=%v err=%v sab calls=%d", grabbed, err, sabCalls)
	}
}
