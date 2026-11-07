package api

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestAudioProfileIsAnAdminSetting(t *testing.T) {
	f := newScopedFixture(t)
	if rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/profiles/audiobook", nil); rec.Code != http.StatusForbidden {
		t.Errorf("user reads profile: %d", rec.Code)
	}
	if rec, _ := f.do(t, f.userCookie, "PUT", "/api/v1/profiles/audiobook", map[string]any{"formats": []string{"mp3"}}); rec.Code != http.StatusForbidden {
		t.Errorf("user writes profile: %d", rec.Code)
	}
	rec, out := f.do(t, f.adminCooke, "GET", "/api/v1/profiles/audiobook", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}
	// the defaults: M4B first, abridged readings avoided, the ebook
	// profile's languages carried over
	if fmtList(out["formats"]) != "m4b,mp3,m4a" || out["minSizeMB"] != 5.0 || out["languages"] != "english" {
		t.Fatalf("defaults = %v", out)
	}
	for _, bad := range []map[string]any{
		{"formats": []string{}},
		{"formats": []string{"epub"}},
		{"formats": []string{"mp3", "mp3"}},
		{"formats": []string{"mp3"}, "minSizeMB": 500, "maxSizeMB": 100},
		{"formats": []string{"mp3"}, "minSizeMB": -1},
	} {
		if rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/profiles/audiobook", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%v: got %d, want 400", bad, rec.Code)
		}
	}
	good := map[string]any{"formats": []string{"mp3", "m4b"}, "languages": "english\nswedish",
		"preferredTerms": "unabridged", "avoidedTerms": "abridged", "minSizeMB": 10, "maxSizeMB": 2000}
	if rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/profiles/audiobook", good); rec.Code != http.StatusOK {
		t.Fatalf("put: %d", rec.Code)
	}
	if _, out := f.do(t, f.adminCooke, "GET", "/api/v1/profiles/audiobook", nil); fmtList(out["formats"]) != "mp3,m4b" || out["maxSizeMB"] != 2000.0 {
		t.Fatalf("saved = %v", out)
	}
	// the ebook profile list doesn't grow an audiobook entry
	_, profiles := f.do(t, f.adminCooke, "GET", "/api/v1/profiles", nil)
	if list, _ := profiles["profiles"].([]any); len(list) != 1 {
		t.Fatalf("ebook profiles = %v", profiles)
	}
}

func fmtList(v any) string {
	list, _ := v.([]any)
	out := ""
	for i, x := range list {
		if i > 0 {
			out += ","
		}
		s, _ := x.(string)
		out += s
	}
	return out
}

func TestAudioSearchFollowsTheBook(t *testing.T) {
	f := newScopedFixture(t)
	private := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow"}, []int64{f.off})["id"])
	mine := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Haze"}, []int64{f.mine})["id"])

	// someone else's book: nothing about its audiobook can be touched
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/books/" + private + "/audiobook/releases"},
		{"POST", "/api/v1/books/" + private + "/audiobook/grab"},
		{"POST", "/api/v1/books/" + private + "/audiobook/autograb"},
		{"PUT", "/api/v1/books/" + private + "/audiobook/monitored"},
	} {
		body := map[string]any{"monitored": true, "release": map[string]any{"title": "x", "downloadUrl": "http://x", "protocol": "usenet"}}
		if rec, _ := f.do(t, f.userCookie, tc.method, tc.path, body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", tc.method, tc.path, rec.Code)
		}
	}

	// their own book: monitoring is theirs to switch, and off to begin with
	_, before := f.do(t, f.userCookie, "GET", "/api/v1/books/"+mine, nil)
	if before["audioMonitored"] != nil {
		t.Fatalf("audio monitored by default: %v", before["audioMonitored"])
	}
	if rec, _ := f.do(t, f.userCookie, "PUT", "/api/v1/books/"+mine+"/audiobook/monitored", map[string]any{"monitored": true}); rec.Code != http.StatusOK {
		t.Fatalf("monitor: %d", rec.Code)
	}
	if _, out := f.do(t, f.userCookie, "GET", "/api/v1/books/"+mine, nil); out["audioMonitored"] != true {
		t.Fatalf("audioMonitored = %v", out["audioMonitored"])
	}
	// monitoring the audiobook leaves the ebook's monitoring alone
	if _, out := f.do(t, f.userCookie, "GET", "/api/v1/books/"+mine, nil); out["monitored"] != before["monitored"] {
		t.Fatalf("ebook monitored went from %v to %v", before["monitored"], out["monitored"])
	}

	// once it has an audiobook, grabbing another (which replaces it, and so
	// deletes a file) is an admin's
	id, _ := strconv.ParseInt(mine, 10, 64)
	if _, err := f.srv.db.Exec(`INSERT INTO audiobooks (book_id, file_path, format, status) VALUES (?, '/nowhere.mp3', 'mp3', 'ready')`, id); err != nil {
		t.Fatal(err)
	}
	grab := map[string]any{"release": map[string]any{"title": "Haze M4B", "downloadUrl": "http://x/nzb", "protocol": "usenet"}}
	if rec, _ := f.do(t, f.userCookie, "POST", "/api/v1/books/"+mine+"/audiobook/grab", grab); rec.Code != http.StatusForbidden {
		t.Errorf("user replacing an audiobook by grab: got %d, want 403", rec.Code)
	}
	// an admin may (here it fails further on — no SABnzbd — but not on access)
	if rec, _ := f.do(t, f.adminCooke, "POST", "/api/v1/books/"+mine+"/audiobook/grab", grab); rec.Code == http.StatusForbidden {
		t.Errorf("admin grab refused: %d", rec.Code)
	}
}

