package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Goodreads has no API. Access, in order of robustness:
//  1. JSON autocomplete endpoint (search) — rarely blocked
//  2. book pages' embedded __NEXT_DATA__ JSON (full records)
//  3. /book/isbn/{isbn} redirect (ISBN → book id)
// Detail pages sit behind an AWS WAF challenge that appears intermittently, so
// every detail fetch can degrade to autocomplete-level data without failing
// the batch.

const goodreadsUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

type Goodreads struct {
	BaseURL string // override in tests
	Client  *http.Client
	// polite pacing between detail fetches
	Delay time.Duration

	MaxResults int
}

func NewGoodreads() *Goodreads {
	return &Goodreads{
		BaseURL:    "https://www.goodreads.com",
		Client:     &http.Client{Timeout: 15 * time.Second},
		Delay:      600 * time.Millisecond,
		MaxResults: 3,
	}
}

func (g *Goodreads) Key() string { return "goodreads" }

// EnrichOnly keeps Goodreads out of primary search and author bibliographies
// while preserving its role as the by-id detail source for watched-list
// entries (thin RSS references enriched via FetchBook) and as a gap-filler in
// Enrich. Hardcover drives search and bibliographies; Goodreads drives lists.
func (g *Goodreads) EnrichOnly() bool { return true }

func (g *Goodreads) Search(ctx context.Context, p SearchParams) ([]BookMeta, error) {
	if p.ISBN != "" {
		id, err := g.ResolveISBN(ctx, p.ISBN)
		if err != nil || id == "" {
			return nil, err
		}
		meta, _, err := g.FetchBook(ctx, id)
		if err != nil || meta == nil {
			return nil, err
		}
		return []BookMeta{*meta}, nil
	}

	items, err := g.autocomplete(ctx, p)
	if err != nil {
		return nil, err
	}
	// Return up to the caller's limit, but only the top MaxResults get the
	// expensive detail-page fetch — the rest ship with autocomplete-level
	// data (title, author, cover, series guess), which is plenty to pick
	// from, and enrichment fills the gaps once one is added.
	limit := p.Limit
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	if len(items) > limit {
		items = items[:limit]
	}

	results := make([]BookMeta, 0, len(items))
	detailReachable := true
	for i, item := range items {
		var meta *BookMeta
		if i < g.MaxResults && detailReachable && item.GoodreadsID != "" {
			if i > 0 {
				select {
				case <-ctx.Done():
					return results, ctx.Err()
				case <-time.After(g.Delay):
				}
			}
			detail, reachable, err := g.FetchBook(ctx, item.GoodreadsID)
			if err == nil && detail != nil {
				meta = detail
			}
			if !reachable {
				// WAF block: stop hammering detail pages, use autocomplete data.
				detailReachable = false
			}
		}
		if meta == nil {
			m := item
			meta = &m
		}
		results = append(results, *meta)
	}
	return results, nil
}

// ---- autocomplete ----

type grAutocompleteItem struct {
	// bookId arrives as a number, ratingsCount as a comma-formatted string
	// ("1,500,000") — both parsed leniently.
	BookID        json.RawMessage `json:"bookId"`
	BookURL       string          `json:"bookUrl"`
	Title         string          `json:"title"`
	BookTitleBare string          `json:"bookTitleBare"`
	Author        json.RawMessage `json:"author"`
	ImageURL      string          `json:"imageUrl"`
	RatingsCount  json.RawMessage `json:"ratingsCount"`
}

func (g *Goodreads) autocomplete(ctx context.Context, p SearchParams) ([]BookMeta, error) {
	query := p.Query
	if query == "" {
		query = strings.TrimSpace(p.Title + " " + p.Author)
	}
	if query == "" {
		return nil, nil
	}

	u := fmt.Sprintf("%s/book/auto_complete?format=json&q=%s", g.BaseURL, url.QueryEscape(query))
	body, _, err := g.get(ctx, u, "application/json,text/plain,*/*")
	if err != nil {
		return nil, err
	}
	var items []grAutocompleteItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("goodreads autocomplete: %w", err)
	}

	seen := map[string]bool{}
	var metas []BookMeta
	for _, it := range items {
		id := it.bookID()
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		title := it.BookTitleBare
		if title == "" {
			title = it.Title
		}
		seriesName, seriesIdx := parseSeriesSuffix(it.Title)
		m := BookMeta{
			Provider:     "goodreads",
			Title:        title,
			Authors:      it.authorNames(),
			GoodreadsID:  id,
			CoverURL:     upgradeGoodreadsCover(it.ImageURL),
			SeriesName:   seriesName,
			SeriesIndex:  seriesIdx,
			RatingsCount: parseCount(it.RatingsCount),
			Compilation:  IsCompilation(title),
		}
		metas = append(metas, m)
	}

	wantTitle := p.Title
	if wantTitle == "" {
		wantTitle = p.Query
	}
	sort.SliceStable(metas, func(a, b int) bool {
		return scoreCandidate(metas[a], wantTitle, p.Author) > scoreCandidate(metas[b], wantTitle, p.Author)
	})
	return metas, nil
}

