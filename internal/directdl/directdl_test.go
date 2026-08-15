package directdl

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeAnnas stands in for an Anna's Archive mirror: the fast_download API
// answers per memberOK, and the slow-download page serves a partner link
// unless gated is set.
func fakeAnnas(t *testing.T, memberOK, gated bool) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, s string) {
		if _, err := io.WriteString(w, s); err != nil {
			t.Error(err)
		}
	}
	mux.HandleFunc("/dyn/api/fast_download.json", func(w http.ResponseWriter, r *http.Request) {
		if memberOK {
			write(w, `{"download_url":"http://members-only/file/good.epub"}`)
			return
		}
		// what Anna's returns for a valid key with no active membership
		write(w, `{"error":"This key is not associated with a membership"}`)
	})
	mux.HandleFunc("/slow_download/", func(w http.ResponseWriter, r *http.Request) {
		if gated {
			// partners serve the verification wall with a 403, not a 200 — the
			// challenge must still be detected through the status error
			w.WriteHeader(http.StatusForbidden)
			write(w, `<div>Checking your browser before you continue</div>`)
			return
		}
		write(w, `<a href="https://partner.example/dl/free.epub">Download now</a>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func clientFor(srv *httptest.Server, key string) *Client {
	return New(Mirrors{Annas: func() []string { return []string{srv.URL} }},
		func() string { return key })
}

// A valid membership: the fast API's direct link is used, slow path untouched.
func TestResolveMemberUsesFastLink(t *testing.T) {
	c := clientFor(fakeAnnas(t, true, false), "good-key")
	got, err := c.resolve(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasSuffix(got, "/file/good.epub") {
		t.Fatalf("expected the member fast link, got %q", got)
	}
}

// The case that matters here: a key with no membership. The fast API errors,
// and resolve must fall through to the free slow path instead of failing.
func TestResolveFallsBackWhenNotAMember(t *testing.T) {
	c := clientFor(fakeAnnas(t, false, false), "key-without-membership")
	got, err := c.resolve(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("non-member key should fall back to slow path, got error: %v", err)
	}
	if got != "https://partner.example/dl/free.epub" {
		t.Fatalf("expected the slow-path partner link, got %q", got)
	}
}

// No key at all behaves exactly like before: straight to the slow path.
func TestResolveNoKeyUsesSlow(t *testing.T) {
	c := clientFor(fakeAnnas(t, false, false), "")
	got, err := c.resolve(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got != "https://partner.example/dl/free.epub" {
		t.Fatalf("got %q", got)
	}
}

// Fallback still surfaces a real failure: non-member key AND a gated slow
// path means nothing is downloadable, and resolve reports it. The error
// must reflect the verification wall (proving challenge detection ran), not
// a generic parse miss.
func TestResolveFallbackStillFailsWhenGated(t *testing.T) {
	c := clientFor(fakeAnnas(t, false, true), "key-without-membership")
	_, err := c.resolve(context.Background(), "abc123")
	if err == nil {
		t.Fatal("expected an error when the key is non-member and slow path is gated")
	}
	if !strings.Contains(err.Error(), "verification wall") {
		t.Fatalf("gated error should name the verification wall, got %q", err)
	}
}

// annasListHTML is the default (list) search view: one anchor per record
// wrapping cover + title <h3> + author. The second record sits inside an
// HTML comment, the way Anna's lazy-loads results past the first ~10.
const annasListHTML = `
<div class="h-[110px]">
  <a href="/md5/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" class="js-vim-focus custom-a">
    <div class="flex-none"><div class="w-[72px] h-[100px]"><img src="cover1.jpg"/></div></div>
    <div class="grow">
      <div class="text-gray-500">English [en], epub, 1.2MB</div>
      <h3 class="font-bold">Iron Dawn</h3>
      <div>Marrowbone Press, 2014</div>
      <div class="italic">Cole Merrick</div>
    </div>
  </a>
</div>
<div class="h-[110px] js-scroll-hidden">
  <!--
  <a href="/md5/bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" class="js-vim-focus custom-a">
    <div class="grow"><h3 class="font-bold">Iron Crown</h3><div class="italic">Cole Merrick</div></div>
  </a>
  -->
</div>`

// annasTableHTML is the table view: many anchors to the same md5 per row, the
// first (cover cell) carrying no text, the title in a later cell.
const annasTableHTML = `
<table><tr class="group">
  <td class="w-[22px]"><a href="/md5/cccccccccccccccccccccccccccccccc"><span><img src="c.jpg"/></span></a></td>
  <td class="w-[28%]"><a href="/md5/cccccccccccccccccccccccccccccccc"><span>Iron Veil</span></a></td>
  <td class="w-[28%]"><a href="/md5/cccccccccccccccccccccccccccccccc"><span>Cole Merrick</span></a></td>
  <td><a href="/md5/cccccccccccccccccccccccccccccccc"><span>epub</span></a></td>
</tr></table>`

// annasRealListHTML mirrors the current live markup: the md5 anchor wraps only
// the cover + title, and the format/size metadata line sits in a SIBLING div
// after the anchor closes ("English [en] · AZW3 · 4.8MB · …"). This is the
// shape the anchor-only format scan missed — every result came back format-"".
const annasRealListHTML = `
<div class="js-aarecord-list-outer">
  <a href="/md5/dddddddddddddddddddddddddddddddd" class="custom-a block"><img src="d.jpg"/></a>
  <div class="grow">
    <a href="/md5/dddddddddddddddddddddddddddddddd" class="line-clamp-[3] custom-a"><h3>The Silent Comet</h3></a>
    <div class="text-gray-500 text-xs">English [en] · AZW3 · 4.8MB · 2021 · &#128213;&nbsp;Book (fiction)</div>
  </div>
</div>
<div class="js-aarecord-list-outer">
  <a href="/md5/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" class="custom-a block"><img src="e.jpg"/></a>
  <div class="grow">
    <a href="/md5/eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee" class="line-clamp-[3] custom-a"><h3>The Silent Comet: A Novel</h3></a>
    <div class="text-gray-500 text-xs">English [en] · EPUB · 1.2MB · 2021 · &#128213;&nbsp;Book (fiction)</div>
  </div>
</div>`

func annasSearchServer(t *testing.T, html string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := io.WriteString(w, html); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestSearchListViewParsesTitles(t *testing.T) {
	c := clientFor(annasSearchServer(t, annasListHTML), "")
	got, err := c.Search(context.Background(), "iron dawn")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Title, "Iron Dawn") || got[0].Title == "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("first title not parsed: %q", got[0].Title)
	}
	if got[0].DownloadURL != "md5:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Errorf("download token wrong: %q", got[0].DownloadURL)
	}
	if got[0].Format != "epub" {
		t.Errorf("format not detected: %q", got[0].Format)
	}
	// the commented-out (lazy-loaded) record must still be found and titled
	if !strings.Contains(got[1].Title, "Iron Crown") {
		t.Errorf("lazy-loaded title not parsed: %q", got[1].Title)
	}
}

// Regression: on the real markup the format lives OUTSIDE the md5 anchor, in a
// sibling metadata div. The record-block scan must find it per result.
func TestSearchDetectsFormatFromSiblingMetadata(t *testing.T) {
	c := clientFor(annasSearchServer(t, annasRealListHTML), "")
	got, err := c.Search(context.Background(), "the silent comet")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 results, got %d: %+v", len(got), got)
	}
	if got[0].Title != "The Silent Comet" || got[0].Format != "azw3" {
		t.Errorf("record 0: title=%q format=%q, want The Silent Comet / azw3", got[0].Title, got[0].Format)
	}
	if got[1].Format != "epub" {
		t.Errorf("record 1: format=%q, want epub (must not bleed from record 0)", got[1].Format)
	}
}

func TestSearchTableViewParsesTitles(t *testing.T) {
	c := clientFor(annasSearchServer(t, annasTableHTML), "")
	got, err := c.Search(context.Background(), "iron veil")
	if err != nil {
		t.Fatal(err)
	}
	// the dozen anchors to one md5 must collapse to a single result whose
	// title comes from the text cells, never the empty cover anchor
	if len(got) != 1 {
		t.Fatalf("table rows should dedupe to 1 result, got %d: %+v", len(got), got)
	}
	if !strings.Contains(got[0].Title, "Iron Veil") {
		t.Errorf("table title not parsed (cover-anchor emptiness leaked?): %q", got[0].Title)
	}
}

func TestSearchRealDownloadTokenResolves(t *testing.T) {
	// end-to-end shape: a search token feeds Download, which resolves the
	// slow path — proving the md5 token the parser emits is grab-ready.
	dl := fakeAnnas(t, false, false)
	c := clientFor(dl, "")
	url, err := c.resolve(context.Background(), "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	if url != "https://partner.example/dl/free.epub" {
		t.Fatalf("got %q", url)
	}
}

// slowPage builds a slow-download page: a waitlist countdown until it has been
// hit hitsBeforeLink times, then the real d3-style partner link.
func annasWaitlistServer(t *testing.T, hitsBeforeLink int) *httptest.Server {
	t.Helper()
	var mu struct {
		hits int
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.hits++
		if mu.hits <= hitsBeforeLink {
			_, _ = io.WriteString(w, `<script>let waitSeconds = 1;</script>`)
			return
		}
		_, _ = io.WriteString(w,
			`<a href="https://partner.example/d3/y/1700000000/10000/somepath~/abc/book.epub">Download now</a>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestResolveSlowWaitsOutCountdown(t *testing.T) {
	// slot 0 shows a 1s countdown once, then serves the d3 link on re-fetch
	c := clientFor(annasWaitlistServer(t, 3), "") // 3 slots gated first pass, then link
	start := time.Now()
	got, err := c.resolve(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("waitlist should resolve after sleeping: %v", err)
	}
	if !strings.Contains(got, "/d3/y/") || !strings.HasSuffix(got, "book.epub") {
		t.Fatalf("expected the d3 partner link, got %q", got)
	}
	if time.Since(start) < 900*time.Millisecond {
		t.Error("resolve returned before honoring the ~1s countdown")
	}
}

func TestResolveSlowD3LinkExtracted(t *testing.T) {
	// a slot that serves the modern d3 URL (no ebook extension in the path
	// component) must still be recognized by the structural matcher
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w,
			`<div><a href="https://partner.example/d3/y/1700000000/10000/aa%2Fbb~/deadbeef/My Book.epub">Download now</a></div>`)
	}))
	t.Cleanup(srv.Close)
	c := clientFor(srv, "")
	got, err := c.resolve(context.Background(), "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/d3/y/1700000000/") {
		t.Fatalf("d3 link not extracted: %q", got)
	}
}

// A cancelled context during the member request must abort — not silently
// downgrade to the slow path and then report a misleading parse error.
func TestResolveDoesNotSwallowCancellation(t *testing.T) {
	// a mirror that blocks until the request's context is cancelled
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)
	c := clientFor(srv, "some-key")

	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := c.resolve(ctx, "abc123")
	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if !strings.Contains(err.Error(), "no slow-download link") {
		return // good: it surfaced the cancellation, not a slow-path parse miss
	}
	t.Fatalf("cancellation was masked as a slow-path failure: %q", err)
}

func TestParseSizeBytes(t *testing.T) {
	cases := map[string]int64{
		"English [en] · AZW3 · 4.8MB · The Silent Comet": 5033164, // 4.8 * 1<<20, truncated
		"EPUB · 739.2 kB":       756940,     // 739.2 * 1<<10
		"PDF · 1.1GB scanned":   1181116006, // 1.1 * 1<<30
		"no size in this block": 0,
	}
	for block, want := range cases {
		if got := parseSizeBytes(block); got != want {
			t.Errorf("parseSizeBytes(%q) = %d, want %d", block, got, want)
		}
	}
}
