package opds

import (
	"encoding/xml"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/catalog"
)

// The catalog a reader app browses: a start page leading to every book,
// recent arrivals, authors, series, the account's tags, and search. Every
// list is built from the books the account may read — nothing else is ever
// loaded into a feed — so an author or series index can't advertise books
// shared with someone else.

const (
	atomNS     = "http://www.w3.org/2005/Atom"
	navType    = "application/atom+xml;profile=opds-catalog;kind=navigation"
	acqType    = "application/atom+xml;profile=opds-catalog;kind=acquisition"
	searchType = "application/opensearchdescription+xml"
	// pageSize keeps a feed small enough for an e-reader to page through
	pageSize = 100
)

// shelf returns the books the account may read and download.
func (h *Handler) shelf(u *auth.User) ([]catalog.Book, error) {
	all, err := h.catalog.ListBooks(0, 0, 0)
	if err != nil {
		return nil, err
	}
	out := all[:0:0]
	for i := range all {
		if all[i].FilePath != "" && h.mayRead(u, &all[i]) {
			out = append(out, all[i])
		}
	}
	return out, nil
}

func newFeed(id, title, self, kind string) feed {
	return feed{
		Xmlns: atomNS, XmlnsDC: "http://purl.org/dc/terms/", XmlnsOS: "http://a9.com/-/spec/opensearch/1.1/",
		ID: id, Title: title, Updated: time.Now().UTC().Format(time.RFC3339),
		Links: []link{
			{Rel: "self", Href: self, Type: kind},
			{Rel: "start", Href: "/opds", Type: navType},
			{Rel: "search", Href: "/opds/opensearch.xml", Type: searchType},
		},
	}
}

func writeFeed(w http.ResponseWriter, f feed, kind string) {
	w.Header().Set("Content-Type", kind+"; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(xml.Header)); err != nil {
		return
	}
	if err := xml.NewEncoder(w).Encode(f); err != nil {
		log.Printf("opds: encode feed: %v", err)
	}
}

// navEntry is one row of a navigation feed: a title leading to another feed.
func navEntry(id, title, blurb, href, kind string) entry {
	e := entry{
		ID: id, Title: title, Updated: time.Now().UTC().Format(time.RFC3339),
		Links: []link{{Rel: "subsection", Href: href, Type: kind}},
	}
	if blurb != "" {
		e.Content = &text{Type: "text", Body: blurb}
	}
	return e
}

func (h *Handler) bookEntry(b *catalog.Book) entry {
	e := entry{
		ID:       fmt.Sprintf("urn:booky:book:%d", b.ID),
		Title:    b.Title,
		Updated:  time.Now().UTC().Format(time.RFC3339),
		Authors:  []person{{Name: b.Author}},
		Language: b.Language,
		Links: []link{{
			Rel:  "http://opds-spec.org/acquisition",
			Href: fmt.Sprintf("/opds/download/%d", b.ID),
			Type: mimeFor(b.FileFormat),
		}},
	}
	if b.SeriesName != "" {
		label := b.SeriesName
		if b.SeriesNum != 0 {
			label = fmt.Sprintf("%s #%g", b.SeriesName, b.SeriesNum)
		}
		e.Series = label
	}
	if b.Description != "" {
		e.Summary = &text{Type: "text", Body: b.Description}
	}
	if h.covers.Path(b.ID) != "" {
		cover := fmt.Sprintf("/opds/cover/%d", b.ID)
		e.Links = append(e.Links,
			link{Rel: "http://opds-spec.org/image", Href: cover, Type: "image/jpeg"},
			link{Rel: "http://opds-spec.org/image/thumbnail", Href: cover, Type: "image/jpeg"},
		)
	}
	return e
}

// writeBooks renders a page of books as an acquisition feed, with
// first/previous/next links when the list runs past one page.
func (h *Handler) writeBooks(w http.ResponseWriter, r *http.Request, id, title, self string, books []catalog.Book) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageURL := func(n int) string {
		q := r.URL.Query()
		q.Set("page", strconv.Itoa(n))
		return self + "?" + q.Encode()
	}
	f := newFeed(id, title, pageURL(page), acqType)
	f.TotalItems = len(books)
	start := (page - 1) * pageSize
	if start > len(books) {
		start = len(books)
	}
	end := min(start+pageSize, len(books))
	if page > 1 {
		f.Links = append(f.Links, link{Rel: "first", Href: pageURL(1), Type: acqType},
			link{Rel: "previous", Href: pageURL(page - 1), Type: acqType})
	}
	if end < len(books) {
		f.Links = append(f.Links, link{Rel: "next", Href: pageURL(page + 1), Type: acqType})
	}
	for i := start; i < end; i++ {
		f.Entries = append(f.Entries, h.bookEntry(&books[i]))
	}
	writeFeed(w, f, acqType)
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func byTitle(books []catalog.Book) {
	sort.SliceStable(books, func(i, j int) bool {
		return strings.ToLower(books[i].Title) < strings.ToLower(books[j].Title)
	})
}

