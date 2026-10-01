package zlibrary

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func fakeZlib(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/eapi/user/login", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.FormValue("email") != "reader@example.com" || r.FormValue("password") != "hunter2" {
			_, _ = w.Write([]byte(`{"error":"Incorrect email or password"}`))
			return
		}
		_, _ = w.Write([]byte(`{"user":{"id":4242,"remix_userkey":"abc123key"}}`))
	})
	mux.HandleFunc("/eapi/user/profile", func(w http.ResponseWriter, r *http.Request) {
		if cookie, _ := r.Cookie("remix_userkey"); cookie == nil || cookie.Value != "abc123key" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"user":{"downloads_today":3,"downloads_limit":10}}`))
	})
	mux.HandleFunc("/eapi/book/search", func(w http.ResponseWriter, r *http.Request) {
		// mirror the real eapi shape: filesizeString is a human string, the
		// numeric byte count is in filesize, and success:1 wraps it
		_, _ = w.Write([]byte(`{"success":1,"books":[
			{"id":999,"hash":"deadbeef","title":"First Ember","author":"Mara Voss","extension":"epub","filesize":1200000,"filesizeString":"1.15 MB","year":2023,"language":"english"}]}`))
	})
	mux.HandleFunc("/eapi/book/999/deadbeef/file", func(w http.ResponseWriter, r *http.Request) {
		// real eapi hands back a redirect endpoint whose URL path carries no
		// filename — the real name lives in the file response's header
		_, _ = w.Write([]byte(`{"file":{"downloadLink":"` + srv.URL + `/redirection"}}`))
	})
	mux.HandleFunc("/redirection", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Disposition", `attachment; filename="First Ember.epub"`)
		_, _ = w.Write([]byte("zlib-epub-bytes"))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func domains(url string) func() []string { return func() []string { return []string{url} } }

func TestLoginAndTest(t *testing.T) {
	srv := fakeZlib(t)
	c := New(domains(srv.URL), "reader@example.com", "hunter2")
	left, limit, err := c.Test(context.Background())
	if err != nil || left != 7 || limit != 10 {
		t.Fatalf("Test: %v left=%d limit=%d", err, left, limit)
	}

	bad := New(domains(srv.URL), "reader@example.com", "wrong")
	if _, _, err := bad.Test(context.Background()); err == nil {
		t.Fatal("bad password should error")
	}
}

func TestSearchAndDownload(t *testing.T) {
	srv := fakeZlib(t)
	c := New(domains(srv.URL), "reader@example.com", "hunter2")

	rels, err := c.Search(context.Background(), "first ember")
	if err != nil || len(rels) != 1 {
		t.Fatalf("Search: %v %+v", err, rels)
	}
	r := rels[0]
	if r.Source != "zlibrary" || r.Format != "epub" || r.DownloadURL != "zlib:999/deadbeef" {
		t.Fatalf("release = %+v", r)
	}

	dir := t.TempDir()
	path, err := c.Download(context.Background(), r.DownloadURL, dir, r.Format)
	if err != nil {
		t.Fatalf("Download: %v", err)
	}
	body, _ := os.ReadFile(path)
	if string(body) != "zlib-epub-bytes" {
		t.Errorf("downloaded = %q", body)
	}
	// the saved file must take its name from Content-Disposition, not the
	// meaningless "/redirection" URL path
	if base := filepath.Base(path); base != "First Ember.epub" {
		t.Errorf("downloaded filename = %q, want %q", base, "First Ember.epub")
	}
}

func TestConfigured(t *testing.T) {
	if New(domains("x"), "", "").Configured() {
		t.Error("no creds should be unconfigured")
	}
	if !New(domains("x"), "a@b.c", "pw").Configured() {
		t.Error("creds should be configured")
	}
}

// TestCookieWall mimics DiamWall in front of the mirrors: a 307 to the same
// URL that sets a cookie, then a block page whose script sets dwid. Without
// a jar the client loops ("stopped after 10 redirects").
func TestCookieWall(t *testing.T) {
	inner := fakeZlib(t)
	wall := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, _ := r.Cookie("__diamwall"); c == nil {
			http.SetCookie(w, &http.Cookie{Name: "__diamwall", Value: "0x1", Path: "/"}) //nolint:gosec // test server
			http.Redirect(w, r, r.URL.String(), http.StatusTemporaryRedirect)            //nolint:gosec // test server redirects to itself
			return
		}
		if c, _ := r.Cookie("dwid"); c == nil || c.Value != "a3410d96" {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(517)
			_, _ = w.Write([]byte(`<html><script>document.cookie="dwid=a3410d96; path=/";location.reload()</script></html>`))
			return
		}
		r.Host = ""
		proxyURL := inner.URL + r.URL.RequestURI()
		req, _ := http.NewRequest(r.Method, proxyURL, r.Body) //nolint:gosec // test proxy to a local httptest server
		req.Header = r.Header.Clone()
		res, err := http.DefaultClient.Do(req) //nolint:gosec // test proxy to a local httptest server
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		w.WriteHeader(res.StatusCode)
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(wall.Close)

	c := New(domains(wall.URL), "reader@example.com", "hunter2")
	left, limit, err := c.Test(context.Background())
	if err != nil || left != 7 || limit != 10 {
		t.Fatalf("Test through wall: %v left=%d limit=%d", err, left, limit)
	}
}

func TestBrowserUserAgent(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.UserAgent()
		_, _ = w.Write([]byte(`{"error":"Incorrect email or password"}`))
	}))
	t.Cleanup(srv.Close)
	_, _, _ = New(domains(srv.URL), "a@b.c", "pw").Test(context.Background())
	if got != browserUA {
		t.Fatalf("User-Agent = %q, want browser UA", got)
	}
}
