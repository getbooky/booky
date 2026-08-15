package metadata

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Hardcover exposes a GraphQL API (beta) at api.hardcover.app/v1/graphql with
// per-account bearer tokens. It is the structured backbone: clean series data
// and edition/ISBN cross-links. Documented limit is 60 requests/minute — our
// usage (one query per lookup) sits far below it.
//
// NOTE: the schema is beta and can drift; the queries below are the surface we
// depend on and are exercised against the live API during integration testing,
// not unit tests.

type Hardcover struct {
	BaseURL string // override in tests
	Client  *http.Client
	Token   func() string // fetched per-call so settings changes apply live
	limiter *hcLimiter
}

func NewHardcover(token func() string) *Hardcover {
	return &Hardcover{
		BaseURL: "https://api.hardcover.app/v1/graphql",
		Client:  &http.Client{Timeout: 15 * time.Second},
		Token:   token,
		limiter: &hcLimiter{},
	}
}

func (h *Hardcover) Key() string { return "hardcover" }

// WorksConfigured reports whether bibliography queries can be answered — the
// chain treats an unconfigured Hardcover as absent rather than authoritative.
func (h *Hardcover) WorksConfigured() bool { return h.Token() != "" }

// Test verifies the token with the cheapest possible query.
func (h *Hardcover) Test(ctx context.Context) error {
	if h.Token() == "" {
		return fmt.Errorf("no token set")
	}
	var out struct {
		Books []struct {
			ID json.Number `json:"id"`
		} `json:"books"`
	}
	if err := h.do(ctx, `query { books(limit: 1) { id } }`, nil, &out); err != nil {
		return err
	}
	if len(out.Books) == 0 {
		return fmt.Errorf("token accepted but the API returned nothing — try again")
	}
	return nil
}

const hcBookFields = `
  id
  title
  description
  release_date
  users_count
  compilation
  cached_tags
  cached_image
  contributions { author { id name } }
  book_series { position series { id name } }
  default_cover_edition { language { language code3 } }
  default_ebook_edition { language { language code3 } }
  default_physical_edition { language { language code3 } }
`

// hcEnglishOrNull is a server-side pre-filter for bibliography queries: keep
// books whose representative (cover) edition is English or has no language
// recorded, so foreign-language book records don't consume the query limit.
// Language truly lives on editions (books carry none — verified against the
// schema), and the cover edition is the record's canonical display edition.
// Books with no cover edition or no language pass through; the client-side
// filter never sees them as foreign, so nothing legitimate is lost.
//
// The no-cover-edition arm matches on the RELATION being absent, not the id
// column being null: live records exist whose default_cover_edition_id
// points at an edition row that doesn't resolve (verified against live
// records), and an id-null check silently drops those books
// from bibliographies. `_not: {relation: {}}` covers both the null-id and
// dangling-id cases.
const hcEnglishOrNull = `_or: [
  {_not: {default_cover_edition: {}}},
  {default_cover_edition: {language_id: {_is_null: true}}},
  {default_cover_edition: {language: {code3: {_eq: "eng"}}}}
]`

func (h *Hardcover) Search(ctx context.Context, p SearchParams) ([]BookMeta, error) {
	if h.Token() == "" {
		return nil, nil // not configured — chain moves on
	}
	if p.ISBN != "" {
		return h.byISBN(ctx, p.ISBN)
	}
	q := p.Title
	if q == "" {
		q = p.Query
	}
	if q == "" {
		return nil, nil
	}
	return h.byTitle(ctx, q, p.Author, p.Limit)
}

func (h *Hardcover) byISBN(ctx context.Context, isbn string) ([]BookMeta, error) {
	query := fmt.Sprintf(`query ($isbn: String!) {
	  editions(where: {_or: [{isbn_13: {_eq: $isbn}}, {isbn_10: {_eq: $isbn}}]}, limit: 3) {
	    isbn_13
	    isbn_10
	    book { %s }
	  }
	}`, hcBookFields)

	var out struct {
		Editions []struct {
			Isbn13 string  `json:"isbn_13"`
			Isbn10 string  `json:"isbn_10"`
			Book   *hcBook `json:"book"`
		} `json:"editions"`
	}
	if err := h.do(ctx, query, map[string]any{"isbn": isbn}, &out); err != nil {
		return nil, err
	}
	var metas []BookMeta
	for _, e := range out.Editions {
		if e.Book == nil {
			continue
		}
		m := e.Book.toMeta()
		m.ISBN13 = e.Isbn13
		m.ISBN10 = e.Isbn10
		metas = append(metas, m)
	}
	return metas, nil
}