func (it grAutocompleteItem) bookID() string {
	if s := rawScalar(it.BookID); s != "" && s != "0" {
		return s
	}
	if m := regexp.MustCompile(`/book/show/(\d+)`).FindStringSubmatch(it.BookURL); m != nil {
		return m[1]
	}
	return ""
}

// rawScalar renders a JSON value that may be a number or a string as text.
func rawScalar(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var n json.Number
	if json.Unmarshal(raw, &n) == nil {
		return n.String()
	}
	return ""
}

func (it grAutocompleteItem) authorNames() []string {
	if len(it.Author) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(it.Author, &s) == nil && s != "" {
		return []string{s}
	}
	var obj struct {
		Name string `json:"name"`
	}
	if json.Unmarshal(it.Author, &obj) == nil && obj.Name != "" {
		return []string{obj.Name}
	}
	return nil
}

// ---- ISBN resolver ----

var grCanonical = regexp.MustCompile(`/book/show/(\d+)`)

func (g *Goodreads) ResolveISBN(ctx context.Context, isbn string) (string, error) {
	body, _, err := g.get(ctx, g.BaseURL+"/book/isbn/"+url.PathEscape(isbn), "text/html")
	if err != nil {
		return "", err
	}
	if m := grCanonical.FindSubmatch(body); m != nil {
		return string(m[1]), nil
	}
	return "", nil
}

// ---- detail pages (__NEXT_DATA__) ----

var nextDataRe = regexp.MustCompile(`(?s)<script id="__NEXT_DATA__"[^>]*>(.*?)</script>`)

