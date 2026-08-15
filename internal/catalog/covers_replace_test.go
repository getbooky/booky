package catalog

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// imageServer serves changing bytes so a re-fetch is observable.
func imageServer(t *testing.T, body *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if *body == "" {
			http.Error(w, "gone", http.StatusNotFound)
			return
		}
		w.Write([]byte(*body)) //nolint:errcheck // test server
	}))
	t.Cleanup(srv.Close)
	return srv
}

func readCover(t *testing.T, c *CoverCache, id int64) string {
	t.Helper()
	data, err := os.ReadFile(c.Path(id))
	if err != nil {
		t.Fatalf("read cover: %v", err)
	}
	return string(data)
}

// Ensure keeps what's cached (that's the point — browsing must not re-fetch),
// while Replace takes the provider's current art. "Refresh metadata" leans on
// Replace, which is why a cover changed at the source now arrives.
func TestEnsureKeepsCachedCoverButReplaceUpdatesIt(t *testing.T) {
	body := "first-image"
	srv := imageServer(t, &body)
	c := NewCoverCache(filepath.Join(t.TempDir(), "covers"))

	if err := c.Ensure(context.Background(), 7, srv.URL); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := readCover(t, c, 7); got != "first-image" {
		t.Fatalf("cached %q, want the downloaded bytes", got)
	}

	body = "second-image"
	if err := c.Ensure(context.Background(), 7, srv.URL); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if got := readCover(t, c, 7); got != "first-image" {
		t.Errorf("Ensure re-downloaded a cached cover: %q", got)
	}
	if err := c.Replace(context.Background(), 7, srv.URL); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	if got := readCover(t, c, 7); got != "second-image" {
		t.Errorf("Replace kept the stale cover: %q", got)
	}
}

// A failed re-fetch must leave the existing cover alone. ForceURL used to
// delete first, so a typo'd URL in Edit metadata wiped the cover it was
// meant to replace.
func TestReplaceKeepsOldCoverWhenFetchFails(t *testing.T) {
	body := "good-image"
	srv := imageServer(t, &body)
	c := NewCoverCache(filepath.Join(t.TempDir(), "covers"))
	if err := c.Ensure(context.Background(), 3, srv.URL); err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	body = "" // the source now 404s
	if err := c.ForceURL(context.Background(), 3, srv.URL); err == nil {
		t.Error("a 404 source should report an error")
	}
	if c.Path(3) == "" {
		t.Fatal("the old cover was deleted by a failed replace")
	}
	if got := readCover(t, c, 3); got != "good-image" {
		t.Errorf("cover = %q, want the original to survive", got)
	}

	if err := c.ForceURL(context.Background(), 3, "not-a-url"); err == nil {
		t.Error("a malformed url should report an error")
	}
	if c.Path(3) == "" {
		t.Error("a rejected url must not delete the cached cover")
	}
}

func TestReplaceIgnoresEmptyURL(t *testing.T) {
	body := "keep-me"
	srv := imageServer(t, &body)
	c := NewCoverCache(filepath.Join(t.TempDir(), "covers"))
	if err := c.Ensure(context.Background(), 1, srv.URL); err != nil {
		t.Fatal(err)
	}
	if err := c.Replace(context.Background(), 1, ""); err != nil {
		t.Fatalf("Replace with no url: %v", err)
	}
	if got := readCover(t, c, 1); got != "keep-me" {
		t.Errorf("a provider that returned no cover url must not clear the cover: %q", got)
	}
}