// FetchByHardcoverID fetches one exact book by its canonical Hardcover ID —
// the strongest key the enrich chain can hold. Returns (nil, nil) when the
// id doesn't resolve (deleted/merged records) so callers can fall back.
func (h *Hardcover) FetchByHardcoverID(ctx context.Context, id string) (*BookMeta, error) {
	if h.Token() == "" {
		return nil, nil // not configured — chain moves on
	}
	n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("hardcover id %q is not numeric", id)
	}
	query := fmt.Sprintf(`query ($id: Int!) {
	  books(where: {id: {_eq: $id}}, limit: 1) { %s }
	}`, hcBookFields)
	var out struct {
		Books []hcBook `json:"books"`
	}
	if err := h.do(ctx, query, map[string]any{"id": n}, &out); err != nil {
		return nil, err
	}
	if len(out.Books) == 0 {
		return nil, nil
	}
	m := out.Books[0].toMeta()
	return &m, nil
}

// byTitle resolves a title query through Hardcover's search endpoint
// (Typesense-backed, relevance-ranked). The former books(title: {_ilike})
// query is rejected by the live API ("ilike and related operations are not
// permitted on this server"), so search is the only title path.
//
// The query is the CLEANED TITLE ALONE: Goodreads-style "(… Series Book N)"
// suffixes are stripped and the author is not concatenated in — the live
// endpoint returns zero hits for a noisy title+author string ("The Hounds of
// Alton (The Alton Reed Series Book 1) D. R. Cole") even though the bare
// title matches fine. Author verification is scoreCandidate's job on the
// hydrated results. Only when the bare title verifies nothing (very generic
// titles crowded out of the top hits) does one retry append the author to
// disambiguate.
func (h *Hardcover) byTitle(ctx context.Context, title, author string, limit int) ([]BookMeta, error) {
	if limit <= 0 || limit > 20 {
		limit = 20
	}
	cleaned := StripSeriesSuffix(title)
	metas, err := h.searchVerified(ctx, cleaned, cleaned, author, limit)
	if err != nil || len(metas) > 0 || author == "" {
		return metas, err
	}
	return h.searchVerified(ctx, cleaned+" "+author, cleaned, author, limit)
}

