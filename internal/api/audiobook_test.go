package api

import (
	"archive/zip"
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// uploadAudio posts files to a book's audiobook endpoint as c.
func (f *scopedFixture) uploadAudio(t *testing.T, c *http.Cookie, bookID string, files map[string][]byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for name, data := range files {
		w, err := mw.CreateFormFile("file", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/api/v1/books/"+bookID+"/audiobook", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

func TestAudiobookAccessFollowsTheBook(t *testing.T) {
	f := newScopedFixture(t)
	private := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow"}, []int64{f.off})["id"])
	mine := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Haze"}, []int64{f.mine})["id"])

	// a book outside the user's tags: every audiobook route is closed
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/v1/books/" + private + "/audiobook"},
		{"GET", "/api/v1/books/" + private + "/audiobook/file"},
		{"GET", "/api/v1/books/" + private + "/readalong/timing.json"},
		{"GET", "/api/v1/books/" + private + "/readalong/audio/0001.mp3"},
		{"GET", "/api/v1/books/" + private + "/listen"},
		{"PUT", "/api/v1/books/" + private + "/listen"},
	} {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, map[string]any{"position": 1})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
	if rec := f.uploadAudio(t, f.userCookie, private, map[string][]byte{"a.mp3": []byte("ID3")}); rec.Code != http.StatusForbidden {
		t.Errorf("upload to someone else's book: got %d, want 403", rec.Code)
	}

	// choosing server paths, rebuilding and deleting are an admin's, even on
	// the user's own book
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/v1/books/" + mine + "/audiobook/import"},
		{"POST", "/api/v1/books/" + mine + "/audiobook/rebuild"},
		{"DELETE", "/api/v1/books/" + mine + "/audiobook?file=delete"},
	} {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, map[string]any{"path": "/etc"})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", tc.method, tc.path, rec.Code)
		}
	}

	// the user's own book: no audiobook yet reads as 404, not 403
	if rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/books/"+mine+"/audiobook", nil); rec.Code != http.StatusNotFound {
		t.Errorf("no audiobook: got %d, want 404", rec.Code)
	}
	// once it has one, replacing it (which deletes the old file) is an
	// admin's too
	id, _ := strconv.ParseInt(mine, 10, 64)
	if _, err := f.srv.db.Exec(`INSERT INTO audiobooks (book_id, file_path, format, status) VALUES (?, '/nowhere.mp3', 'mp3', 'ready')`, id); err != nil {
		t.Fatal(err)
	}
	if rec := f.uploadAudio(t, f.userCookie, mine, map[string][]byte{"a.mp3": []byte("ID3")}); rec.Code != http.StatusForbidden {
		t.Errorf("user replacing an audiobook: got %d, want 403", rec.Code)
	}
}

func TestListenPosition(t *testing.T) {
	f := newScopedFixture(t)
	book := idStr(f.addBook(t, f.userCookie, map[string]any{"title": "Haze"}, nil)["id"])
	for _, bad := range []any{-1, 1e9, "soon"} {
		if rec, _ := f.do(t, f.userCookie, "PUT", "/api/v1/books/"+book+"/listen", map[string]any{"position": bad}); rec.Code != http.StatusBadRequest {
			t.Errorf("position %v: got %d, want 400", bad, rec.Code)
		}
	}
	if rec, _ := f.do(t, f.userCookie, "PUT", "/api/v1/books/"+book+"/listen", map[string]any{"position": 754.25}); rec.Code != http.StatusOK {
		t.Fatalf("put: %d", rec.Code)
	}
	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/books/"+book+"/listen", nil)
	if rec.Code != http.StatusOK || out["position"] != 754.25 {
		t.Fatalf("get: %d %v", rec.Code, out)
	}
	// positions are per account
	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/books/"+book+"/listen", nil)
	if rec.Code != http.StatusOK || out["position"] != 0.0 {
		t.Fatalf("admin sees someone else's position: %v", out)
	}
}

