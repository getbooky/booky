// Package directdl is the Anna's Archive provider. Search is public; the
// download path has two modes, matching how the Anna's KoReader plugins
// behave:
//
//   - member key set  → the fast_download.json API returns a guaranteed
//     direct link (no timer, no verification). This is the reliable path.
//   - no key          → the free "slow download" partner-server path, which
//     usually serves the file after a short wait but occasionally sits behind
//     a browser check we can't clear headlessly. Best-effort: if a partner
//     server throws a challenge, the grab fails cleanly and the next-ranked
//     candidate is tried.
//
// Every host is a user-editable mirror list — these domains move often.
package directdl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/release"
)

// Mirrors supplies the current Anna's Archive domains (first working wins).
type Mirrors struct {
	Annas func() []string // e.g. https://annas-archive.org
}

type Client struct {
	mirrors   Mirrors
	memberKey func() string // Anna's secret key; "" = anonymous slow path
	http      *http.Client
	ua        string
}

func New(m Mirrors, memberKey func() string) *Client {
	if memberKey == nil {
		memberKey = func() string { return "" }
	}
	return &Client{
		mirrors:   m,
		memberKey: memberKey,
		http:      &http.Client{Timeout: 90 * time.Second},
		ua:        "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0 Safari/537.36",
	}
}

func (c *Client) Configured() bool {
	return c != nil && len(c.mirrors.Annas()) > 0
}

func (c *Client) HasMemberKey() bool { return c != nil && c.memberKey() != "" }

// SplitMirrors parses a settings value into a mirror list: one URL per line
// or comma-separated, blanks dropped, trailing slashes trimmed.
func SplitMirrors(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == '\n' || r == ',' })
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimRight(strings.TrimSpace(f), "/")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

func (c *Client) get(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	return c.http.Do(req)
}

func (c *Client) fetchText(ctx context.Context, url string) (string, error) {
	body, status, err := c.fetchTextAllowErr(ctx, url)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("%s returned %d", url, status)
	}
	return body, nil
}