// searchVerified runs one search query, hydrates the ranked hit ids with a
// books by-id query (the hits carry a trimmed document, so hydration keeps
// the shared field selection), and drops results that don't verify against
// the wanted title/author.
func (h *Hardcover) searchVerified(ctx context.Context, query, wantTitle, wantAuthor string, limit int) ([]BookMeta, error) {
	q := strings.TrimSpace(query)
	var sout struct {
		Search struct {
			Results json.RawMessage `json:"results"`
		} `json:"search"`
	}
	squery := `query ($q: String!, $per: Int!) {
	  search(query: $q, query_type: "Book", per_page: $per) { results }
	}`
	if err := h.do(ctx, squery, map[string]any{"q": q, "per": limit}, &sout); err != nil {
		return nil, err
	}
	var results struct {
		Hits []struct {
			Document struct {
				ID json.Number `json:"id"`
			} `json:"document"`
		} `json:"hits"`
	}
	if len(sout.Search.Results) > 0 {
		if err := json.Unmarshal(sout.Search.Results, &results); err != nil {
			return nil, fmt.Errorf("hardcover search results: %w", err)
		}
	}
	rank := map[string]int{}
	var ids []int64
	for _, hit := range results.Hits {
		id, err := hit.Document.ID.Int64()
		if err != nil {
			continue
		}
		if _, seen := rank[hit.Document.ID.String()]; !seen {
			rank[hit.Document.ID.String()] = len(ids)
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}

	hydrate := fmt.Sprintf(`query ($ids: [Int!]) {
	  books(where: {id: {_in: $ids}}) { %s }
	}`, hcBookFields)
	var out struct {
		Books []hcBook `json:"books"`
	}
	if err := h.do(ctx, hydrate, map[string]any{"ids": ids}, &out); err != nil {
		return nil, err
	}
	// restore search's relevance order — the by-id query returns any order
	sort.SliceStable(out.Books, func(i, j int) bool {
		return rank[out.Books[i].ID.String()] < rank[out.Books[j].ID.String()]
	})
	metas := make([]BookMeta, 0, len(out.Books))
	for _, b := range out.Books {
		metas = append(metas, b.toMeta())
	}
	// Verify fuzzy matches before trusting them. A bare search-bar query
	// (no structured author) freely mixes title and author words — "terminal
	// list cade rennick", or just an author's name — so scoring it against the
	// title alone rejects every correct hit and search falls through to
	// Open Library's messy editions. Those queries are trusted when every
	// query word appears in the hit's combined title + author text.
	filtered := metas[:0]
	for _, m := range metas {
		ok := scoreCandidate(m, wantTitle, wantAuthor) > 40
		if !ok && wantAuthor == "" {
			ok = queryCoveredBy(wantTitle, m.Title+" "+strings.Join(m.Authors, " "))
		}
		if ok {
			filtered = append(filtered, m)
		}
	}
	return filtered, nil
}

// queryCoveredBy reports whether every word of the query appears (as a whole
// word) in the candidate text.
func queryCoveredBy(query, text string) bool {
	ts := tokens(normalizeText(query))
	if len(ts) == 0 {
		return false
	}
	hay := " " + normalizeText(text) + " "
	for _, t := range ts {
		if !strings.Contains(hay, " "+t+" ") {
			return false
		}
	}
	return true
}

// HCList is one of the account's own lists, for the watched-list picker.
type HCList struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// MyLists returns the token owner's lists. The me field's shape has drifted
// between array and object in the beta schema, so both parse.
func (h *Hardcover) MyLists(ctx context.Context) ([]HCList, error) {
	if h.Token() == "" {
		return nil, fmt.Errorf("hardcover token not configured")
	}
	var out struct {
		Me json.RawMessage `json:"me"`
	}
	if err := h.do(ctx, `query { me { id username lists { id name books_count } } }`, nil, &out); err != nil {
		return nil, err
	}
	type hcUser struct {
		Lists []struct {
			ID         json.Number `json:"id"`
			Name       string      `json:"name"`
			BooksCount int         `json:"books_count"`
		} `json:"lists"`
	}
	var users []hcUser
	if err := json.Unmarshal(out.Me, &users); err != nil {
		var one hcUser
		if err := json.Unmarshal(out.Me, &one); err != nil {
			return nil, fmt.Errorf("hardcover me: unexpected shape")
		}
		users = []hcUser{one}
	}
	var lists []HCList
	for _, u := range users {
		for _, l := range u.Lists {
			lists = append(lists, HCList{ID: l.ID.String(), Name: l.Name, Count: l.BooksCount})
		}
	}
	return lists, nil
}

// SearchAuthorImages resolves author names to portrait URLs for a search
// query — one Author-type search against the same endpoint the book search
// uses. Best-effort: schema drift or missing images just yield fewer
// entries, never an error the caller must care about. Keys are lowercased
// names.
func (h *Hardcover) SearchAuthorImages(ctx context.Context, query string, limit int) map[string]string {
	if h.Token() == "" || strings.TrimSpace(query) == "" {
		return nil
	}
	if limit <= 0 || limit > 10 {
		limit = 5
	}
	var sout struct {
		Search struct {
			Results json.RawMessage `json:"results"`
		} `json:"search"`
	}
	squery := `query ($q: String!, $per: Int!) {
	  search(query: $q, query_type: "Author", per_page: $per) { results }
	}`
	if err := h.do(ctx, squery, map[string]any{"q": strings.TrimSpace(query), "per": limit}, &sout); err != nil {
		return nil
	}
	var results struct {
		Hits []struct {
			Document struct {
				Name  string          `json:"name"`
				Image json.RawMessage `json:"image"`
			} `json:"document"`
		} `json:"hits"`
	}
	if len(sout.Search.Results) == 0 || json.Unmarshal(sout.Search.Results, &results) != nil {
		return nil
	}
	images := map[string]string{}
	for _, hit := range results.Hits {
		if hit.Document.Name == "" || len(hit.Document.Image) == 0 {
			continue
		}
		// the image field has drifted between a bare string and {url: …}
		var url string
		if err := json.Unmarshal(hit.Document.Image, &url); err != nil {
			var obj struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal(hit.Document.Image, &obj); err != nil {
				continue
			}
			url = obj.URL
		}
		if url != "" {
			images[strings.ToLower(hit.Document.Name)] = url
		}
	}
	return images
}

// hcUsernameRe pulls the username out of whatever the user pasted: a
// hardcover.app profile/list URL ("hardcover.app/@alex/lists/…"), an
// "@alex" handle, or a bare username.
var hcUsernameRe = regexp.MustCompile(`@([A-Za-z0-9_.\-]+)|hardcover\.app/([A-Za-z0-9_.\-]+)`)

// ParseHardcoverUser extracts a username from a pasted URL or handle.
func ParseHardcoverUser(input string) (string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", fmt.Errorf("empty hardcover user")
	}
	if m := hcUsernameRe.FindStringSubmatch(input); m != nil {
		if m[1] != "" {
			return m[1], nil
		}
		return m[2], nil
	}
	if strings.ContainsAny(input, "/: ") {
		return "", fmt.Errorf("could not find a Hardcover username in %q — paste the profile URL or @username", input)
	}
	return input, nil
}