func TestReadAlongFilesAreWhitelisted(t *testing.T) {
	f := newScopedFixture(t)
	book := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Haze"}, nil)["id"])
	// even with a read-along on disk and the row ready, only the three kinds
	// of file are ever served
	id, _ := strconv.ParseInt(book, 10, 64)
	out := f.srv.ReadAlong.OutDir(id)
	if err := os.MkdirAll(filepath.Join(out, "audio"), 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"timing.json": `{}`, "secret.txt": "no"} {
		if err := os.WriteFile(filepath.Join(out, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.srv.db.Exec(`INSERT INTO audiobooks (book_id, file_path, format, status) VALUES (?, '/nowhere.mp3', 'mp3', 'ready')`, id); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.do(t, f.adminCooke, "GET", "/api/v1/books/"+book+"/readalong/timing.json", nil); rec.Code != http.StatusOK {
		t.Fatalf("timing.json: %d", rec.Code)
	}
	for _, p := range []string{"secret.txt", "audio/1.mp3", "audio/0001.mp3.bak", "../../booky.db",
		"audio/../secret.txt", "timing.json/../secret.txt", "%2e%2e/booky.db", "audio/0001.MP3"} {
		rec, _ := f.do(t, f.adminCooke, "GET", "/api/v1/books/"+book+"/readalong/"+p, nil)
		if rec.Code == http.StatusOK {
			t.Errorf("served %q", p)
		}
	}
}

func TestAudiobookUploadRejectsNonAudio(t *testing.T) {
	f := newScopedFixture(t)
	// uploads are checked with ffprobe alone: an install with ffmpeg but no
	// speech model still takes (and vets) them
	if f.srv.ReadAlong.Tools.FFprobe == "" {
		book := idStr(f.addBook(t, f.userCookie, map[string]any{"title": "Haze"}, nil)["id"])
		if rec := f.uploadAudio(t, f.userCookie, book, map[string][]byte{"a.mp3": []byte("ID3")}); rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("without ffmpeg: got %d, want 503", rec.Code)
		}
		t.Skip("ffmpeg not installed; the rest needs it")
	}
	book := idStr(f.addBook(t, f.userCookie, map[string]any{"title": "Haze"}, nil)["id"])
	for name, data := range map[string][]byte{
		"book.exe":       []byte("MZ"),
		"playlist.mp3":   []byte("#EXTM3U\nfile:///config/booky.db\n"),
		"concat.m4b":     []byte("ffconcat version 1.0\nfile /config/secret.key\n"),
		"../../evil.mp3": []byte("#EXTM3U\n"),
	} {
		if rec := f.uploadAudio(t, f.userCookie, book, map[string][]byte{name: data}); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 400", name, rec.Code)
		}
	}
	// nothing was left behind in the uploads folder
	left, _ := filepath.Glob(filepath.Join(f.srv.Settings.Get("downloads_dir"), "uploads", "audiobook-*"))
	if len(left) > 0 {
		t.Errorf("rejected uploads left %v", left)
	}
}

// ---- the whole flow, against the real tools ----

func minimalEPUB(t *testing.T, path string, sentences []string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	files := []struct{ name, body string }{{"mimetype", "application/epub+zip"}}
	for name, body := range map[string]string{
		"META-INF/container.xml": `<?xml version="1.0"?><container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`,
		"content.opf":            `<?xml version="1.0"?><package xmlns="http://www.idpf.org/2007/opf" version="3.0"><metadata/><manifest><item id="c1" href="c1.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="c1"/></spine></package>`,
		"c1.xhtml":               `<html xmlns="http://www.w3.org/1999/xhtml"><body><p>` + strings.Join(sentences, " ") + `</p></body></html>`,
	} {
		files = append(files, struct{ name, body string }{name, body})
	}
	for i, file := range files {
		hdr := &zip.FileHeader{Name: file.name, Method: zip.Deflate}
		if i == 0 {
			hdr.Method = zip.Store // mimetype first and uncompressed, as EPUB requires
		}
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(file.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func spokenMP3(t *testing.T, sentences []string) []byte {
	t.Helper()
	dir := t.TempDir()
	wav, mp3 := filepath.Join(dir, "a.wav"), filepath.Join(dir, "a.mp3")
	if out, err := exec.Command("espeak-ng", //nolint:gosec // G204: test speech, fixed sentences
		"-s", "165", "-w", wav, strings.Join(sentences, " ")).CombinedOutput(); err != nil {
		t.Fatalf("espeak: %v %s", err, out)
	}
	if out, err := exec.Command("ffmpeg", "-v", //nolint:gosec // G204: test fixture, our own temp files
		"error", "-i", wav, "-c:a", "libmp3lame", "-y", mp3).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	data, err := os.ReadFile(mp3)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestAudiobookUploadToReadAlong(t *testing.T) {
	if os.Getenv("BOOKY_READALONG_IT") == "" {
		t.Skip("set BOOKY_READALONG_IT=1 to run against ffmpeg/vosk/espeak")
	}
	f := newScopedFixture(t)
	if f.srv.ReadAlong.ToolsErr != nil {
		t.Fatal(f.srv.ReadAlong.ToolsErr)
	}
	root := t.TempDir()
	f.srv.mediaRoot = root
	if err := f.srv.Settings.Set("downloads_dir", filepath.Join(root, "downloads")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.db.Exec(`UPDATE libraries SET root_path = ?`, filepath.Join(root, "books")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go f.srv.ReadAlong.Run(ctx)

	sentences := []string{
		"Alice was beginning to get very tired of sitting by her sister on the bank.",
		"Once or twice she had peeped into the book her sister was reading.",
		"So she was considering in her own mind whether the pleasure of making a daisy chain would be worth the trouble.",
		"Suddenly a White Rabbit with pink eyes ran close by her.",
	}
	out := f.addBook(t, f.userCookie, map[string]any{"title": "Alice"}, nil)
	book := idStr(out["id"])
	id, _ := strconv.ParseInt(book, 10, 64)
	epub := filepath.Join(root, "books", "Emmett Hale", "Alice.epub")
	minimalEPUB(t, epub, sentences)
	if err := f.srv.Catalog.SetFile(f.lib, id, epub, "epub", 1); err != nil {
		t.Fatal(err)
	}

	if rec := f.uploadAudio(t, f.userCookie, book, map[string][]byte{"Alice (Unabridged).mp3": spokenMP3(t, sentences)}); rec.Code != http.StatusAccepted {
		t.Fatalf("upload: %d %s", rec.Code, rec.Body)
	}
	// it lands beside the ebook, same name
	waitFor(t, 60*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(root, "books", "Emmett Hale", "Alice.mp3"))
		return err == nil
	})
	var view map[string]any
	waitFor(t, 120*time.Second, func() bool {
		_, view = f.do(t, f.userCookie, "GET", "/api/v1/books/"+book+"/audiobook", nil)
		if view["status"] == "failed" {
			t.Fatalf("read-along failed: %v", view["error"])
		}
		return view["readAlong"] == true
	})
	if view["filePath"] != nil {
		t.Error("a plain user must not see the server path")
	}
	if m, _ := view["match"].(float64); m < 0.45 {
		t.Errorf("match %.2f", m)
	}
	if _, out := f.do(t, f.userCookie, "GET", "/api/v1/books/"+book, nil); out["audio"] != "ready" {
		t.Errorf("book audio state = %v", out["audio"])
	}

	// the read-along serves, and audio supports seeking
	for _, p := range []string{"readalong.epub", "timing.json"} {
		if rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/books/"+book+"/readalong/"+p, nil); rec.Code != http.StatusOK {
			t.Errorf("%s: %d", p, rec.Code)
		}
	}
	req := httptest.NewRequest("GET", "/api/v1/books/"+book+"/readalong/audio/0001.mp3", nil)
	req.AddCookie(f.userCookie)
	req.Header.Set("Range", "bytes=0-99")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusPartialContent || rec.Body.Len() != 100 || rec.Header().Get("Content-Type") != "audio/mpeg" {
		t.Errorf("range request: %d, %d bytes, %s", rec.Code, rec.Body.Len(), rec.Header().Get("Content-Type"))
	}

	// the user who can't see the book still can't hear it
	if _, err := f.srv.Catalog.SetBookTags(id, []int64{f.off}); err != nil {
		t.Fatal(err)
	}
	if rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/books/"+book+"/readalong/timing.json", nil); rec.Code != http.StatusForbidden {
		t.Errorf("after unsharing: %d", rec.Code)
	}

	// deleting with the file removes the audio and the read-along
	if rec, _ := f.do(t, f.adminCooke, "DELETE", "/api/v1/books/"+book+"/audiobook?file=delete", nil); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(root, "books", "Emmett Hale", "Alice.mp3")); !os.IsNotExist(err) {
		t.Error("audio file still on disk")
	}
	if _, err := os.Stat(f.srv.ReadAlong.OutDir(id)); !os.IsNotExist(err) {
		t.Error("read-along still on disk")
	}
	if _, err := os.Stat(epub); err != nil {
		t.Error("deleting the audiobook must not touch the ebook")
	}
}

func waitFor(t *testing.T, limit time.Duration, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v", limit)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