// TestDownloadedAudiobookIsFiled runs a finished download's folder of parts
// through the same import an upload takes: joined, filed beside the ebook,
// lined up.
func TestDownloadedAudiobookIsFiled(t *testing.T) {
	if os.Getenv("BOOKY_READALONG_IT") == "" {
		t.Skip("set BOOKY_READALONG_IT=1 to run against ffmpeg/vosk/espeak")
	}
	f := newScopedFixture(t)
	root := t.TempDir()
	f.srv.mediaRoot = root
	if _, err := f.srv.db.Exec(`UPDATE libraries SET root_path = ?`, filepath.Join(root, "books")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.srv.ReadAlong.Run(ctx)

	first := []string{"Alice was beginning to get very tired of sitting by her sister on the bank.",
		"Once or twice she had peeped into the book her sister was reading."}
	second := []string{"So she was considering in her own mind whether the pleasure of making a daisy chain would be worth the trouble.",
		"Suddenly a White Rabbit with pink eyes ran close by her."}
	book := idStr(f.addBook(t, f.userCookie, map[string]any{"title": "Alice"}, nil)["id"])
	id, _ := strconv.ParseInt(book, 10, 64)
	epub := filepath.Join(root, "books", "Emmett Hale", "Alice.epub")
	minimalEPUB(t, epub, append(append([]string{}, first...), second...))
	if err := f.srv.Catalog.SetFile(f.lib, id, epub, "epub", 1); err != nil {
		t.Fatal(err)
	}

	// a SAB job folder: two parts in a subfolder, and the usual clutter
	job := filepath.Join(root, "downloads", "Alice (Unabridged)")
	for name, data := range map[string][]byte{
		"CD1/01 - Part One.mp3": spokenMP3(t, first),
		"CD1/02 - Part Two.mp3": spokenMP3(t, second),
		"Alice.nfo":             []byte("release notes"),
		"cover.jpg":             []byte("not really a jpeg"),
	} {
		p := filepath.Join(job, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.srv.ImportAudioFrom(context.Background(), id, job); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "books", "Emmett Hale", "Alice.m4b")); err != nil {
		t.Fatalf("joined audiobook not beside the ebook: %v", err)
	}
	var view map[string]any
	waitFor(t, 120*time.Second, func() bool {
		_, view = f.do(t, f.userCookie, "GET", "/api/v1/books/"+book+"/audiobook", nil)
		if view["status"] == "failed" {
			t.Fatalf("read-along failed: %v", view["error"])
		}
		return view["readAlong"] == true
	})
	// the job folder is left for SAB's cleanup to remove
	if _, err := os.Stat(filepath.Join(job, "CD1", "01 - Part One.mp3")); err != nil {
		t.Errorf("parts were taken from the job folder: %v", err)
	}

	// an empty download is refused, not filed
	empty := filepath.Join(root, "downloads", "nothing")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.ImportAudioFrom(context.Background(), id, empty); err == nil {
		t.Fatal("an empty download was filed")
	}
}
