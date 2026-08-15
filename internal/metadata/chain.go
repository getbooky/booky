package metadata

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"
)

// Chain queries providers in the user's ranked order. Search returns the
// first provider's non-empty results (lower providers are fallbacks, not
// mixed in — mixing would duplicate the same books under different IDs).
// Enrich fills a matched book's gaps from every remaining provider, best
// first, using ISBN as the cross-provider bridge.
type Chain struct {
	providers map[string]Provider
	// Order returns the ranked provider keys at call time so settings
	// changes apply without restarts.
	Order func() []string
	// Exclude returns the user's extra exclusion terms (on top of the
	// built-in box-set/omnibus heuristics), fetched at call time.
	Exclude func() []string
	// PerProviderTimeout caps a single provider's enrich lookup so a blocked
	// or slow source can't starve the providers after it. Zero uses the
	// default.
	PerProviderTimeout time.Duration
}

func NewChain(order func() []string, providers ...Provider) *Chain {
	m := make(map[string]Provider, len(providers))
	for _, p := range providers {
		m[p.Key()] = p
	}
	return &Chain{providers: m, Order: order}
}

func (c *Chain) ranked() []Provider {
	var out []Provider
	seen := map[string]bool{}
	for _, key := range c.Order() {
		if p, ok := c.providers[key]; ok && !seen[key] {
			out = append(out, p)
			seen[key] = true
		}
	}
	// anything registered but unranked goes last, deterministically
	var rest []string
	for key := range c.providers {
		if !seen[key] {
			rest = append(rest, key)
		}
	}
	sort.Strings(rest)
	for _, key := range rest {
		out = append(out, c.providers[key])
	}
	return out
}

// userExclusions resolves the configured exclusion terms, if any.
func (c *Chain) userExclusions() []string {
	// no settings hook wired (tests, tools) → the stock term list applies
	if c.Exclude == nil {
		return DefaultExcludeTerms
	}
	return c.Exclude()
}

// enrichOnly marks a provider that must not originate primary search results
// or author bibliographies, but still participates in Enrich (by id/isbn/title
// gap-fill). Goodreads opts in: it stays the by-id detail source that fleshes
// out watched-list entries, while Hardcover drives what a search returns.
type enrichOnly interface {
	EnrichOnly() bool
}

func isEnrichOnly(p Provider) bool {
	eo, ok := p.(enrichOnly)
	return ok && eo.EnrichOnly()
}

// Search asks providers in order and returns the first non-empty answer,
// compilations and user-excluded titles filtered out. Enrich-only providers
// (Goodreads) never originate results here — they only fill gaps in Enrich.
func (c *Chain) Search(ctx context.Context, p SearchParams) ([]BookMeta, error) {
	var lastErr error
	for _, provider := range c.ranked() {
		if isEnrichOnly(provider) {
			continue
		}
		metas, err := provider.Search(ctx, p)
		if err != nil {
			log.Printf("metadata: %s search failed, trying next: %v", provider.Key(), err)
			lastErr = err
			continue
		}
		metas = c.dropExcluded(metas)
		if len(metas) > 0 {
			return metas, nil
		}
	}
	return nil, lastErr
}

// WorksLister is implemented by providers that can list an author's works.
type WorksLister interface {
	AuthorWorks(ctx context.Context, authorName string, limit int) ([]BookMeta, error)
}

// bibliographyGapFiller marks a WorksLister demoted to fallback duty: it
// never drives an author's bibliography while a configured primary lister
// exists, and only serves when every primary is unconfigured or erroring.
// Open Library opts in — it aggregates every global edition and printing, so
// its bibliographies are messy next to Hardcover's, but it keeps keyless
// setups working.
type bibliographyGapFiller interface {
	BibliographyGapFillOnly() bool
}

// worksConfigured lets a lister report whether it can answer at all (e.g. an
// API token is set). A lister without the method counts as configured.
type worksConfigured interface {
	WorksConfigured() bool
}

func isGapFiller(p Provider) bool {
	gf, ok := p.(bibliographyGapFiller)
	return ok && gf.BibliographyGapFillOnly()
}

func isConfiguredLister(p Provider) bool {
	if _, ok := p.(WorksLister); !ok {
		return false
	}
	if isEnrichOnly(p) {
		return false // enrich-only providers never drive a bibliography
	}
	if wc, ok := p.(worksConfigured); ok {
		return wc.WorksConfigured()
	}
	return true
}