// fetchTextAllowErr returns the body and status even for non-2xx responses, so
// callers can inspect an error page (e.g. a 403 that is really a verification
// challenge) instead of losing it behind a status error.
func (c *Client) fetchTextAllowErr(ctx context.Context, url string) (string, int, error) {
	res, err := c.get(ctx, url)
	if err != nil {
		return "", 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	return string(body), res.StatusCode, err
}

func firstMirror[T any](mirrors []string, fn func(base string) (T, error)) (T, error) {
	var zero T
	var lastErr = fmt.Errorf("no Anna's Archive mirrors configured")
	for _, m := range mirrors {
		if v, err := fn(m); err == nil {
			return v, nil
		} else {
			lastErr = err
		}
	}
	return zero, lastErr
}

// annasHitRe matches search-result links: /md5/<hash>, grabbing the hash and
// the inner text block. How much text one anchor carries depends on the
// display mode Anna's renders: the list view wraps a whole record — cover,
// title <h3>, publisher, author — in ONE anchor, while the table view emits a
// dozen anchors to the same path per record, the first wrapping only the
// cover image (no text at all). Taking the first anchor per hash therefore
// yields an empty title in table view, so anchors are grouped by hash and
// every anchor's text folded into the record. Results past the first ~10 sit
// inside HTML comments (uncommented by scroll JS) — a regex reads through
// those fine.
var (
	annasHitRe = regexp.MustCompile(`(?s)href="/md5/([0-9a-f]{32})"[^>]*>(.*?)</a>`)
	annasH3Re  = regexp.MustCompile(`(?s)<h3[^>]*>(.*?)</h3>`)
	tagRe      = regexp.MustCompile(`<[^>]+>`)
	spaceRe    = regexp.MustCompile(`\s+`)
)

// htmlText flattens an HTML fragment to its space-normalized visible text.
func htmlText(blob string) string {
	return strings.TrimSpace(spaceRe.ReplaceAllString(tagRe.ReplaceAllString(blob, " "), " "))
}

// anchorTitleText pulls the display title from one search-result anchor. The
// list view wraps the whole record in the anchor with the title in an <h3>
// (cover row, publisher and author are noise) — prefer that. The table view
// has no <h3>; there the anchor's flattened cell text is the title.
func anchorTitleText(blob string) string {
	if m := annasH3Re.FindStringSubmatch(blob); m != nil {
		if t := htmlText(m[1]); t != "" {
			return t
		}
	}
	return htmlText(blob)
}

// Search queries each Anna's Archive mirror. Download tokens are "md5:<hash>"
// and resolve at grab time, so search spends no quota.
func (c *Client) Search(ctx context.Context, query string) ([]release.Release, error) {
	const maxHits = 40
	return firstMirror(c.mirrors.Annas(), func(base string) ([]release.Release, error) {
		page, err := c.fetchText(ctx, base+"/search?q="+url.QueryEscape(query))
		if err != nil {
			return nil, err
		}
		seen := map[string]int{}
		var out []release.Release
		type acc struct {
			titles []string // h3-preferred anchor titles, joined for display
			start  int      // byte offset of this record's first anchor in page
		}
		var texts []*acc
		for _, m := range annasHitRe.FindAllStringSubmatchIndex(page, -1) {
			md5 := page[m[2]:m[3]]
			blob := page[m[4]:m[5]]
			idx, ok := seen[md5]
			if !ok {
				if len(out) >= maxHits {
					continue
				}
				idx = len(out)
				seen[md5] = idx
				out = append(out, release.Release{
					Source:      "annas",
					Protocol:    "direct",
					DownloadURL: "md5:" + md5,
					InfoURL:     base + "/md5/" + md5,
				})
				texts = append(texts, &acc{start: m[0]})
			}
			if t := anchorTitleText(blob); t != "" {
				texts[idx].titles = append(texts[idx].titles, t)
			}
		}
		for i := range out {
			// the first meaningful anchor text is the title: list view yields
			// one <h3>-extracted title, table view yields the title cell first
			// (the cover cell is empty and never recorded). Joining every cell
			// would fold author/format into the title and skew quality-profile
			// term matching.
			title := ""
			if len(texts[i].titles) > 0 {
				title = texts[i].titles[0]
			}
			if title == "" {
				title = strings.TrimPrefix(out[i].DownloadURL, "md5:")
			}
			out[i].Title = title
			// Format lives in the record's metadata row ("English [en] · AZW3 ·
			// 4.8MB · …"), which sits OUTSIDE the md5 anchors — so scan the whole
			// record block (this record's first anchor to the next record's)
			// rather than the anchor text alone.
			end := len(page)
			if i+1 < len(texts) {
				end = texts[i+1].start
			}
			block := htmlText(page[texts[i].start:end])
			out[i].Format = release.DetectFormat(block)
			out[i].SizeBytes = parseSizeBytes(block)
			// the metadata row tags languages as "English [en] · French [fr]"
			// pairs (translated editions carry both) — collect every pair, so
			// ranking can reject a record with ANY unwanted language. Keying
			// on the name+code pair keeps prose brackets from matching.
			out[i].Language = strings.Join(blockLanguages(block), ",")
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("no results on %s", base)
		}
		return out, nil
	})
}

// blockLangRe matches the metadata row's "Name [code]" language pairs
// ("English [en]", "Français [fr]").
var blockLangRe = regexp.MustCompile(`[A-Za-zÀ-ÿ]+ \[([a-z]{2,3})\]`)

// blockLanguages extracts every tagged language from a record block,
// canonicalized and deduped, in page order.
func blockLanguages(block string) []string {
	var out []string
	seen := map[string]bool{}
	for _, m := range blockLangRe.FindAllStringSubmatch(block, 4) {
		if l := release.NormalizeLanguage(m[1]); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// sizeRe matches the human size from the record's metadata row ("4.8MB",
// "739.2 kB", "1.1GB").
var sizeRe = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*(KB|MB|GB)\b`)

// parseSizeBytes reads the first human-readable size in the block; 0 when
// none is present.
func parseSizeBytes(block string) int64 {
	m := sizeRe.FindStringSubmatch(block)
	if m == nil {
		return 0
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0
	}
	switch strings.ToUpper(m[2]) {
	case "KB":
		n *= 1 << 10
	case "MB":
		n *= 1 << 20
	case "GB":
		n *= 1 << 30
	}
	return int64(n)
}

// Download resolves an "md5:<hash>" token and fetches the file into dir.
// Uses the member API when a key is set, otherwise the free slow path.
// wantFormat (the release's known format) backs up the filename's extension
// when the resolved link carries none.
func (c *Client) Download(ctx context.Context, token, dir, wantFormat string) (string, error) {
	md5, ok := strings.CutPrefix(token, "md5:")
	if !ok {
		return "", fmt.Errorf("annas: not an md5 token: %s", token)
	}
	fileURL, err := c.resolve(ctx, md5)
	if err != nil {
		return "", err
	}
	return c.fetchFile(ctx, fileURL, dir, wantFormat)
}

// resolve turns an md5 into a direct file URL. A member key is tried first
// (guaranteed direct link), but a key belonging to a non-member — or one
// whose daily fast-download quota is spent — makes the API return an error;
// rather than fail the grab, fall back to the same free slow path an
// unauthenticated client uses. So a key is always safe to set: it upgrades
// downloads when the membership is active and is harmless when it isn't.
func (c *Client) resolve(ctx context.Context, md5 string) (string, error) {
	if key := c.memberKey(); key != "" {
		fileURL, err := c.resolveMember(ctx, md5, key)
		if err == nil {
			return fileURL, nil
		}
		// A cancelled/timed-out request is not "not a member" — don't
		// downgrade to the slow path, which would only fire more doomed
		// requests and mask the real cause. Surface the cancellation.
		if ctx.Err() != nil {
			return "", err
		}
		log.Printf("annas: member download unavailable (%v) — falling back to the free slow path", err)
		slowURL, slowErr := c.resolveSlow(ctx, md5)
		if slowErr != nil {
			// both paths failed with a key set: report both, so the error
			// doesn't tell a member to "add a member key"
			return "", fmt.Errorf("member path: %v; slow path: %w", err, slowErr)
		}
		return slowURL, nil
	}
	return c.resolveSlow(ctx, md5)
}

// resolveMember uses the authenticated fast_download.json endpoint — the
// same one the paid Anna's KoReader plugin uses. Guaranteed direct link.
func (c *Client) resolveMember(ctx context.Context, md5, key string) (string, error) {
	return firstMirror(c.mirrors.Annas(), func(base string) (string, error) {
		u := fmt.Sprintf("%s/dyn/api/fast_download.json?md5=%s&key=%s", base, md5, url.QueryEscape(key))
		res, err := c.get(ctx, u)
		if err != nil {
			return "", err
		}
		defer res.Body.Close()
		var out struct {
			DownloadURL string `json:"download_url"`
			Error       string `json:"error"`
			Account     struct {
				DownloadsLeft int `json:"downloads_left"`
			} `json:"account_fast_download_info"`
		}
		if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&out); err != nil {
			return "", fmt.Errorf("annas: bad member response from %s", base)
		}
		if out.Error != "" {
			return "", fmt.Errorf("annas: %s", out.Error)
		}
		if out.DownloadURL == "" {
			return "", fmt.Errorf("annas: no download url (daily fast-download limit reached?)")
		}
		return out.DownloadURL, nil
	})
}

// The free slow-download page (/slow_download/<md5>/<n>/<m>) renders the
// partner link as an absolute URL of the shape
// https://<partner>/d3/y/<expiry>/<speed>/<path>~/<hash>/<filename> —
// slowPartnerRe keys on that structure (extension-agnostic, tolerant of the
// d3 prefix version drifting), with slowExtRe as a fallback for any absolute
// link carrying an ebook extension. Other absolute links on the page
// (download-manager recommendations etc.) match neither.
var (
	slowPartnerRe = regexp.MustCompile(`href="(https?://[^"]+/d\d+/[xy]/\d+/\d+/[^"]+)"`)
	slowExtRe     = regexp.MustCompile(`href="(https?://[^"]+\.(?:epub|azw3?|mobi|pdf|djvu|fb2|cbz|cbr|rtf|txt)[^"]*)"`)
	// slowWaitRe reads the waitlist countdown the page embeds as
	// `let waitSeconds = N;` when the partner time-gates free downloads.
	slowWaitRe = regexp.MustCompile(`waitSeconds\s*=\s*(\d+)`)
)

// challengeRe spots a browser/captcha verification wall we can't clear.
var challengeRe = regexp.MustCompile(`(?i)cf-challenge|cf_chl|captcha|verify you are (a )?human|checking your browser`)

// ErrPartnersGated marks the transient condition where every free partner
// slot sits behind a verification wall. It comes and goes with the partner's
// rate guard — callers must treat it as "try again later", never as a verdict
// on the release (blocklisting on it poisons every candidate tried during a
// gated window, which reads as "keyless downloads always fail" forever).
var ErrPartnersGated = errors.New("annas: free partner servers are behind a verification wall — try again later, or add a member key for reliable downloads")

func findSlowLink(page string) string {
	if m := slowPartnerRe.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	if m := slowExtRe.FindStringSubmatch(page); m != nil {
		return m[1]
	}
	return ""
}

// parseWaitSeconds extracts the waitlist countdown, if the page shows one.
func parseWaitSeconds(page string) (time.Duration, bool) {
	m := slowWaitRe.FindStringSubmatch(page)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return time.Duration(n) * time.Second, true
}

// maxSlowWait caps a single honored countdown: a normal waitlist cycle is
// ~75s (up to ~150s for heavily-throttled IPs), so a page claiming more than
// this isn't a normal waitlist and the slot is abandoned for the next
// candidate. maxSlowTotal bounds the CUMULATIVE sleep across all retries in
// one resolveSlow so an interactive grab (called with the request context and
// no deadline) can never hang for minutes waiting on countdown-gated partners.
const (
	maxSlowWait  = 90 * time.Second
	maxSlowTotal = 100 * time.Second
)

// resolveSlow walks the free slow-download partners. It tries partner slots
// 0..2; a slot behind a browser check is skipped. Slots that answer with a
// waitlist countdown instead of a link (the "slightly faster" partners
// time-gate anonymous downloads) are not failures: the shortest countdown is
// slept out and the slot re-fetched, which serves the link during its ~15s
// download window. Only when every slot is gated or never yields a link does
// the resolve fail, so the caller can move to the next candidate.
func (c *Client) resolveSlow(ctx context.Context, md5 string) (string, error) {
	return firstMirror(c.mirrors.Annas(), func(base string) (string, error) {
		slotURL := func(slot int) string {
			return fmt.Sprintf("%s/slow_download/%s/0/%d", base, md5, slot)
		}
		var gated bool
		waitSlot, waitFor := -1, time.Duration(-1)
		for slot := 0; slot < 3; slot++ {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			// read the body even on a non-200: partner servers serve their
			// verification wall with a 403/503, and losing that body would
			// misreport a gated slot as "no link found"
			page, status, err := c.fetchTextAllowErr(ctx, slotURL(slot))
			if err != nil {
				continue
			}
			if challengeRe.MatchString(page) {
				gated = true
				continue
			}
			if status != http.StatusOK {
				continue
			}
			if link := findSlowLink(page); link != "" {
				return link, nil
			}
			if w, ok := parseWaitSeconds(page); ok && (waitFor < 0 || w < waitFor) {
				waitSlot, waitFor = slot, w
			}
		}
		// every slot countdown-gated: wait out the shortest, then re-fetch.
		// A second countdown means the download window was missed — honor a
		// couple more, but only while a single countdown stays within the
		// normal range AND the cumulative sleep stays under the total budget,
		// so a partner reporting ever-growing waits can't stall the grab.
		var slept time.Duration
		for waitSlot >= 0 && waitFor >= 0 && waitFor <= maxSlowWait && slept+waitFor <= maxSlowTotal {
			if err := sleepCtx(ctx, waitFor+time.Second); err != nil {
				return "", err
			}
			slept += waitFor + time.Second
			page, err := c.fetchText(ctx, slotURL(waitSlot))
			if err != nil {
				break
			}
			if challengeRe.MatchString(page) {
				gated = true
				break
			}
			if link := findSlowLink(page); link != "" {
				return link, nil
			}
			var ok bool
			if waitFor, ok = parseWaitSeconds(page); !ok {
				break
			}
		}
		if gated {
			return "", ErrPartnersGated
		}
		return "", fmt.Errorf("annas: no slow-download link found on %s", base)
	})
}

// sleepCtx sleeps for d unless the context ends first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func (c *Client) fetchFile(ctx context.Context, fileURL, dir, wantFormat string) (string, error) {
	res, err := c.get(ctx, fileURL)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("annas: download returned %d", res.StatusCode)
	}
	// partner links point at a filename, but member/redirect links may not —
	// SaveDownload prefers the Content-Disposition name, back-fills the
	// extension, and keeps two concurrent downloads off the same filename.
	path, err := release.SaveDownload(res, fileURL, dir, wantFormat, "annas-download")
	if err != nil {
		return "", fmt.Errorf("annas: %w", err)
	}
	return path, nil
}
