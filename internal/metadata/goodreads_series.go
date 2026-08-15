package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// Goodreads series pages are the one place that reliably lists announced
// books years ahead (with positions and years) — Hardcover catalogs them
// late. The pages embed the full list as JSON in ReactComponents.SeriesList
// props, so no HTML scraping beyond locating the attribute.

// GRSeriesEntry is one row of a Goodreads series page.
type GRSeriesEntry struct {
	Position     string  // raw header: "1", "1.5", "1-3", "1, part 1"
	Index        float64 // parsed Position; 0 when Position isn't a clean number
	Title        string
	Author       string
	GoodreadsID  string
	CoverURL     string
	Year         string // publication year only — the page carries no full date
	RatingsCount int
	Description  string // HTML, as Goodreads serves it
}

var (
	grSeriesProps = regexp.MustCompile(`data-react-class="ReactComponents\.SeriesList" data-react-props="([^"]*)"`)
	grSeriesID    = regexp.MustCompile(`/series/(\d+)`)
	grCleanPos    = regexp.MustCompile(`^\d+(\.\d+)?$`)
)

// SeriesRefForBook resolves the Goodreads series a book belongs to (name and
// numeric series id) from the book's detail page. reachable mirrors
// FetchBook: false means the page was WAF-blocked, not that parsing failed.
func (g *Goodreads) SeriesRefForBook(ctx context.Context, bookID string) (name, seriesID string, reachable bool, err error) {
	body, status, err := g.get(ctx, g.BaseURL+"/book/show/"+url.PathEscape(bookID), "text/html")
	if err != nil {
		return "", "", false, err
	}
	if isWAFChallenge(status, body) {
		return "", "", false, nil
	}
	m := nextDataRe.FindSubmatch(body)
	if m == nil {
		return "", "", true, nil
	}
	var next struct {
		Props struct {
			PageProps struct {
				ApolloState map[string]json.RawMessage `json:"apolloState"`
			} `json:"pageProps"`
		} `json:"props"`
	}
	if err := json.Unmarshal(m[1], &next); err != nil || next.Props.PageProps.ApolloState == nil {
		return "", "", true, nil
	}
	state := next.Props.PageProps.ApolloState
	for key, raw := range state {
		if !strings.HasPrefix(key, "Book:") {
			continue
		}
		var b grBook
		if json.Unmarshal(raw, &b) != nil {
			continue
		}
		if b.LegacyID.String() != bookID && !strings.Contains(b.WebURL, "/book/show/"+bookID) {
			continue
		}
		if len(b.BookSeries) == 0 {
			return "", "", true, nil
		}
		var series struct {
			Title  string `json:"title"`
			WebURL string `json:"webUrl"`
		}
		raw, ok := state[b.BookSeries[0].Series.Ref]
		if !ok || json.Unmarshal(raw, &series) != nil {
			return "", "", true, nil
		}
		if sm := grSeriesID.FindStringSubmatch(series.WebURL); sm != nil {
			return series.Title, sm[1], true, nil
		}
		return "", "", true, nil
	}
	return "", "", true, nil
}

// SeriesEntries fetches and parses a Goodreads series page by numeric id.
func (g *Goodreads) SeriesEntries(ctx context.Context, seriesID string) ([]GRSeriesEntry, error) {
	body, status, err := g.get(ctx, g.BaseURL+"/series/"+url.PathEscape(seriesID), "text/html")
	if err != nil {
		return nil, err
	}
	if isWAFChallenge(status, body) {
		return nil, fmt.Errorf("goodreads series page blocked")
	}
	return parseGRSeriesPage(body)
}

func parseGRSeriesPage(body []byte) ([]GRSeriesEntry, error) {
	type grSeriesItem struct {
		Book struct {
			BookID          json.RawMessage `json:"bookId"`
			BookTitleBare   string          `json:"bookTitleBare"`
			Title           string          `json:"title"`
			ImageURL        string          `json:"imageUrl"`
			RatingsCount    json.RawMessage `json:"ratingsCount"`
			PublicationDate string          `json:"publicationDate"`
			Author          struct {
				Name string `json:"name"`
			} `json:"author"`
			Description struct {
				HTML string `json:"html"`
			} `json:"description"`
		} `json:"book"`
	}
	var out []GRSeriesEntry
	for _, m := range grSeriesProps.FindAllSubmatch(body, -1) {
		var props struct {
			Series        []grSeriesItem `json:"series"`
			SeriesHeaders []string       `json:"seriesHeaders"`
		}
		if err := json.Unmarshal([]byte(html.UnescapeString(string(m[1]))), &props); err != nil {
			continue // one malformed block must not sink the page
		}
		for i, it := range props.Series {
			pos := ""
			if i < len(props.SeriesHeaders) {
				pos = strings.TrimSpace(strings.TrimPrefix(props.SeriesHeaders[i], "Book "))
			}
			title := it.Book.BookTitleBare
			if title == "" {
				title = it.Book.Title
			}
			e := GRSeriesEntry{
				Position:     pos,
				Title:        title,
				Author:       it.Book.Author.Name,
				GoodreadsID:  rawScalar(it.Book.BookID),
				CoverURL:     upgradeGoodreadsCover(it.Book.ImageURL),
				Year:         strings.TrimSpace(it.Book.PublicationDate),
				RatingsCount: parseCount(it.Book.RatingsCount),
				Description:  strings.TrimSpace(it.Book.Description.HTML),
			}
			if grCleanPos.MatchString(pos) {
				e.Index, _ = strconv.ParseFloat(pos, 64)
			}
			out = append(out, e)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("goodreads series page: no SeriesList data found")
	}
	return out, nil
}