// AuthorWorks fetches an author's bibliography from the best-ranked provider
// that supports it, deduplicated by normalized title and with excluded
// titles (box sets, user patterns, derivative junk) dropped.
//
// A configured primary lister owns the answer: even its empty result is
// returned as-is rather than letting a gap-fill provider (Open Library)
// substitute a lower-quality bibliography — an authoritative "nothing found"
// beats a junk-prone guess. Gap-fillers serve only when no primary is
// configured, or every primary errored.
func (c *Chain) AuthorWorks(ctx context.Context, authorName string, limit int) ([]BookMeta, error) {
	ranked := c.ranked()
	hasPrimary := anyConfiguredPrimary(ranked)
	var primaries, gapFillers []Provider
	for _, p := range ranked {
		if _, ok := p.(WorksLister); !ok {
			continue
		}
		if isEnrichOnly(p) {
			continue // enrich-only providers never originate a bibliography
		}
		if isGapFiller(p) && hasPrimary {
			gapFillers = append(gapFillers, p)
		} else {
			primaries = append(primaries, p)
		}
	}
	var lastErr error
	for _, provider := range primaries {
		keep, err := c.authorWorksFrom(ctx, provider, authorName, limit)
		if err != nil {
			log.Printf("metadata: %s author works failed, trying next: %v", provider.Key(), err)
			lastErr = err
			continue
		}
		// a configured primary's answer is final even when empty (see above);
		// otherwise keep falling through until something answers
		if len(keep) > 0 || isConfiguredLister(provider) {
			return keep, nil
		}
	}
	// every primary errored or was unconfigured — gap-fillers may rescue
	for _, provider := range gapFillers {
		keep, err := c.authorWorksFrom(ctx, provider, authorName, limit)
		if err != nil {
			log.Printf("metadata: %s author works failed, trying next: %v", provider.Key(), err)
			lastErr = err
			continue
		}
		if len(keep) > 0 {
			return keep, nil
		}
	}
	return nil, lastErr
}

func anyConfiguredPrimary(ranked []Provider) bool {
	for _, p := range ranked {
		if isConfiguredLister(p) && !isGapFiller(p) {
			return true
		}
	}
	return false
}

// authorWorksFrom queries one lister and applies the shared cleanup: excluded
// titles dropped, edition variants deduped, sibling omnibuses removed.
func (c *Chain) authorWorksFrom(ctx context.Context, provider Provider, authorName string, limit int) ([]BookMeta, error) {
	metas, err := provider.(WorksLister).AuthorWorks(ctx, authorName, limit)
	if err != nil {
		return nil, err
	}
	metas = c.dropExcluded(metas)
	// Dedupe a book's edition variants ("In the Blood", "In the Blood: A
	// Thriller", "In the Blood: James Reece 5") to one row using a
	// series-aware key that never merges distinct books in the same
	// series. Merged variants keep the cleanest (shortest) title while
	// series info and identifiers carry over from whichever variant had
	// them.
	seen := map[string]int{}
	keep := metas[:0:0]
	for _, m := range metas {
		if isDerivative(m.Title) {
			continue
		}
		// chain-level language belt: whatever provider answered, a recorded
		// non-English language never enters a bibliography
		if !englishOrUnknown(m.Language) {
			continue
		}
		// A title with no Latin characters at all (Cyrillic, CJK, Arabic, …)
		// is a translated edition's record unless the provider explicitly says
		// it's English — providers often leave language empty on exactly these
		// records, which slips them past the language filters and parks them
		// next to the English original forever. General search keeps them (the
		// dedupe-key fallback exists for that); a bibliography must not.
		if normalizeText(m.Title) == "" && !explicitlyEnglish(m.Language) {
			continue
		}
		key := dedupeKey(m.Title, m.SeriesName)
		if key == "" {
			continue
		}
		if idx, ok := seen[key]; ok {
			cur := &keep[idx]
			if cur.SeriesName == "" && m.SeriesName != "" {
				cur.SeriesName, cur.SeriesIndex = m.SeriesName, m.SeriesIndex
			}
			if cur.ISBN13 == "" {
				cur.ISBN13 = m.ISBN13
			}
			if cur.GoodreadsID == "" {
				cur.GoodreadsID = m.GoodreadsID
			}
			if cur.CoverURL == "" {
				cur.CoverURL = m.CoverURL
			}
			if cur.Description == "" {
				cur.Description = m.Description
			}
			// the canonical record carries the readers: between edition
			// variants, the most-rated one's title wins ("Nora Vale and
			// the Body in the Garden" over a bare "The Body in the Garden");
			// among equals the shortest stays
			if m.RatingsCount > cur.RatingsCount {
				cur.Title, cur.RatingsCount = m.Title, m.RatingsCount
			} else if m.RatingsCount == cur.RatingsCount && len(m.Title) < len(cur.Title) {
				cur.Title = m.Title
			}
			continue
		}
		seen[key] = len(keep)
		keep = append(keep, m)
	}
	return dropSiblingOmnibuses(keep), nil
}

// goodreadsByID is implemented by the Goodreads provider: given the book's
// Goodreads ID (which list feeds hand us directly), it fetches that exact
// book's detail page — more reliable than re-searching by title.
type goodreadsByID interface {
	FetchBook(ctx context.Context, id string) (*BookMeta, bool, error)
}

