package directdl

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// Live integration tests against real Anna's Archive mirrors. Search is
// public, so most of this runs with no secrets. Opt in with:
//
//	ANNAS_LIVE=1 go test ./internal/directdl/ -run TestAnnasLive -v
//
// Overrides (all optional):
//   ANNAS_MIRRORS  comma/newline mirror list (default: the built-in .gl/.pk/.gd)
//   ANNAS_KEY      member key — exercises the fast_download.json path. Read
//                  only from the environment; never commit it or write it to disk.
//   ANNAS_QUERY    search query (default: "project hail mary")
//
// The download tests are best-effort: the anonymous slow path can sit behind a
// partner verification wall we can't clear headlessly, so a gated result SKIPS
// rather than fails — that's an environment condition, not a code defect.

const liveDefaultMirrors = "https://annas-archive.gl\nhttps://annas-archive.pk\nhttps://annas-archive.gd"

func liveAnnas(t *testing.T) *Client {
	t.Helper()
	if os.Getenv("ANNAS_LIVE") == "" {
		t.Skip("ANNAS_LIVE not set — skipping live Anna's Archive test")
	}
	mirrorsCfg := os.Getenv("ANNAS_MIRRORS")
	if mirrorsCfg == "" {
		mirrorsCfg = liveDefaultMirrors
	}
	mirrors := SplitMirrors(mirrorsCfg)
	key := os.Getenv("ANNAS_KEY") // "" = anonymous slow path
	return New(Mirrors{Annas: func() []string { return mirrors }}, func() string { return key })
}

func liveQuery() string {
	if q := os.Getenv("ANNAS_QUERY"); q != "" {
		return q
	}
	return "project hail mary"
}

func TestAnnasLiveSearch(t *testing.T) {
	c := liveAnnas(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	rels, err := c.Search(ctx, liveQuery())
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(rels) == 0 {
		t.Fatal("search returned nothing for a well-known title")
	}

	formats := map[string]int{}
	emptyTitles := 0
	for _, r := range rels {
		if r.Source != "annas" || r.Protocol != "direct" {
			t.Errorf("unexpected source/protocol: %+v", r)
		}
		if !strings.HasPrefix(r.DownloadURL, "md5:") || len(r.DownloadURL) != len("md5:")+32 {
			t.Errorf("malformed download token: %q", r.DownloadURL)
		}
		if strings.TrimSpace(r.Title) == "" || strings.HasPrefix(r.Title, "md5:") {
			emptyTitles++
		}
		formats[r.Format]++
	}
	// The title-extraction logic is the fragile part (Anna's flips between list
	// and table markup); most results must carry a real, non-md5 title.
	if emptyTitles > len(rels)/2 {
		t.Errorf("%d/%d results had no parseable title — markup drifted?", emptyTitles, len(rels))
	}
	t.Logf("%d results; formats: %v", len(rels), formats)
	for i, r := range rels {
		if i >= 5 {
			break
		}
		t.Logf("  %q [%s] %s", r.Title, r.Format, r.DownloadURL)
	}
}

// TestAnnasLiveResolve resolves a real search hit to a download URL. With a
// member key it exercises fast_download.json; without one it walks the free
// slow path and SKIPS if the partners are gated.
func TestAnnasLiveResolve(t *testing.T) {
	c := liveAnnas(t)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	rels, err := c.Search(ctx, liveQuery())
	if err != nil || len(rels) == 0 {
		t.Fatalf("search precondition failed: %v (%d results)", err, len(rels))
	}
	md5 := strings.TrimPrefix(rels[0].DownloadURL, "md5:")

	fileURL, err := c.resolve(ctx, md5)
	if err != nil {
		if c.HasMemberKey() {
			t.Fatalf("member resolve failed with a key set: %v", err)
		}
		// anonymous: a verification wall or gated partners is expected sometimes
		if strings.Contains(err.Error(), "verification wall") {
			t.Skipf("free partners gated right now: %v", err)
		}
		t.Skipf("anonymous slow path did not yield a link: %v", err)
	}
	if !strings.HasPrefix(fileURL, "http") {
		t.Errorf("resolved link is not a URL: %q", fileURL)
	}
	via := "slow path"
	if c.HasMemberKey() {
		via = "member fast_download"
	}
	t.Logf("resolved %s via %s -> %s", md5, via, fileURL)
}
