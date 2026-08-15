package zlibrary

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/getbooky/booky/internal/release"
)

// Live integration tests against a real Z-Library account. Z-Library's eapi
// requires a logged-in session, so these need real credentials — supplied
// ONLY through the environment, never committed or written to disk:
//
//	ZLIB_EMAIL=… ZLIB_PASSWORD=… go test ./internal/zlibrary/ -run TestZlibLive -v
//
// Z-Library's public domains rotate constantly and are frequently geoblocked
// or 503 from datacenter IPs. Override the domain list with a currently-working
// one when the defaults are down:
//
//	ZLIB_DOMAINS="https://your-working-mirror" ZLIB_EMAIL=… ZLIB_PASSWORD=… go test …
//
// A domain that can't be reached at all SKIPS (environment condition); only a
// server that answers and rejects the login FAILS.

func liveZlib(t *testing.T) *Client {
	t.Helper()
	email := os.Getenv("ZLIB_EMAIL")
	password := os.Getenv("ZLIB_PASSWORD")
	if email == "" || password == "" {
		t.Skip("ZLIB_EMAIL / ZLIB_PASSWORD not set — skipping live Z-Library test")
	}
	domainsCfg := os.Getenv("ZLIB_DOMAINS")
	if domainsCfg == "" {
		domainsCfg = "https://z-lib.sk\nhttps://z-library.sk\nhttps://1lib.sk"
	}
	var domains []string
	for _, d := range strings.FieldsFunc(domainsCfg, func(r rune) bool { return r == '\n' || r == ',' }) {
		if d = strings.TrimRight(strings.TrimSpace(d), "/"); d != "" {
			domains = append(domains, d)
		}
	}
	return New(func() []string { return domains }, email, password)
}

// looksUnreachable distinguishes "the mirror is down/blocked from here" (skip)
// from "the server answered and rejected us" (fail).
func looksUnreachable(err error) bool {
	s := strings.ToLower(err.Error())
	for _, m := range []string{
		"no z-library domains", "no such host", "timeout", "deadline exceeded",
		"connection refused", "connection reset", "eof", "503", "502",
		"no route to host", "tls", "handshake", "bad login response", "bad profile response",
		// Z-Library's own server-side transients (search backend overloaded)
		"temporary unavailable", "temporarily unavailable", "service unavailable",
	} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func TestZlibLiveLoginAndQuota(t *testing.T) {
	c := liveZlib(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	left, limit, err := c.Test(ctx)
	if err != nil {
		if looksUnreachable(err) {
			t.Skipf("Z-Library unreachable from here: %v", err)
		}
		t.Fatalf("login/profile failed (credentials?): %v", err)
	}
	if limit <= 0 {
		t.Errorf("download limit reported as %d — profile shape drifted?", limit)
	}
	if left < 0 || left > limit {
		t.Errorf("downloads-left %d out of range for limit %d", left, limit)
	}
	t.Logf("logged in; %d of %d daily downloads left", left, limit)
}

func TestZlibLiveSearch(t *testing.T) {
	c := liveZlib(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	rels, err := c.Search(ctx, "project hail mary")
	if err != nil {
		if looksUnreachable(err) {
			t.Skipf("Z-Library unreachable from here: %v", err)
		}
		t.Fatalf("search failed: %v", err)
	}
	if len(rels) == 0 {
		t.Fatal("search returned nothing for a well-known title")
	}
	for _, r := range rels {
		if r.Source != "zlibrary" || r.Protocol != "direct" {
			t.Errorf("unexpected source/protocol: %+v", r)
		}
		if !strings.HasPrefix(r.DownloadURL, "zlib:") || !strings.Contains(r.DownloadURL, "/") {
			t.Errorf("malformed download token: %q", r.DownloadURL)
		}
	}
	var sample release.Release
	for _, r := range rels {
		if strings.Contains(strings.ToLower(r.Title), "hail mary") {
			sample = r
			break
		}
	}
	t.Logf("%d results; sample: %q [%s] %s", len(rels), sample.Title, sample.Format, sample.DownloadURL)
}

// TestZlibLiveDownload actually spends one of the account's daily downloads,
// so it is gated behind an extra explicit opt-in (ZLIB_LIVE_DOWNLOAD=1) on top
// of the credentials.
func TestZlibLiveDownload(t *testing.T) {
	if os.Getenv("ZLIB_LIVE_DOWNLOAD") == "" {
		t.Skip("ZLIB_LIVE_DOWNLOAD not set — skipping (this spends a daily download)")
	}
	c := liveZlib(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	rels, err := c.Search(ctx, "project hail mary")
	if err != nil {
		if looksUnreachable(err) {
			t.Skipf("Z-Library unreachable from here: %v", err)
		}
		t.Fatalf("search precondition failed: %v", err)
	}
	if len(rels) == 0 {
		t.Fatal("no results to download")
	}
	dir := t.TempDir()
	path, err := c.Download(ctx, rels[0].DownloadURL, dir, rels[0].Format)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "limit") {
			t.Skipf("daily download limit reached: %v", err)
		}
		t.Fatalf("download failed: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("downloaded file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Error("downloaded file is empty")
	}
	t.Logf("downloaded %q (%d bytes)", path, info.Size())
}