// hardcoverByID is implemented by the Hardcover provider: given the book's
// canonical Hardcover ID, it fetches that exact book — an identity lookup
// that can never wander to a same-titled book by someone else the way a
// title search can.
type hardcoverByID interface {
	FetchByHardcoverID(ctx context.Context, id string) (*BookMeta, error)
}

// defaultPerProviderTimeout caps one provider's lookup so a blocked or slow
// source (Goodreads is frequently WAF-gated) can't starve the providers
// ranked after it — the symptom being "put Goodreads first, nothing fills in".
const defaultPerProviderTimeout = 12 * time.Second

// Enrich fills meta's empty fields from every provider that hasn't answered
// yet, in ranked order. Each provider is looked up with the ORIGINAL seed
// identifiers — never an ISBN a higher provider happened to supply — so a
// mismatched edition from one source can't misdirect the next. A missing
// ISBN edition falls back to a verified title/author match.
func (c *Chain) Enrich(ctx context.Context, meta BookMeta) BookMeta {
	seedISBN := meta.ISBN13
	seedTitle := meta.Title
	seedAuthor := ""
	if len(meta.Authors) > 0 {
		seedAuthor = meta.Authors[0]
	}
	seedGoodreadsID := meta.GoodreadsID
	seedHardcoverID := meta.HardcoverID

	timeout := c.PerProviderTimeout
	if timeout <= 0 {
		timeout = defaultPerProviderTimeout
	}
	for _, provider := range c.ranked() {
		if provider.Key() == meta.Provider {
			continue
		}
		if complete(meta) {
			break
		}
		pctx, cancel := context.WithTimeout(ctx, timeout)
		candidate, ok := c.lookup(pctx, provider, seedISBN, seedTitle, seedAuthor, seedGoodreadsID, seedHardcoverID)
		cancel()
		if ok {
			Merge(&meta, candidate)
		}
	}
	return meta
}

// lookup resolves one provider's view of the book, preferring the strongest
// key available and falling back through weaker ones: the provider's own
// book ID (exact — Hardcover ID for Hardcover, Goodreads ID for Goodreads),
// then ISBN (exact), then a fuzzy-verified title/author match.
func (c *Chain) lookup(ctx context.Context, provider Provider, isbn, title, author, goodreadsID, hardcoverID string) (BookMeta, bool) {
	// exact detail fetch when we hold the provider's own id
	if hardcoverID != "" {
		if hc, ok := provider.(hardcoverByID); ok {
			if m, err := hc.FetchByHardcoverID(ctx, hardcoverID); err == nil && m != nil && m.Title != "" {
				return *m, true
			}
		}
	}
	if goodreadsID != "" {
		if gr, ok := provider.(goodreadsByID); ok {
			if m, _, err := gr.FetchBook(ctx, goodreadsID); err == nil && m != nil && m.Title != "" {
				return *m, true
			}
		}
	}
	if isbn != "" {
		if r, err := provider.Search(ctx, SearchParams{ISBN: isbn, Limit: 1}); err == nil && len(r) > 0 {
			return r[0], true
		}
	}
	if title == "" {
		return BookMeta{}, false
	}
	r, err := provider.Search(ctx, SearchParams{Title: title, Author: author, Limit: 5})
	if err != nil || len(r) == 0 {
		return BookMeta{}, false
	}
	return bestEnrichMatch(r, title, author)
}

// dropSiblingOmnibuses removes a bibliography entry whose title contains two
// or more OTHER entries' titles as substrings — a data-driven omnibus signal
// ("Into the Storm, Cold Harbor, and Winter Watch" contains three sibling
// titles) that, unlike a comma/"and" regex, can't misfire on a real title
// like "The Key, the Door, and the Garden" (which contains no sibling
// book titles). Requiring two siblings keeps nested titles ("The Storm" in
// "The Eye of the Storm") from tripping it.
func dropSiblingOmnibuses(books []BookMeta) []BookMeta {
	norm := make([]string, len(books))
	for i, b := range books {
		norm[i] = normalizeText(b.Title)
	}
	out := books[:0:0]
	for i, b := range books {
		siblings := 0
		for j := range books {
			if i == j || len(norm[j]) < 6 || len(norm[j]) >= len(norm[i]) {
				continue
			}
			if strings.Contains(norm[i], norm[j]) {
				siblings++
			}
		}
		if siblings < 2 {
			out = append(out, b)
		}
	}
	return out
}

func complete(m BookMeta) bool {
	return m.Title != "" && len(m.Authors) > 0 && m.Description != "" &&
		m.ISBN13 != "" && m.GoodreadsID != "" && m.HardcoverID != "" &&
		m.CoverURL != "" && m.ReleaseDate != ""
}

func (c *Chain) dropExcluded(metas []BookMeta) []BookMeta {
	patterns := c.userExclusions()
	out := metas[:0]
	for _, m := range metas {
		if m.Compilation || Excluded(m.Title, patterns) {
			continue
		}
		out = append(out, m)
	}
	return out
}