// UserLists returns another user's public lists by username. Visibility is
// enforced by the API — private lists simply don't come back.
func (h *Hardcover) UserLists(ctx context.Context, username string) ([]HCList, error) {
	if h.Token() == "" {
		return nil, fmt.Errorf("hardcover token not configured")
	}
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return nil, fmt.Errorf("empty hardcover username")
	}
	var out struct {
		Users []struct {
			Username string `json:"username"`
			Lists    []struct {
				ID         json.Number `json:"id"`
				Name       string      `json:"name"`
				BooksCount int         `json:"books_count"`
			} `json:"lists"`
		} `json:"users"`
	}
	q := `query ($u: citext!) { users(where: {username: {_eq: $u}}, limit: 1) { username lists { id name books_count } } }`
	if err := h.do(ctx, q, map[string]any{"u": username}, &out); err != nil {
		return nil, err
	}
	if len(out.Users) == 0 {
		return nil, fmt.Errorf("no Hardcover user named %q", username)
	}
	var lists []HCList
	for _, l := range out.Users[0].Lists {
		lists = append(lists, HCList{ID: l.ID.String(), Name: l.Name, Count: l.BooksCount})
	}
	return lists, nil
}

// AuthorWorks lists an author's bibliography by exact name match, most-read
// first. One query — well inside the rate limit.
func (h *Hardcover) AuthorWorks(ctx context.Context, authorName string, limit int) ([]BookMeta, error) {
	if h.Token() == "" || authorName == "" {
		return nil, nil // not configured — chain moves to the next provider
	}
	if limit <= 0 || limit > 200 {
		limit = 100
	}
	query := fmt.Sprintf(`query ($name: String!, $limit: Int!) {
	  books(where: {contributions: {author: {name: {_eq: $name}}}, %s},
	        order_by: {users_count: desc}, limit: $limit) { %s }
	}`, hcEnglishOrNull, hcBookFields)

	var out struct {
		Books []hcBook `json:"books"`
	}
	if err := h.do(ctx, query, map[string]any{"name": authorName, "limit": limit}, &out); err != nil {
		return nil, err
	}
	// Hardcover's catalog holds near-zero-usage duplicate records for
	// translated editions and retitled reprints, usually with no language
	// recorded anywhere — invisible to the English pre-filter. On live data
	// the separation is stark: every legitimate unknown-language work has
	// real readership while ghost records sit at 0–2 users. For an author
	// with an established readership, an unknown-language record must also
	// show a minimal reader count; explicitly-English records always pass.
	maxUsers := 0
	for _, b := range out.Books {
		if b.UsersCount > maxUsers {
			maxUsers = b.UsersCount
		}
	}
	ghostCutoff := 0
	if maxUsers >= 100 {
		ghostCutoff = min(maxUsers/100, 5)
	}
	metas := make([]BookMeta, 0, len(out.Books))
	for _, b := range out.Books {
		// belt over the server-side pre-filter: a bibliography must never
		// show foreign-language book records, even if the schema's filter
		// semantics drift
		m := b.toMeta()
		if !englishOrUnknown(m.Language) {
			continue
		}
		if ghostCutoff > 0 && !explicitlyEnglish(m.Language) && b.UsersCount <= ghostCutoff {
			continue
		}
		metas = append(metas, m)
	}
	return metas, nil
}

// HCAuthorInfo is Hardcover's presentation data for an author: portrait and
// biography, for the Authors pages.
type HCAuthorInfo struct {
	ID       string
	Name     string
	Bio      string
	ImageURL string
}