// FetchBook loads a book page and maps its embedded Apollo state. reachable
// reports whether the page itself loaded (false = WAF block / network), so
// callers can distinguish "blocked" from "did not parse".
func (g *Goodreads) FetchBook(ctx context.Context, id string) (meta *BookMeta, reachable bool, err error) {
	body, status, err := g.get(ctx, g.BaseURL+"/book/show/"+url.PathEscape(id), "text/html")
	if err != nil {
		return nil, false, err
	}
	if isWAFChallenge(status, body) {
		return nil, false, nil
	}
	m := nextDataRe.FindSubmatch(body)
	if m == nil {
		return nil, true, nil
	}
	var next struct {
		Props struct {
			PageProps struct {
				ApolloState map[string]json.RawMessage `json:"apolloState"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(m[1], &next); err != nil {
		return nil, true, nil
	}
	state := next.Props.PageProps.ApolloState
	if state == nil {
		return nil, true, nil
	}
	return mapApolloState(state, id), true, nil
}

type grBook struct {
	LegacyID    json.Number `json:"legacyId"`
	Title       string      `json:"title"`
	Description string      `json:"description"`
	ImageURL    string      `json:"imageUrl"`
	WebURL      string      `json:"webUrl"`
	Details     struct {
		Publisher       string      `json:"publisher"`
		Isbn            string      `json:"isbn"`
		Isbn13          string      `json:"isbn13"`
		PublicationTime json.Number `json:"publicationTime"` // epoch millis
		Language        struct {
			Name string `json:"name"`
		} `json:"language"`
	} `json:"details"`
	PrimaryContributorEdge struct {
		Node struct {
			Ref string `json:"__ref"`
		} `json:"node"`
	} `json:"primaryContributorEdge"`
	BookSeries []struct {
		UserPosition string `json:"userPosition"`
		Series       struct {
			Ref string `json:"__ref"`
		} `json:"series"`
	} `json:"bookSeries"`
	// Stats moved from the Book record to the linked Work record in a layout
	// drift (observed 2026-08); both locations are read, book-level first.
	Stats grStats `json:"stats"`
	Work  struct {
		Ref string `json:"__ref"`
	} `json:"work"`
	BookGenres []struct {
		Genre struct {
			Name string `json:"name"`
		} `json:"genre"`
	} `json:"bookGenres"`
}

type grStats struct {
	RatingsCount int `json:"ratingsCount"`
}

func mapApolloState(state map[string]json.RawMessage, wantID string) *BookMeta {
	var book *grBook
	for key, raw := range state {
		if !strings.HasPrefix(key, "Book:") {
			continue
		}
		var b grBook
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		if b.LegacyID.String() == wantID || strings.Contains(b.WebURL, "/book/show/"+wantID) {
			book = &b
			break
		}
	}
	if book == nil || book.Title == "" {
		return nil
	}

	meta := &BookMeta{
		Provider:     "goodreads",
		Title:        book.Title,
		Description:  strings.TrimSpace(book.Description),
		Publisher:    book.Details.Publisher,
		Language:     book.Details.Language.Name,
		ISBN10:       book.Details.Isbn,
		ISBN13:       book.Details.Isbn13,
		GoodreadsID:  wantID,
		CoverURL:     book.ImageURL,
		RatingsCount: book.Stats.RatingsCount,
		Compilation:  IsCompilation(book.Title),
	}
	if meta.RatingsCount == 0 && book.Work.Ref != "" {
		var work struct {
			Stats grStats `json:"stats"`
		}
		if raw, ok := state[book.Work.Ref]; ok && json.Unmarshal(raw, &work) == nil {
			meta.RatingsCount = work.Stats.RatingsCount
		}
	}
	if ms, err := book.Details.PublicationTime.Int64(); err == nil && ms > 0 {
		meta.ReleaseDate = time.UnixMilli(ms).UTC().Format("2006-01-02")
	}
	for _, g := range book.BookGenres {
		if g.Genre.Name != "" && len(meta.Genres) < 6 {
			meta.Genres = append(meta.Genres, g.Genre.Name)
		}
	}
	if ref := book.PrimaryContributorEdge.Node.Ref; ref != "" {
		var contributor struct {
			Name string `json:"name"`
		}
		if raw, ok := state[ref]; ok && json.Unmarshal(raw, &contributor) == nil && contributor.Name != "" {
			meta.Authors = []string{contributor.Name}
		}
	}
	if len(book.BookSeries) > 0 {
		bs := book.BookSeries[0]
		var series struct {
			Title string `json:"title"`
		}
		if raw, ok := state[bs.Series.Ref]; ok && json.Unmarshal(raw, &series) == nil {
			meta.SeriesName = series.Title
		}
		if f, err := strconv.ParseFloat(strings.TrimSpace(bs.UserPosition), 64); err == nil {
			meta.SeriesIndex = f
		}
	}
	return meta
}

// ---- shared plumbing ----

func (g *Goodreads) get(ctx context.Context, url, accept string) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", goodreadsUA)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	res, err := g.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, res.StatusCode, err
	}
	if res.StatusCode == http.StatusTooManyRequests {
		return nil, res.StatusCode, fmt.Errorf("goodreads throttled (429)")
	}
	if res.StatusCode >= 400 {
		return nil, res.StatusCode, fmt.Errorf("goodreads status %d", res.StatusCode)
	}
	return body, res.StatusCode, nil
}

var wafMarkers = regexp.MustCompile(`awsWafCookieDomainList|AwsWafIntegration|id="challenge-container"|challenge\.js`)

func isWAFChallenge(status int, body []byte) bool {
	return status == http.StatusAccepted || wafMarkers.Match(body)
}

// "First Ember (The Ember Cycle, #1)" → ("The Ember Cycle", 1)
var seriesSuffix = regexp.MustCompile(`\(([^()]+?),?\s*#([\d.]+)\)\s*$`)

func parseSeriesSuffix(title string) (string, float64) {
	m := seriesSuffix.FindStringSubmatch(title)
	if m == nil {
		return "", 0
	}
	idx, _ := strconv.ParseFloat(m[2], 64)
	return strings.TrimSpace(m[1]), idx
}

// autocomplete serves tiny covers; request the full-size variant.
var coverSizeRe = regexp.MustCompile(`\._\w+\d+_?\.`)

func upgradeGoodreadsCover(u string) string {
	return coverSizeRe.ReplaceAllString(u, ".")
}

func parseCount(raw json.RawMessage) int {
	s := strings.ReplaceAll(rawScalar(raw), ",", "")
	v, _ := strconv.Atoi(s)
	return v
}
