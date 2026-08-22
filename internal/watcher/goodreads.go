// Package watcher runs the background loops: watched-list
// polling (Goodreads RSS, Hardcover lists), release-day calendar triggers,
// the weekly release-date refresh, and the opt-in backlog pass.
package watcher

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Goodreads shelves are watched through their public RSS feed:
// /review/list_rss/{userID}?shelf={shelf}. It needs no login, exposes the
// last 100 adds, and supports conditional requests — we send If-None-Match
// and treat 304 as "nothing new". Same standing defenses as the metadata
// scraper: browser UA and honoring 429.

const goodreadsRSSUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

type GoodreadsRSS struct {
	BaseURL string // override in tests
	Client  *http.Client
}

func NewGoodreadsRSS() *GoodreadsRSS {
	return &GoodreadsRSS{
		BaseURL: "https://www.goodreads.com",
		Client:  &http.Client{Timeout: 20 * time.Second},
	}
}

// Entry is one book on a shelf feed.
type Entry struct {
	GoodreadsID string
	Title       string
	Author      string
	ISBN13      string
	CoverURL    string
}

// goodreadsPageSize is the feed's fixed page length. A page shorter than
// this is the shelf's end; a full one means there may be more behind it.
const goodreadsPageSize = 100

// Fetch loads the first page of a shelf feed. etag is the value from the
// previous poll ("" for none); when the server answers 304 Not Modified,
// notModified is true and entries is nil.
func (g *GoodreadsRSS) Fetch(ctx context.Context, userID, shelf, etag string) (entries []Entry, newEtag string, notModified bool, err error) {
	return g.FetchPage(ctx, userID, shelf, 1, etag)
}