// AuthorInfo fetches an author's bio and portrait by exact name match (the
// same matching AuthorWorks uses). Most-read first so a common name resolves
// to the author the user actually reads; the beta schema drifts, so if the
// ordered query is rejected the plain one answers instead. Returns nil (no
// error) when the author isn't on Hardcover.
func (h *Hardcover) AuthorInfo(ctx context.Context, name string) (*HCAuthorInfo, error) {
	if h.Token() == "" || name == "" {
		return nil, nil
	}
	type hcAuthor struct {
		ID          json.Number     `json:"id"`
		Name        string          `json:"name"`
		Bio         string          `json:"bio"`
		CachedImage json.RawMessage `json:"cached_image"`
	}
	var out struct {
		Authors []hcAuthor `json:"authors"`
	}
	ordered := `query ($name: String!) {
	  authors(where: {name: {_eq: $name}}, order_by: {users_count: desc}, limit: 5) {
	    id name bio cached_image
	  }
	}`
	plain := `query ($name: String!) {
	  authors(where: {name: {_eq: $name}}, limit: 5) {
	    id name bio cached_image
	  }
	}`
	if err := h.do(ctx, ordered, map[string]any{"name": name}, &out); err != nil {
		out.Authors = nil
		if err := h.do(ctx, plain, map[string]any{"name": name}, &out); err != nil {
			return nil, err
		}
	}
	if len(out.Authors) == 0 {
		return nil, nil
	}
	// prefer the record that actually carries presentation data — duplicate
	// author rows with empty bios exist for many names
	best := out.Authors[0]
	for _, a := range out.Authors {
		if (a.Bio != "" || hcImageURL(a.CachedImage) != "") && best.Bio == "" && hcImageURL(best.CachedImage) == "" {
			best = a
		}
	}
	return &HCAuthorInfo{
		ID:       best.ID.String(),
		Name:     best.Name,
		Bio:      best.Bio,
		ImageURL: hcImageURL(best.CachedImage),
	}, nil
}

// ListBooks fetches every book on a Hardcover list (by numeric list id) for
// the watched-list poller. One query per poll — well inside the 60/min limit.
func (h *Hardcover) ListBooks(ctx context.Context, listID string) ([]BookMeta, error) {
	if h.Token() == "" {
		return nil, fmt.Errorf("hardcover token not configured")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(listID), 10, 64)
	if err != nil {
		return nil, fmt.Errorf("hardcover list id must be a number, got %q", listID)
	}
	query := fmt.Sprintf(`query ($id: Int!) {
	  list_books(where: {list_id: {_eq: $id}}, order_by: {position: asc}, limit: 500) {
	    book { %s }
	  }
	}`, hcBookFields)

	var out struct {
		ListBooks []struct {
			Book *hcBook `json:"book"`
		} `json:"list_books"`
	}
	if err := h.do(ctx, query, map[string]any{"id": id}, &out); err != nil {
		return nil, err
	}
	var metas []BookMeta
	for _, lb := range out.ListBooks {
		if lb.Book == nil {
			continue
		}
		metas = append(metas, lb.Book.toMeta())
	}
	return metas, nil
}

type hcBook struct {
	ID            json.Number     `json:"id"`
	Title         string          `json:"title"`
	Description   string          `json:"description"`
	ReleaseDate   string          `json:"release_date"`
	UsersCount    int             `json:"users_count"`
	Compilation   bool            `json:"compilation"`
	CachedTags    json.RawMessage `json:"cached_tags"`
	CachedImage   json.RawMessage `json:"cached_image"`
	Contributions []struct {
		Author struct {
			Name string `json:"name"`
		} `json:"author"`
	} `json:"contributions"`
	BookSeries []struct {
		Position json.Number `json:"position"`
		Series   struct {
			Name string `json:"name"`
		} `json:"series"`
	} `json:"book_series"`
	DefaultCoverEdition    *hcEdition `json:"default_cover_edition"`
	DefaultEbookEdition    *hcEdition `json:"default_ebook_edition"`
	DefaultPhysicalEdition *hcEdition `json:"default_physical_edition"`
}

// hcEdition carries the slice of an edition we ask for: its language.
type hcEdition struct {
	Language *struct {
		Language string `json:"language"` // human name, e.g. "English"
		Code3    string `json:"code3"`    // ISO 639-3, e.g. "eng"
	} `json:"language"`
}

// language reduces the default editions to one representative language name:
// the cover edition speaks for the book record, with ebook/physical editions
// consulted only when it carries no language. Empty means unknown.
func (b hcBook) language() string {
	for _, e := range []*hcEdition{b.DefaultCoverEdition, b.DefaultEbookEdition, b.DefaultPhysicalEdition} {
		if e == nil || e.Language == nil {
			continue
		}
		if e.Language.Language != "" {
			return e.Language.Language
		}
		if e.Language.Code3 != "" {
			return e.Language.Code3
		}
	}
	return ""
}