// handleRoot is the start page: where a reader app lands.
func (h *Handler) handleRoot(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	f := newFeed("urn:booky:user:"+strconv.FormatInt(u.ID, 10), u.Username+"'s books — Booky", "/opds", navType)
	f.Entries = []entry{
		navEntry("urn:booky:all", "All books", plural(len(books), "book"), "/opds/all", acqType),
		navEntry("urn:booky:recent", "Recently added", "The newest arrivals first", "/opds/recent", acqType),
		navEntry("urn:booky:authors", "Authors", "", "/opds/authors", navType),
		navEntry("urn:booky:series", "Series", "", "/opds/series", navType),
	}
	if tags := h.tagsFor(u); len(tags) > 0 {
		f.Entries = append(f.Entries, navEntry("urn:booky:tags", "Tags", "What's shared with you, by tag", "/opds/tags", navType))
	}
	writeFeed(w, f, navType)
}

func (h *Handler) handleAll(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	byTitle(books)
	h.writeBooks(w, r, "urn:booky:all", "All books", "/opds/all", books)
}

func (h *Handler) handleRecent(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	sort.SliceStable(books, func(i, j int) bool {
		if books[i].AddedAt != books[j].AddedAt {
			return books[i].AddedAt > books[j].AddedAt
		}
		return books[i].ID > books[j].ID
	})
	if len(books) > pageSize {
		books = books[:pageSize]
	}
	h.writeBooks(w, r, "urn:booky:recent", "Recently added", "/opds/recent", books)
}

// group is one author or series in an index, with how many of the
// account's books it holds.
type group struct {
	id    int64
	name  string
	sort  string
	count int
}

func sortedGroups(m map[int64]*group) []*group {
	out := make([]*group, 0, len(m))
	for _, g := range m {
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].sort) < strings.ToLower(out[j].sort) })
	return out
}

func (h *Handler) handleAuthors(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	authors := map[int64]*group{}
	for _, b := range books {
		g := authors[b.AuthorID]
		if g == nil {
			// sort by surname: "Jane Austen" files under A
			sortKey := b.Author
			if i := strings.LastIndex(b.Author, " "); i > 0 {
				sortKey = b.Author[i+1:] + " " + b.Author[:i]
			}
			g = &group{id: b.AuthorID, name: b.Author, sort: sortKey}
			authors[b.AuthorID] = g
		}
		g.count++
	}
	f := newFeed("urn:booky:authors", "Authors", "/opds/authors", navType)
	for _, g := range sortedGroups(authors) {
		f.Entries = append(f.Entries, navEntry(fmt.Sprintf("urn:booky:author:%d", g.id), g.name,
			plural(g.count, "book"), fmt.Sprintf("/opds/authors/%d", g.id), acqType))
	}
	writeFeed(w, f, navType)
}

func (h *Handler) handleAuthor(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	mine := books[:0:0]
	for _, b := range books {
		if b.AuthorID == id {
			mine = append(mine, b)
		}
	}
	// an author with nothing on the account's shelf doesn't exist here
	if len(mine) == 0 {
		http.NotFound(w, r)
		return
	}
	sort.SliceStable(mine, func(i, j int) bool {
		if mine[i].SeriesName != mine[j].SeriesName {
			return strings.ToLower(mine[i].SeriesName) < strings.ToLower(mine[j].SeriesName)
		}
		if mine[i].SeriesNum != mine[j].SeriesNum {
			return mine[i].SeriesNum < mine[j].SeriesNum
		}
		return strings.ToLower(mine[i].Title) < strings.ToLower(mine[j].Title)
	})
	h.writeBooks(w, r, fmt.Sprintf("urn:booky:author:%d", id), mine[0].Author, fmt.Sprintf("/opds/authors/%d", id), mine)
}

func (h *Handler) handleSeriesIndex(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	series := map[int64]*group{}
	authorOf := map[int64]string{}
	for _, b := range books {
		if b.SeriesID == nil {
			continue
		}
		g := series[*b.SeriesID]
		if g == nil {
			g = &group{id: *b.SeriesID, name: b.SeriesName, sort: b.SeriesName}
			series[*b.SeriesID] = g
			authorOf[*b.SeriesID] = b.Author
		}
		g.count++
	}
	f := newFeed("urn:booky:series", "Series", "/opds/series", navType)
	for _, g := range sortedGroups(series) {
		f.Entries = append(f.Entries, navEntry(fmt.Sprintf("urn:booky:series:%d", g.id), g.name,
			authorOf[g.id]+" · "+plural(g.count, "book"), fmt.Sprintf("/opds/series/%d", g.id), acqType))
	}
	writeFeed(w, f, navType)
}

