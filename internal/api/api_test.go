package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/getbooky/booky/internal/acquire"
	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/backup"
	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/importer"
	"github.com/getbooky/booky/internal/kindle"
	"github.com/getbooky/booky/internal/koreader"
	"github.com/getbooky/booky/internal/metadata"
	"github.com/getbooky/booky/internal/opds"
	"github.com/getbooky/booky/internal/secrets"
	"github.com/getbooky/booky/internal/settings"
	"github.com/getbooky/booky/internal/watcher"
)

type stubProvider struct {
	results []metadata.BookMeta
	works   []metadata.BookMeta
}

func (s *stubProvider) Key() string { return "stub" }
func (s *stubProvider) Search(ctx context.Context, p metadata.SearchParams) ([]metadata.BookMeta, error) {
	return s.results, nil
}

func (s *stubProvider) AuthorWorks(ctx context.Context, name string, limit int) ([]metadata.BookMeta, error) {
	return s.works, nil
}

func testKeeper(t *testing.T) *secrets.Keeper {
	t.Helper()
	k, err := secrets.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func testServer(t *testing.T) *Server {
	t.Helper()
	configDir := t.TempDir()
	conn, err := db.Open(filepath.Join(configDir, "booky.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })

	cat := catalog.New(conn)
	cfg := settings.New(conn)
	chain := metadata.NewChain(func() []string { return []string{"stub"} },
		&stubProvider{
			results: []metadata.BookMeta{{
				Provider: "stub", Title: "Burrow", Authors: []string{"Emmett Hale"},
				ISBN13: "9781476735115", GoodreadsID: "13453029",
			}},
			works: []metadata.BookMeta{
				{Provider: "stub", Title: "Burrow", Authors: []string{"Emmett Hale"}, ISBN13: "9781476735115"},
				{Provider: "stub", Title: "Drift", Authors: []string{"Emmett Hale"}},
				{Provider: "stub", Title: "Haze", Authors: []string{"Emmett Hale"}},
				{Provider: "stub", Title: "Vault Box Set 1-3", Authors: []string{"Emmett Hale"}},
				{Provider: "stub", Title: "Burrow Large Print", Authors: []string{"Emmett Hale"}},
			},
		})
	chain.Exclude = cfg.ExcludePatterns
	imp := importer.New(conn, cat, chain)
	covers := catalog.NewCoverCache(t.TempDir())
	engine := acquire.New(conn, cat, cfg, imp, covers)
	hc := metadata.NewHardcover(func() string { return cfg.Get("hardcover_token") })
	srv, err := New(Deps{
		DB: conn, Version: "test",
		Dist:     fstest.MapFS{"dist/index.html": {Data: []byte("<html>booky</html>")}},
		Catalog:  cat,
		Chain:    chain,
		Importer: imp,
		Covers:   covers,
		Settings: cfg,
		Acquire:  engine,
		Watcher:  watcher.New(conn, cat, chain, cfg, engine, covers, hc),
		Auth:     auth.New(conn),
		KoReader: koreader.New(conn, testKeeper(t)),
		Kindle:   kindle.New(conn, testKeeper(t)),
		Backups:  backup.New(conn, configDir),
		OPDS:     opds.New(conn, cat, covers),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv
}

func doJSON(t *testing.T, h http.Handler, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	out := map[string]any{}
	json.Unmarshal(rec.Body.Bytes(), &out) //nolint:errcheck,gosec // some responses aren't objects
	return rec, out
}

func TestSearchEndpoint(t *testing.T) {
	h := testServer(t).Handler()
	rec, out := doJSON(t, h, "GET", "/api/v1/search?q=burrow", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	results := out["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("results = %v", out)
	}
}

// The client echoes a whole search result — including the inLibrary/monitored
// annotations the search endpoint adds — back to the add endpoint. The strict
// JSON decoder must tolerate those extra fields, not 400 with "unknown field".
func TestAddBookAcceptsAnnotatedSearchResult(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	rec, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/books/alex"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library: %d %s", rec.Code, rec.Body)
	}
	libID := int64(out["id"].(float64))

	rec, out = doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"meta": map[string]any{
			"provider": "goodreads", "title": "Burrow", "authors": []string{"Emmett Hale"},
			"inLibrary": false, "monitored": true, // annotations the client round-trips
		},
		"libraryId": libID,
		"monitored": true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add book with annotated meta: %d %s", rec.Code, rec.Body)
	}
	if out["title"] != "Burrow" {
		t.Fatalf("book = %v", out)
	}
}

func TestAddBookAndListFlow(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	rec, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/books/alex"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library: %d %s", rec.Code, rec.Body)
	}
	libID := int64(out["id"].(float64))

	// partial meta from a different source — the chain fills the gaps
	rec, out = doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "manual", "title": "Burrow", "authors": []string{"Emmett Hale"}, "isbn13": "9781476735115"},
		"libraryId": libID,
		"monitored": true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add book: %d %s", rec.Code, rec.Body)
	}
	if out["title"] != "Burrow" {
		t.Fatalf("book = %v", out)
	}
	if out["goodreadsId"] != "13453029" {
		t.Errorf("enrich should fill goodreads id via chain, got %v", out["goodreadsId"])
	}

	rec, out = doJSON(t, h, "GET", "/api/v1/books?libraryId=1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list books: %d", rec.Code)
	}
	if books := out["books"].([]any); len(books) != 1 {
		t.Fatalf("books = %v", out)
	}

	rec, out = doJSON(t, h, "GET", "/api/v1/authors", nil)
	if authors := out["authors"].([]any); rec.Code != http.StatusOK || len(authors) != 1 {
		t.Fatalf("authors = %v", out)
	}
}

func TestSettingsRoundTripAndSecretMasking(t *testing.T) {
	h := testServer(t).Handler()

	rec, _ := doJSON(t, h, "PUT", "/api/v1/settings/provider_order", map[string]string{"value": "hardcover,goodreads"})
	if rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body)
	}
	_, out := doJSON(t, h, "GET", "/api/v1/settings/provider_order", nil)
	if out["value"] != "hardcover,goodreads" {
		t.Fatalf("get = %v", out)
	}

	doJSON(t, h, "PUT", "/api/v1/settings/hardcover_token", map[string]string{"value": "secret-token"})
	_, out = doJSON(t, h, "GET", "/api/v1/settings/hardcover_token", nil)
	if out["value"] == "secret-token" {
		t.Error("secret must never be echoed back")
	}

	rec, _ = doJSON(t, h, "PUT", "/api/v1/settings/not_a_key", map[string]string{"value": "x"})
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown key should 404, got %d", rec.Code)
	}
}

func TestSPAFallback(t *testing.T) {
	h := testServer(t).Handler()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/library/some/route", nil))
	if rec.Code != http.StatusOK || !bytes.Contains(rec.Body.Bytes(), []byte("booky")) {
		t.Fatalf("spa fallback: %d %s", rec.Code, rec.Body)
	}
}