func (b hcBook) toMeta() BookMeta {
	m := BookMeta{
		Provider:     "hardcover",
		Title:        b.Title,
		Description:  b.Description,
		Language:     b.language(),
		ReleaseDate:  b.ReleaseDate,
		RatingsCount: b.UsersCount,
		HardcoverID:  b.ID.String(),
		CoverURL:     hcImageURL(b.CachedImage),
		Genres:       hcGenres(b.CachedTags),
		// title heuristics OR Hardcover's own curated flag — curation is
		// spotty, so neither signal replaces the other
		Compilation: b.Compilation || IsCompilation(b.Title),
	}
	for _, c := range b.Contributions {
		if c.Author.Name != "" {
			m.Authors = append(m.Authors, c.Author.Name)
		}
	}
	if len(b.BookSeries) > 0 {
		m.SeriesName = b.BookSeries[0].Series.Name
		if f, err := strconv.ParseFloat(b.BookSeries[0].Position.String(), 64); err == nil {
			m.SeriesIndex = f
		}
	}
	return m
}

// hcGenres extracts the "Genre" category from cached_tags, which arrives as
// {"Genre": [{"tag": "Thriller", ...}, ...], "Mood": [...], ...} — sometimes
// double-encoded as a JSON string. Capped at 6, matching Goodreads.
func hcGenres(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		raw = json.RawMessage(s)
	}
	var cats struct {
		Genre []struct {
			Tag string `json:"tag"`
		} `json:"Genre"`
	}
	if json.Unmarshal(raw, &cats) != nil {
		return nil
	}
	var genres []string
	for _, g := range cats.Genre {
		if g.Tag != "" && len(genres) < 6 {
			genres = append(genres, g.Tag)
		}
	}
	return genres
}

// cached_image arrives either as a bare URL string or {"url": "..."}.
func hcImageURL(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj struct {
		URL string `json:"url"`
	}
	if json.Unmarshal(raw, &obj) == nil {
		return obj.URL
	}
	return ""
}

// hcLimiter paces API calls under Hardcover's documented 60/min limit. A
// small burst keeps interactive search snappy; sustained callers (a 100-book
// list import, each entry costing two or three queries) drain the bucket and
// settle at the refill rate instead of tripping 429s that leave half the
// import unmatched.
type hcLimiter struct {
	mu     sync.Mutex
	tokens float64
	last   time.Time
}

const (
	hcRatePerSec = 0.85 // ~51/min sustained — headroom under the 60/min cap
	hcBurst      = 8
)

func (l *hcLimiter) wait(ctx context.Context) error {
	for {
		l.mu.Lock()
		now := time.Now()
		if l.last.IsZero() {
			l.tokens = hcBurst
		} else {
			l.tokens = math.Min(hcBurst, l.tokens+now.Sub(l.last).Seconds()*hcRatePerSec)
		}
		l.last = now
		if l.tokens >= 1 {
			l.tokens--
			l.mu.Unlock()
			return nil
		}
		wait := time.Duration((1 - l.tokens) / hcRatePerSec * float64(time.Second))
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

func (h *Hardcover) do(ctx context.Context, query string, vars map[string]any, out any) error {
	payload, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	if err := h.limiter.wait(ctx); err != nil {
		return err
	}
	body, status, err := h.post(ctx, payload)
	if err != nil {
		return err
	}
	// a 429 despite pacing (another client on the same token, server-side
	// bursts): wait out the window once rather than failing the caller
	if status == http.StatusTooManyRequests {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Second):
		}
		if err := h.limiter.wait(ctx); err != nil {
			return err
		}
		body, status, err = h.post(ctx, payload)
		if err != nil {
			return err
		}
	}
	if status >= 400 {
		return fmt.Errorf("hardcover status %d: %s", status, strings.TrimSpace(string(body[:min(len(body), 200)])))
	}
	var envelope struct {
		Data   json.RawMessage `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("hardcover response: %w", err)
	}
	if len(envelope.Errors) > 0 {
		return fmt.Errorf("hardcover: %s", envelope.Errors[0].Message)
	}
	return json.Unmarshal(envelope.Data, out)
}

func (h *Hardcover) post(ctx context.Context, payload []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.BaseURL, bytes.NewReader(payload))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+h.Token())
	res, err := h.Client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, res.StatusCode, err
	}
	return body, res.StatusCode, nil
}