func (h *Handler) handleSeries(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	mine := books[:0:0]
	for _, b := range books {
		if b.SeriesID != nil && *b.SeriesID == id {
			mine = append(mine, b)
		}
	}
	if len(mine) == 0 {
		http.NotFound(w, r)
		return
	}
	sort.SliceStable(mine, func(i, j int) bool { return mine[i].SeriesNum < mine[j].SeriesNum })
	h.writeBooks(w, r, fmt.Sprintf("urn:booky:series:%d", id), mine[0].SeriesName, fmt.Sprintf("/opds/series/%d", id), mine)
}

// tagsFor lists the tags an account may browse by: every tag for an admin,
// the ones they hold otherwise.
func (h *Handler) tagsFor(u *auth.User) []catalog.Tag {
	all, err := h.catalog.ListTags()
	if err != nil {
		return nil
	}
	out := all[:0:0]
	for _, t := range all {
		if u.IsAdmin() || u.HasTag(t.ID) {
			out = append(out, t)
		}
	}
	return out
}

func tagTitle(u *auth.User, t catalog.Tag) string {
	if t.ID == u.PersonalTag {
		return "My shelf"
	}
	return t.Name
}

func (h *Handler) handleTags(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	counts := map[int64]int{}
	for _, b := range books {
		for _, t := range b.Tags {
			counts[t]++
		}
	}
	f := newFeed("urn:booky:tags", "Tags", "/opds/tags", navType)
	for _, t := range h.tagsFor(u) {
		if counts[t.ID] == 0 {
			continue
		}
		f.Entries = append(f.Entries, navEntry(fmt.Sprintf("urn:booky:tag:%d", t.ID), tagTitle(u, t),
			plural(counts[t.ID], "book"), fmt.Sprintf("/opds/tags/%d", t.ID), acqType))
	}
	writeFeed(w, f, navType)
}

func (h *Handler) handleTag(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var tag *catalog.Tag
	for _, t := range h.tagsFor(u) {
		if t.ID == id {
			tag = &t
			break
		}
	}
	// a tag the account doesn't hold reads as absent, like everything else
	if tag == nil {
		http.NotFound(w, r)
		return
	}
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	mine := books[:0:0]
	for _, b := range books {
		for _, t := range b.Tags {
			if t == id {
				mine = append(mine, b)
				break
			}
		}
	}
	byTitle(mine)
	h.writeBooks(w, r, fmt.Sprintf("urn:booky:tag:%d", id), tagTitle(u, *tag), fmt.Sprintf("/opds/tags/%d", id), mine)
}

// handleSearch matches every word of the query against each book's title,
// author and series, case-insensitively — "sherlock doyle" finds the
// Holmes books.
func (h *Handler) handleSearch(w http.ResponseWriter, r *http.Request) {
	u, ok := h.authorize(w, r)
	if !ok {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	books, err := h.shelf(u)
	if err != nil {
		http.Error(w, "feed unavailable", http.StatusInternalServerError)
		return
	}
	words := strings.Fields(strings.ToLower(q))
	hits := books[:0:0]
	if len(words) > 0 {
		for _, b := range books {
			hay := strings.ToLower(b.Title + " " + b.Author + " " + b.SeriesName)
			all := true
			for _, w := range words {
				if !strings.Contains(hay, w) {
					all = false
					break
				}
			}
			if all {
				hits = append(hits, b)
			}
		}
	}
	byTitle(hits)
	h.writeBooks(w, r, "urn:booky:search:"+url.QueryEscape(q), "Search: "+q, "/opds/search", hits)
}

// handleOpenSearch describes the search to reader apps (KOReader, Moon+…):
// where to send the words.
func (h *Handler) handleOpenSearch(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.authorize(w, r); !ok {
		return
	}
	w.Header().Set("Content-Type", searchType+"; charset=utf-8")
	_, _ = w.Write([]byte(xml.Header + `<OpenSearchDescription xmlns="http://a9.com/-/spec/opensearch/1.1/">
  <ShortName>Booky</ShortName>
  <Description>Search your Booky books by title, author or series</Description>
  <InputEncoding>UTF-8</InputEncoding>
  <OutputEncoding>UTF-8</OutputEncoding>
  <Url type="` + acqType + `" template="/opds/search?q={searchTerms}"/>
</OpenSearchDescription>
`))
}
