package release

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fetch performs a real GET against srv so SaveDownload gets a live response
// with whatever headers the handler set.
func fetch(t *testing.T, url string) *http.Response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

// Two grabs in flight at once are routinely handed the same name by the same
// source (direct sources serve everything from ".../redirection"). The second
// must not land on top of the first, which may still be waiting to import.
func TestSaveDownloadNeverOverwrites(t *testing.T) {
	// each download serves distinct bytes, so a clobbered file is visible as
	// the wrong content rather than only as a missing name
	body := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	dir := t.TempDir()

	var paths, bodies []string
	for i := 1; i <= 3; i++ {
		body = fmt.Sprintf("book-%d", i)
		bodies = append(bodies, body)
		p, err := SaveDownload(fetch(t, srv.URL+"/redirection"), srv.URL+"/redirection", dir, "epub", "dl")
		if err != nil {
			t.Fatalf("SaveDownload %d: %v", i, err)
		}
		paths = append(paths, p)
	}
	want := []string{"redirection.epub", "redirection (2).epub", "redirection (3).epub"}
	for i, p := range paths {
		if base := filepath.Base(p); base != want[i] {
			t.Errorf("download %d landed on %q, want %q", i+1, base, want[i])
		}
		got, err := os.ReadFile(p) //nolint:gosec // G304: p is this test's own temp dir
		if err != nil || string(got) != bodies[i] {
			t.Errorf("download %d content = %q (%v) — an earlier download was clobbered", i+1, got, err)
		}
	}
}

func TestSaveDownloadPrefersContentDisposition(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="First Ember.epub"`)
		_, _ = io.WriteString(w, "PK\x03\x04rest")
	}))
	defer srv.Close()
	path, err := SaveDownload(fetch(t, srv.URL), srv.URL+"/redirection", t.TempDir(), "", "dl")
	if err != nil {
		t.Fatal(err)
	}
	if base := filepath.Base(path); base != "First Ember.epub" {
		t.Errorf("saved as %q, want %q", base, "First Ember.epub")
	}
	// the reservation must not survive as a second, empty file
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 1 {
		t.Errorf("downloads dir holds %d files, want 1: %v", len(entries), entries)
	}
}

// Nothing names the file and the release's format is unknown: the bytes get
// the last word, so a pdf isn't shelved as an epub.
func TestSaveDownloadSniffsExtension(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "%PDF-1.7\nbody")
	}))
	defer srv.Close()
	path, err := SaveDownload(fetch(t, srv.URL), srv.URL+"/redirection", t.TempDir(), "", "dl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(path, ".pdf") {
		t.Errorf("saved as %q, want a .pdf", filepath.Base(path))
	}
}

func TestSniffExt(t *testing.T) {
	mobi := make([]byte, 68)
	copy(mobi[60:], []byte("BOOKMOBI"))
	cases := []struct {
		name string
		head []byte
		want string
	}{
		{"pdf", []byte("%PDF-1.7"), ".pdf"},
		{"zip reads as epub", []byte("PK\x03\x04\x14\x00"), ".epub"},
		{"cbr", []byte("Rar!\x1a\x07\x00"), ".cbr"},
		{"mobi", mobi, ".mobi"},
		{"fb2", []byte(`<?xml version="1.0"?><FictionBook>`), ".fb2"},
		{"unknown stays unknown", []byte("just some prose"), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "f")
			if err := os.WriteFile(p, c.head, 0o600); err != nil {
				t.Fatal(err)
			}
			if got := SniffExt(p); got != c.want {
				t.Errorf("SniffExt(%s) = %q, want %q", c.name, got, c.want)
			}
		})
	}
}
