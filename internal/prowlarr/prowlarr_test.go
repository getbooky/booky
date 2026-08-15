package prowlarr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("X-Api-Key") != "good-key" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc("/api/v1/system/status", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"1.21.2"}`))
	}))
	mux.HandleFunc("/api/v1/indexer", auth(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"id":1,"name":"NZBgeek","enable":true,"protocol":"usenet","priority":25},
			{"id":2,"name":"MyAnonamouse","enable":false,"protocol":"torrent","priority":30}]`))
	}))
	mux.HandleFunc("/api/v1/search", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") != "first ember" {
			t.Errorf("unexpected query %q", r.URL.Query().Get("query"))
		}
		_, _ = w.Write([]byte(`[
			{"title":"First Ember Retail EPUB","size":1200000,"downloadUrl":"http://x/nzb/1","indexer":"NZBgeek","protocol":"usenet"}]`))
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestTestAndAuth(t *testing.T) {
	srv := testServer(t)
	c := New(srv.URL, "good-key")
	st, err := c.Test(context.Background())
	if err != nil || st.Version != "1.21.2" {
		t.Fatalf("Test: %v %+v", err, st)
	}
	bad := New(srv.URL, "wrong")
	if _, err := bad.Test(context.Background()); err == nil {
		t.Fatal("bad key should error")
	}
}

func TestIndexersAndSearch(t *testing.T) {
	srv := testServer(t)
	c := New(srv.URL, "good-key")

	idx, err := c.Indexers(context.Background())
	if err != nil || len(idx) != 2 || idx[0].Name != "NZBgeek" {
		t.Fatalf("Indexers: %v %+v", err, idx)
	}

	rels, err := c.Search(context.Background(), "first ember")
	if err != nil || len(rels) != 1 {
		t.Fatalf("Search: %v %+v", err, rels)
	}
	r := rels[0]
	if r.Source != "prowlarr:NZBgeek" || r.Protocol != "usenet" || r.DownloadURL != "http://x/nzb/1" {
		t.Errorf("release = %+v", r)
	}
}

func TestConfigured(t *testing.T) {
	if New("", "").Configured() || !New("http://x", "k").Configured() {
		t.Error("Configured misreports")
	}
}
