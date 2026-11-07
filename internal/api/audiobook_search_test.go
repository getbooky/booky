package api

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// TestSeriesDownloadIsSplitIntoBooks: a series pack grabbed for one book
// files each library book it holds with its own audiobook, leaves books the
// library doesn't have, and never replaces an audiobook nobody asked to.
func TestSeriesDownloadIsSplitIntoBooks(t *testing.T) {
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

	texts := map[string][]string{
		"The Glass Tide":   {"The tide came in made of glass and nobody could swim in it.", "Mara watched it shine from the old pier."},
		"The Iron Moon":    {"Every night the iron moon rang like a bell over the harbour.", "The fishermen counted the strokes before they slept."},
		"The Salt Crown":   {"The queen wore a crown of salt that melted in the rain.", "Her people learned to love the dry season best."},
		"The Last Harbour": {"Nobody remembered who built the last harbour.", "It was older than the sea itself, they said."},
	}
	// the library has three of the four; one already has an audiobook
	ids := map[string]int64{}
	for _, title := range []string{"The Glass Tide", "The Iron Moon", "The Last Harbour"} {
		id, _ := strconv.ParseInt(idStr(f.addBook(t, f.adminCooke, map[string]any{"title": title}, nil)["id"]), 10, 64)
		ids[title] = id
		epub := filepath.Join(root, "books", "Emmett Hale", title+".epub")
		minimalEPUB(t, epub, texts[title])
		if err := f.srv.Catalog.SetFile(f.lib, id, epub, "epub", 1); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.srv.db.Exec(`INSERT INTO audiobooks (book_id, file_path, format, status) VALUES (?, '/elsewhere/harbour.m4b', 'mov', 'ready')`, ids["The Last Harbour"]); err != nil {
		t.Fatal(err)
	}

	// the download: one folder per book, the first in two parts
	job := filepath.Join(root, "downloads", "Emmett Hale - Harbour Series (Books 1-4) MP3")
	files := map[string][]byte{
		"Book 1 - The Glass Tide/Part 1.mp3": spokenMP3(t, texts["The Glass Tide"][:1]),
		"Book 1 - The Glass Tide/Part 2.mp3": spokenMP3(t, texts["The Glass Tide"][1:]),
		"Book 2 - The Iron Moon/01.mp3":      spokenMP3(t, texts["The Iron Moon"]),
		"Book 3 - The Salt Crown/01.mp3":     spokenMP3(t, texts["The Salt Crown"]),
		"Book 4 - The Last Harbour/01.mp3":   spokenMP3(t, texts["The Last Harbour"]),
		"Emmett Hale - Harbour Series.nfo":   []byte("release notes"),
	}
	for name, data := range files {
		p := filepath.Join(job, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// grabbed for book 2
	if err := f.srv.ImportAudioFrom(context.Background(), ids["The Iron Moon"], job); err != nil {
		t.Fatal(err)
	}
	// books 1 and 2 are filed beside their own ebooks, book 1 joined
	for _, title := range []string{"The Glass Tide", "The Iron Moon"} {
		matches, _ := filepath.Glob(filepath.Join(root, "books", "Emmett Hale", title+".m4b"))
		if title == "The Iron Moon" {
			matches, _ = filepath.Glob(filepath.Join(root, "books", "Emmett Hale", title+".mp3"))
		}
		if len(matches) != 1 {
			entries, _ := os.ReadDir(filepath.Join(root, "books", "Emmett Hale"))
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("%s: no audiobook beside the ebook; folder has %q", title, names)
		}
	}
	// and both get their read-along
	for _, title := range []string{"The Glass Tide", "The Iron Moon"} {
		book := strconv.FormatInt(ids[title], 10)
		waitFor(t, 120*time.Second, func() bool {
			_, view := f.do(t, f.adminCooke, "GET", "/api/v1/books/"+book+"/audiobook", nil)
			if view["status"] == "failed" {
				t.Fatalf("%s read-along failed: %v", title, view["error"])
			}
			return view["readAlong"] == true
		})
	}
	// the book that had an audiobook keeps it
	a, err := f.srv.Audiobooks.Get(ids["The Last Harbour"])
	if err != nil || a.Path != "/elsewhere/harbour.m4b" {
		t.Fatalf("existing audiobook replaced: %+v %v", a, err)
	}
	// the history says what happened, naming the book not in the library
	_, hist := f.do(t, f.adminCooke, "GET", "/api/v1/history", nil)
	found := false
	for _, h := range hist["history"].([]any) {
		m := h.(map[string]any)
		if m["kind"] == "series download" {
			d := m["detail"].(string)
			found = strings.Contains(d, "filed 2") && strings.Contains(d, "The Last Harbour") &&
				strings.Contains(d, "not in your library: Book 3 - The Salt Crown")
			if !found {
				t.Errorf("summary: %s", d)
			}
		}
	}
	if !found {
		t.Error("no series download summary in the history")
	}

	// a pack without the book it was grabbed for says so
	other, _ := strconv.ParseInt(idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Nowhere Near"}, nil)["id"]), 10, 64)
	if err := f.srv.ImportAudioFrom(context.Background(), other, job); !errors.Is(err, errNotInDownload) {
		t.Fatalf("pack without the book: %v", err)
	}
}