// FetchPage loads one page of a shelf feed — the feed is the shelf's
// newest-first review list, paged at goodreadsPageSize. Page 1 keeps the
// conditional-request behavior; deeper pages are only ever asked for when
// page 1 changed, so they skip the etag.
func (g *GoodreadsRSS) FetchPage(ctx context.Context, userID, shelf string, page int, etag string) (entries []Entry, newEtag string, notModified bool, err error) {
	u := fmt.Sprintf("%s/review/list_rss/%s?shelf=%s", g.BaseURL, url.PathEscape(userID), url.QueryEscape(shelf))
	if page > 1 {
		u += fmt.Sprintf("&page=%d", page)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", false, err
	}
	req.Header.Set("User-Agent", goodreadsRSSUA)
	req.Header.Set("Accept", "application/rss+xml, application/xml, text/xml")
	if etag != "" {
		req.Header.Set("If-None-Match", etag)
	}
	res, err := g.Client.Do(req)
	if err != nil {
		return nil, "", false, err
	}
	defer res.Body.Close()

	switch {
	case res.StatusCode == http.StatusNotModified:
		return nil, etag, true, nil
	case res.StatusCode == http.StatusTooManyRequests:
		return nil, "", false, fmt.Errorf("goodreads throttled (429)")
	case res.StatusCode >= 400:
		return nil, "", false, fmt.Errorf("goodreads feed status %d (is the shelf public?)", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, "", false, err
	}
	entries, err = parseShelfRSS(body)
	if err != nil {
		return nil, "", false, err
	}
	return entries, res.Header.Get("ETag"), false, nil
}

type rssDoc struct {
	Channel struct {
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
}

type rssItem struct {
	Title      string `xml:"title"`
	BookID     string `xml:"book_id"`
	AuthorName string `xml:"author_name"`
	ISBN       string `xml:"isbn"`
	ImageLarge string `xml:"book_large_image_url"`
	Image      string `xml:"book_image_url"`
}

func parseShelfRSS(body []byte) ([]Entry, error) {
	var doc rssDoc
	if err := xml.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("parse shelf feed: %w", err)
	}
	var out []Entry
	for _, it := range doc.Channel.Items {
		id := strings.TrimSpace(it.BookID)
		if id == "" {
			continue
		}
		e := Entry{
			GoodreadsID: id,
			Title:       strings.TrimSpace(it.Title),
			Author:      strings.TrimSpace(it.AuthorName),
		}
		if isbn := strings.TrimSpace(it.ISBN); len(isbn) == 13 {
			e.ISBN13 = isbn
		}
		if e.CoverURL = strings.TrimSpace(it.ImageLarge); e.CoverURL == "" {
			e.CoverURL = strings.TrimSpace(it.Image)
		}
		out = append(out, e)
	}
	return out, nil
}

// shelfLinkRe pulls shelf slugs out of a user's shelf-list page links;
// shelfCountRe finds the "(312)" that follows the link text in the sidebar.
var (
	shelfLinkRe  = regexp.MustCompile(`[?&]shelf=([A-Za-z0-9_%+.\-]+)`)
	shelfCountRe = regexp.MustCompile(`\(\s*(\d+)\s*\)`)
)

// Shelves discovers a user's shelves by scraping their review-list page —
// the standard three always exist; custom shelves come from the page links.
// Shelf is one shelf on a user's profile; Count is parsed from the shelf
// sidebar ("to-read (312)") and -1 when the page didn't carry one.
type Shelf struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (g *GoodreadsRSS) Shelves(ctx context.Context, userID string) ([]Shelf, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		g.BaseURL+"/review/list/"+url.PathEscape(userID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", goodreadsRSSUA)
	req.Header.Set("Accept", "text/html")
	res, err := g.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode >= 400 {
		return nil, fmt.Errorf("goodreads profile status %d (is the profile public?)", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	// counts live in the text right after each shelf link ("to-read (312)");
	// a shelf can appear in several places, so the first count wins
	counts := map[string]int{}
	order := []string{"to-read", "currently-reading", "read"}
	seen := map[string]bool{}
	for _, s := range order {
		seen[s] = true
	}
	for _, loc := range shelfLinkRe.FindAllSubmatchIndex(body, -1) {
		slug, err := url.QueryUnescape(string(body[loc[2]:loc[3]]))
		if err != nil || slug == "" || strings.HasPrefix(slug, "#") {
			continue
		}
		if !seen[slug] {
			seen[slug] = true
			order = append(order, slug)
		}
		if _, ok := counts[slug]; !ok {
			window := body[loc[1]:min(loc[1]+120, len(body))]
			if m := shelfCountRe.FindSubmatch(window); m != nil {
				if n, err := strconv.Atoi(string(m[1])); err == nil {
					counts[slug] = n
				}
			}
		}
	}
	shelves := make([]Shelf, 0, len(order))
	for _, s := range order {
		c, ok := counts[s]
		if !ok {
			c = -1
		}
		shelves = append(shelves, Shelf{Name: s, Count: c})
	}
	return shelves, nil
}

// source_ref for goodreads lists is stored canonically as "userID/shelf".
var (
	grUserIDRe  = regexp.MustCompile(`/user/show/(\d+)`)
	grListRSSRe = regexp.MustCompile(`/review/list(?:_rss)?/(\d+)`)
	digitsRe    = regexp.MustCompile(`^(\d+)`)
)

// ParseGoodreadsRef turns whatever the user pasted — a profile URL, a shelf
// URL, or a bare numeric ID — plus a shelf name into the canonical
// "userID/shelf" source ref.
func ParseGoodreadsRef(input, shelf string) (string, error) {
	input = strings.TrimSpace(input)
	shelf = strings.TrimSpace(shelf)
	if shelf == "" {
		shelf = "to-read"
	}
	var userID string
	switch {
	case grUserIDRe.MatchString(input):
		userID = grUserIDRe.FindStringSubmatch(input)[1]
	case grListRSSRe.MatchString(input):
		userID = grListRSSRe.FindStringSubmatch(input)[1]
	case digitsRe.MatchString(input):
		userID = digitsRe.FindStringSubmatch(input)[1]
	default:
		return "", fmt.Errorf("could not find a Goodreads user id in %q — paste your profile URL", input)
	}
	// pull ?shelf= out of a pasted URL when the shelf field was left default
	if u, err := url.Parse(input); err == nil {
		if s := u.Query().Get("shelf"); s != "" && shelf == "to-read" {
			shelf = s
		}
	}
	return userID + "/" + shelf, nil
}

// SplitGoodreadsRef is the inverse of ParseGoodreadsRef.
func SplitGoodreadsRef(ref string) (userID, shelf string, err error) {
	parts := strings.SplitN(ref, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("bad goodreads source ref %q", ref)
	}
	return parts[0], parts[1], nil
}
