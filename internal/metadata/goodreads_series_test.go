package metadata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fixture mirroring the live page shape: entries embedded as HTML-escaped
// JSON in ReactComponents.SeriesList props, split across multiple blocks.
func grSeriesPageHTML(blocks ...string) string {
	var sb strings.Builder
	sb.WriteString("<html><body>")
	for _, b := range blocks {
		sb.WriteString(`<div data-react-class="ReactComponents.SeriesList" data-react-props="`)
		sb.WriteString(strings.NewReplacer(`"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(b))
		sb.WriteString(`"></div>`)
	}
	sb.WriteString("</body></html>")
	return sb.String()
}

const grSeriesBlock1 = `{"series":[
  {"book":{"bookId":"101","bookTitleBare":"Warden K","imageUrl":"https://i.gr-assets.com/x/101._SY180_.jpg",
    "ratingsCount":58674,"publicationDate":"2016","author":{"name":"Grant Mercer"},
    "description":{"html":"<b>Who is Warden K?</b> A thriller."}}},
  {"book":{"bookId":"102","bookTitleBare":"Catch a Shadow","imageUrl":"",
    "ratingsCount":6286,"publicationDate":"2016","author":{"name":"Grant Mercer"},
    "description":{"html":"A short story."}}}
],"seriesHeaders":["Book 1","Book 1.5"]}`

const grSeriesBlock2 = `{"series":[
  {"book":{"bookId":"103","bookTitleBare":"The Trilogy Collection","imageUrl":"",
    "ratingsCount":2110,"publicationDate":"","author":{"name":"Grant Mercer"},"description":{"html":""}}},
  {"book":{"bookId":"104","bookTitleBare":"Das dunkle Zeitalter - Teil 1","imageUrl":"",
    "ratingsCount":380,"publicationDate":"2019","author":{"name":"Grant Mercer"},"description":{"html":""}}},
  {"book":{"bookId":"105","bookTitleBare":"The Broken Man","imageUrl":"",
    "ratingsCount":0,"publicationDate":"2027","author":{"name":"Grant Mercer"},"description":{"html":"Announced."}}}
],"seriesHeaders":["Book 1-3","Book 5, part 1","Book 11.5"]}`

func TestParseGRSeriesPage(t *testing.T) {
	entries, err := parseGRSeriesPage([]byte(grSeriesPageHTML(grSeriesBlock1, grSeriesBlock2)))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 5 {
		t.Fatalf("parsed %d entries, want 5: %+v", len(entries), entries)
	}
	byTitle := map[string]GRSeriesEntry{}
	for _, e := range entries {
		byTitle[e.Title] = e
	}

	ox := byTitle["Warden K"]
	if ox.Index != 1 || ox.Year != "2016" || ox.RatingsCount != 58674 || ox.GoodreadsID != "101" {
		t.Errorf("Warden K = %+v", ox)
	}
	if !strings.Contains(ox.Description, "<b>Who is Warden K?</b>") {
		t.Errorf("description lost markup: %q", ox.Description)
	}
	if strings.Contains(ox.CoverURL, "_SY180_") {
		t.Errorf("cover not upgraded to full size: %q", ox.CoverURL)
	}
	if e := byTitle["Catch a Shadow"]; e.Index != 1.5 {
		t.Errorf("novella index = %g, want 1.5", e.Index)
	}
	// dirty positions parse to Index 0 — the overlay's signal to skip
	if e := byTitle["The Trilogy Collection"]; e.Index != 0 || e.Position != "1-3" {
		t.Errorf("box set = %+v", e)
	}
	if e := byTitle["Das dunkle Zeitalter - Teil 1"]; e.Index != 0 || e.Position != "5, part 1" {
		t.Errorf("split translation = %+v", e)
	}
	if e := byTitle["The Broken Man"]; e.Index != 11.5 || e.Year != "2027" || e.RatingsCount != 0 {
		t.Errorf("announced book = %+v", e)
	}
}

// book page fixture: just enough __NEXT_DATA__ for series-ref resolution.
func grBookPageHTML(bookID, seriesTitle, seriesURL string) string {
	return fmt.Sprintf(`<html><body><script id="__NEXT_DATA__" type="application/json">
	{"props":{"pageProps":{"apolloState":{
	  "Book:kca://book/%s":{"legacyId":%s,"title":"X","webUrl":"https://www.goodreads.com/book/show/%s-x",
	    "bookSeries":[{"userPosition":"1","series":{"__ref":"Series:kca://series/9"}}]},
	  "Series:kca://series/9":{"title":"%s","webUrl":"%s"}
	}}}}</script></body></html>`, bookID, bookID, bookID, seriesTitle, seriesURL)
}

func TestSeriesRefForBook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/book/show/42") {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, grBookPageHTML("42", "Warden K", "https://www.goodreads.com/series/170378-warden-k"))
	}))
	t.Cleanup(srv.Close)
	g := NewGoodreads()
	g.BaseURL = srv.URL

	name, id, reachable, err := g.SeriesRefForBook(context.Background(), "42")
	if err != nil || !reachable {
		t.Fatalf("err=%v reachable=%v", err, reachable)
	}
	if name != "Warden K" || id != "170378" {
		t.Errorf("got (%q, %q), want (Warden K, 170378)", name, id)
	}
}
