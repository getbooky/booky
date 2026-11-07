package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/getbooky/booky/internal/readalong"
)

// pairDevice makes a KOReader device for the cookie's account, reaching the
// given tags, and returns its raw bearer token.
func (f *scopedFixture) pairDevice(t *testing.T, c *http.Cookie, tags []int64) string {
	t.Helper()
	if err := f.srv.Settings.Set("server_url", "http://booky.local:8787"); err != nil {
		t.Fatal(err)
	}
	rec, out := f.do(t, c, "POST", "/api/v1/devices", map[string]any{"name": "Kindle", "tagIds": tags, "autoTagIds": []int64{}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("pair device: %d %s", rec.Code, rec.Body)
	}
	token, err := f.srv.KoReader.RawToken(int64(out["id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func deviceDo(t *testing.T, h http.Handler, token, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, r)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// fakeReadAlong puts a built read-along on disk for a book — edition,
// timing and one chunk — and marks it ready, without running the tools.
func (f *scopedFixture) fakeReadAlong(t *testing.T, bookID int64, chunkFile string) {
	t.Helper()
	out := f.srv.ReadAlong.OutDir(bookID)
	if err := os.MkdirAll(filepath.Join(out, readalong.ChunkDir), 0o755); err != nil {
		t.Fatal(err)
	}
	minimalEPUB(t, filepath.Join(out, readalong.EditionFile), []string{"One.", "Two."})
	timing := map[string]any{
		"version": 1, "duration": 4.0,
		"chunks": []map[string]any{{"file": chunkFile, "start": 0, "duration": 4.0}},
		"units": []map[string]any{
			{"id": "booky-s1", "href": "c1.xhtml", "begin": 0.5, "end": 1.5},
			{"id": "booky-s2", "href": "c1.xhtml", "begin": 2.0, "end": 3.0},
			{"id": "booky-s9", "href": "elsewhere.xhtml", "begin": 3.0, "end": 3.5}, // not in the spine
		},
	}
	data, _ := json.Marshal(timing)
	if err := os.WriteFile(filepath.Join(out, readalong.TimingFile), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(out, readalong.ChunkDir, "0001.mp3"), []byte("not really audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.db.Exec(`INSERT INTO audiobooks (book_id, file_path, format, status) VALUES (?, '/nowhere.mp3', 'mp3', 'ready')`, bookID); err != nil {
		t.Fatal(err)
	}
}

func TestKoReaderReadAlongFollowsTheDevice(t *testing.T) {
	f := newScopedFixture(t)
	mine := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Haze"}, []int64{f.mine})["id"])
	private := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow"}, []int64{f.off})["id"])
	mineID, _ := strconv.ParseInt(mine, 10, 64)
	privateID, _ := strconv.ParseInt(private, 10, 64)
	token := f.pairDevice(t, f.userCookie, []int64{f.mine})
	base := func(tag int64, book string) string {
		return "/api/koreader/v1/readalong/" + strconv.FormatInt(tag, 10) + "/" + book
	}

	// no read-along yet: nothing to fetch
	if rec := deviceDo(t, f.h, token, "GET", base(f.mine, mine), nil); rec.Code != http.StatusNotFound {
		t.Fatalf("before a read-along: %d", rec.Code)
	}
	f.fakeReadAlong(t, mineID, "0001.mp3")
	f.fakeReadAlong(t, privateID, "0001.mp3")

	// every route wants the device token, and the tag must reach the book
	for _, suffix := range []string{"", "/edition", "/audio/1", "/position"} {
		if rec := deviceDo(t, f.h, "", "GET", base(f.mine, mine)+suffix, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("no token %s: %d", suffix, rec.Code)
		}
		if rec := deviceDo(t, f.h, "wrong", "GET", base(f.mine, mine)+suffix, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("bad token %s: %d", suffix, rec.Code)
		}
		// someone else's book, through the device's own tag or the other one
		if rec := deviceDo(t, f.h, token, "GET", base(f.mine, private)+suffix, nil); rec.Code != http.StatusNotFound {
			t.Errorf("unreachable book via own tag %s: %d", suffix, rec.Code)
		}
		if rec := deviceDo(t, f.h, token, "GET", base(f.off, private)+suffix, nil); rec.Code != http.StatusNotFound {
			t.Errorf("unreachable book via other tag %s: %d", suffix, rec.Code)
		}
	}

	// the manifest: sentences carry the engine's document number; one whose
	// document isn't in the spine is left out
	rec := deviceDo(t, f.h, token, "GET", base(f.mine, mine), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("manifest: %d %s", rec.Code, rec.Body)
	}
	var m struct {
		Title string
		Audio struct {
			Format         string
			Rate, Channels int
		}
		Chunks []struct {
			N        int
			Start    float64
			Duration float64
		}
		Units []struct {
			ID         string
			Frag       int
			Begin, End float64
		}
		Edition struct{ Size int64 }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if m.Title != "Haze" || m.Audio.Format != "s16le" || m.Audio.Rate != readalong.DeviceRate || m.Audio.Channels != 1 {
		t.Fatalf("manifest = %s", rec.Body)
	}
	if len(m.Chunks) != 1 || m.Chunks[0].N != 1 || m.Edition.Size == 0 {
		t.Fatalf("manifest = %s", rec.Body)
	}
	if len(m.Units) != 2 || m.Units[0].ID != "booky-s1" || m.Units[0].Frag != 0 || m.Units[1].Begin != 2.0 {
		t.Fatalf("units = %+v", m.Units)
	}

	// the edition
	rec = deviceDo(t, f.h, token, "GET", base(f.mine, mine)+"/edition", nil)
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/epub+zip" || int64(rec.Body.Len()) != m.Edition.Size {
		t.Fatalf("edition: %d %s %d", rec.Code, rec.Header().Get("Content-Type"), rec.Body.Len())
	}

	// chunks are numbered from 1, and only those
	for _, n := range []string{"0", "2", "-1", "x", "1.5"} {
		if rec := deviceDo(t, f.h, token, "GET", base(f.mine, mine)+"/audio/"+n, nil); rec.Code != http.StatusNotFound {
			t.Errorf("audio/%s: %d", n, rec.Code)
		}
	}

	// position: the device's owner's, shared with the web player
	if rec := deviceDo(t, f.h, token, "PUT", base(f.mine, mine)+"/position", map[string]any{"position": 61.5}); rec.Code != http.StatusOK {
		t.Fatalf("put position: %d", rec.Code)
	}
	for _, bad := range []any{-1, 1e9, "soon"} {
		if rec := deviceDo(t, f.h, token, "PUT", base(f.mine, mine)+"/position", map[string]any{"position": bad}); rec.Code != http.StatusBadRequest {
			t.Errorf("position %v: %d", bad, rec.Code)
		}
	}
	if _, out := f.do(t, f.userCookie, "GET", "/api/v1/books/"+mine+"/listen", nil); out["position"] != 61.5 {
		t.Fatalf("web sees %v", out["position"])
	}
	if _, err := f.do(t, f.userCookie, "PUT", "/api/v1/books/"+mine+"/listen", map[string]any{"position": 90}); err == nil {
		t.Fatal("no response")
	}
	rec = deviceDo(t, f.h, token, "GET", base(f.mine, mine)+"/position", nil)
	if !strings.Contains(rec.Body.String(), `"position":90`) {
		t.Fatalf("device sees %s", rec.Body)
	}

	// the sync list says which books have one (it lists books on the shelf)
	shelved := filepath.Join(t.TempDir(), "Haze.epub")
	minimalEPUB(t, shelved, []string{"One."})
	if err := f.srv.Catalog.SetFile(f.lib, mineID, shelved, "epub", 1); err != nil {
		t.Fatal(err)
	}
	rec = deviceDo(t, f.h, token, "GET", "/api/koreader/v1/sync", nil)
	if !strings.Contains(rec.Body.String(), `"readAlong":true`) {
		t.Fatalf("sync: %s", rec.Body)
	}
}

func TestKoReaderReadAlongRefusesAForgedChunkName(t *testing.T) {
	f := newScopedFixture(t)
	mine := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Haze"}, []int64{f.mine})["id"])
	id, _ := strconv.ParseInt(mine, 10, 64)
	token := f.pairDevice(t, f.userCookie, []int64{f.mine})
	// a timing file naming something outside the chunk folder is never
	// followed
	f.fakeReadAlong(t, id, "../../../../etc/passwd")
	rec := deviceDo(t, f.h, token, "GET", "/api/koreader/v1/readalong/"+strconv.FormatInt(f.mine, 10)+"/"+mine+"/audio/1", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("forged chunk: %d", rec.Code)
	}
}

// TestKoReaderReadAlongAudioIsPCM decodes a real chunk: the device gets raw
// PCM at the device rate, as long as the chunk.
func TestKoReaderReadAlongAudioIsPCM(t *testing.T) {
	if os.Getenv("BOOKY_READALONG_IT") == "" {
		t.Skip("set BOOKY_READALONG_IT=1 to run against ffmpeg/vosk/espeak")
	}
	f := newScopedFixture(t)
	mine := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Haze"}, []int64{f.mine})["id"])
	id, _ := strconv.ParseInt(mine, 10, 64)
	token := f.pairDevice(t, f.userCookie, []int64{f.mine})
	f.fakeReadAlong(t, id, "0001.mp3")
	chunk := filepath.Join(f.srv.ReadAlong.OutDir(id), readalong.ChunkDir, "0001.mp3")
	if err := os.WriteFile(chunk, spokenMP3(t, []string{"Alice was beginning to get very tired."}), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	probe, err := f.srv.ReadAlong.Tools.Probe(ctx, chunk, readalong.MP3)
	if err != nil {
		t.Fatal(err)
	}
	rec := deviceDo(t, f.h, token, "GET", "/api/koreader/v1/readalong/"+strconv.FormatInt(f.mine, 10)+"/"+mine+"/audio/1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("audio: %d %s", rec.Code, rec.Body)
	}
	got := float64(rec.Body.Len()) / readalong.DeviceBytesPerSec
	if rec.Body.Len()%2 != 0 || got < probe.Duration-0.1 || got > probe.Duration+0.1 {
		t.Fatalf("%d bytes = %.2fs of PCM for a %.2fs chunk", rec.Body.Len(), got, probe.Duration)
	}
}
