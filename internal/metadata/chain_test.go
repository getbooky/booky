package metadata

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeProvider struct {
	key     string
	results []BookMeta
	err     error
	calls   int
}

func (f *fakeProvider) Key() string { return f.key }
func (f *fakeProvider) Search(ctx context.Context, p SearchParams) ([]BookMeta, error) {
	f.calls++
	return f.results, f.err
}

func TestChainFirstNonEmptyWins(t *testing.T) {
	a := &fakeProvider{key: "a"}
	b := &fakeProvider{key: "b", results: []BookMeta{{Provider: "b", Title: "Loom"}}}
	c := &fakeProvider{key: "c", results: []BookMeta{{Provider: "c", Title: "Wrong"}}}
	chain := NewChain(func() []string { return []string{"a", "b", "c"} }, a, b, c)

	results, err := chain.Search(context.Background(), SearchParams{Title: "Loom"})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) != 1 || results[0].Provider != "b" {
		t.Fatalf("got %+v, want b's result", results)
	}
	if c.calls != 0 {
		t.Error("provider c should not have been called")
	}
}

func TestChainFailureDegrades(t *testing.T) {
	a := &fakeProvider{key: "a", err: errors.New("waf")}
	b := &fakeProvider{key: "b", results: []BookMeta{{Provider: "b", Title: "Loom"}}}
	chain := NewChain(func() []string { return []string{"a", "b"} }, a, b)

	results, err := chain.Search(context.Background(), SearchParams{Title: "Loom"})
	if err != nil {
		t.Fatalf("Search should succeed via fallback: %v", err)
	}
	if len(results) != 1 || results[0].Provider != "b" {
		t.Fatalf("got %+v", results)
	}
}

func TestChainRespectsUserOrder(t *testing.T) {
	a := &fakeProvider{key: "a", results: []BookMeta{{Provider: "a", Title: "Loom"}}}
	b := &fakeProvider{key: "b", results: []BookMeta{{Provider: "b", Title: "Loom"}}}
	chain := NewChain(func() []string { return []string{"b", "a"} }, a, b)

	results, _ := chain.Search(context.Background(), SearchParams{Title: "Loom"})
	if results[0].Provider != "b" {
		t.Errorf("user ranked b first; got %s", results[0].Provider)
	}
}

func TestChainFiltersCompilations(t *testing.T) {
	a := &fakeProvider{key: "a", results: []BookMeta{
		{Provider: "a", Title: "Vault Boxed Set (Books 1-3)"},
		{Provider: "a", Title: "Loom"},
	}}
	chain := NewChain(func() []string { return []string{"a"} }, a)

	results, _ := chain.Search(context.Background(), SearchParams{Title: "Loom"})
	if len(results) != 1 || results[0].Title != "Loom" {
		t.Fatalf("compilation should be filtered, got %+v", results)
	}
}

func TestChainEnrichMergesByPriority(t *testing.T) {
	gr := &fakeProvider{key: "goodreads"} // already the source; skipped
	hc := &fakeProvider{key: "hardcover", results: []BookMeta{{
		Provider: "hardcover", Title: "Loom", HardcoverID: "612", SeriesName: "Vault", SeriesIndex: 1,
	}}}
	chain := NewChain(func() []string { return []string{"goodreads", "hardcover"} }, gr, hc)

	meta := chain.Enrich(context.Background(), BookMeta{
		Provider: "goodreads", Title: "Loom", Authors: []string{"Tess Arden"},
		GoodreadsID: "20240117", ISBN13: "9781650000037", Description: "vault",
	})
	if meta.HardcoverID != "612" {
		t.Errorf("hardcover id not merged: %+v", meta)
	}
	if meta.SeriesName != "Vault" || meta.SeriesIndex != 1 {
		t.Errorf("series not merged: %+v", meta)
	}
	if meta.GoodreadsID != "20240117" {
		t.Error("existing identity must not be overwritten")
	}
	if gr.calls != 0 {
		t.Error("source provider should be skipped during enrich")
	}
}

// fakeWorksProvider also implements WorksLister.
type fakeWorksProvider struct {
	fakeProvider
	works []BookMeta
}

func (f *fakeWorksProvider) AuthorWorks(ctx context.Context, name string, limit int) ([]BookMeta, error) {
	return f.works, f.err
}

func TestChainUserExclusions(t *testing.T) {
	p := &fakeProvider{key: "a", results: []BookMeta{
		{Title: "First Ember"},
		{Title: "First Ember Box Set 1-3"},         // via the (editable) term list
		{Title: "First Ember Large Print Edition"}, // user-added term
	}}
	c := NewChain(func() []string { return []string{"a"} }, p)
	c.Exclude = func() []string { return []string{"large print", "box set"} }
	got, err := c.Search(context.Background(), SearchParams{Query: "first ember"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "First Ember" {
		t.Fatalf("exclusions not applied: %+v", got)
	}

	// removing a default term really removes it: without "box set" in the
	// list, the box set is allowed through
	c.Exclude = func() []string { return []string{"large print"} }
	got, err = c.Search(context.Background(), SearchParams{Query: "first ember"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("removed default term should stop filtering: %+v", got)
	}
}

func TestChainAuthorWorks(t *testing.T) {
	worksless := &fakeProvider{key: "first"}
	lister := &fakeWorksProvider{
		fakeProvider: fakeProvider{key: "second"},
		works: []BookMeta{
			{Title: "Loom"},
			{Title: "loom"},                     // dedupe by normalized title
			{Title: "Vault Books 1-3"},          // structural compilation
			{Title: "Loom: Summary & Analysis"}, // derivative junk
			{Title: "Drift"},
			{Title: "Haze Special Edition"}, // user pattern below
		},
	}
	c := NewChain(func() []string { return []string{"first", "second"} }, worksless, lister)
	c.Exclude = func() []string { return []string{"special edition"} }

	got, err := c.AuthorWorks(context.Background(), "Tess Arden", 50)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, m := range got {
		titles = append(titles, m.Title)
	}
	if len(titles) != 2 || titles[0] != "Loom" || titles[1] != "Drift" {
		t.Fatalf("author works filtering wrong: %v", titles)
	}
}

// paramFake responds based on the search params, so tests can prove which
// key (ISBN vs title/author) a provider was queried with.
type paramFake struct {
	key              string
	fn               func(p SearchParams) []BookMeta
	blockUntilCancel bool
}

func (f *paramFake) Key() string { return f.key }
func (f *paramFake) Search(ctx context.Context, p SearchParams) ([]BookMeta, error) {
	if f.blockUntilCancel {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.fn == nil {
		return nil, nil
	}
	return f.fn(p), nil
}

// grFake also serves a detail-by-id fetch, standing in for the real
// Goodreads provider's FetchBook.
type grFake struct {
	paramFake
	byID func(id string) *BookMeta
}

func (f *grFake) FetchBook(ctx context.Context, id string) (*BookMeta, bool, error) {
	if f.byID == nil {
		return nil, true, nil
	}
	return f.byID(id), true, nil
}

// The core regression: a higher provider that returns a DIFFERENT edition's
// ISBN must not misdirect a lower provider's lookup. Description must fill
// regardless of provider order.
func TestEnrichOrderIndependentDescription(t *testing.T) {
	// "gr" returns a thin, wrong-edition record: a mismatched ISBN, no blurb.
	gr := &paramFake{key: "gr", fn: func(p SearchParams) []BookMeta {
		return []BookMeta{{Provider: "gr", Title: "Loom", Authors: []string{"Tess Arden"}, ISBN13: "9999999999999"}}
	}}
	// "hc" has the description, but only answers to the ORIGINAL isbn or a
	// title/author match — never gr's bogus isbn.
	hc := &paramFake{key: "hc", fn: func(p SearchParams) []BookMeta {
		if p.ISBN == "9999999999999" {
			return nil // the poisoned lookup finds nothing
		}
		if p.ISBN == "9780000000001" || (p.Title == "Loom" && p.Author == "Tess Arden") {
			return []BookMeta{{Provider: "hc", Title: "Loom", Description: "Vault.", HardcoverID: "1"}}
		}
		return nil
	}}

	seed := BookMeta{Provider: "seed", Title: "Loom", Authors: []string{"Tess Arden"}, ISBN13: "9780000000001"}

	for _, order := range [][]string{{"gr", "hc"}, {"hc", "gr"}} {
		chain := NewChain(func() []string { return order }, gr, hc)
		got := chain.Enrich(context.Background(), seed)
		if got.Description != "Vault." {
			t.Fatalf("order %v: description not filled, got %q", order, got.Description)
		}
	}
}

// With a Goodreads ID in hand, enrich fetches that exact book's detail page
// (description included) rather than re-searching by title.
func TestEnrichUsesGoodreadsByID(t *testing.T) {
	gr := &grFake{
		paramFake: paramFake{key: "goodreads"},
		byID: func(id string) *BookMeta {
			if id != "77345210" {
				return nil
			}
			return &BookMeta{Provider: "goodreads", Title: "First Ember", Description: "Dragons.", GoodreadsID: id}
		},
	}
	chain := NewChain(func() []string { return []string{"goodreads"} }, gr)
	// a thin list-feed seed: id + title, no description
	seed := BookMeta{Provider: "goodreads_rss", Title: "First Ember", GoodreadsID: "77345210"}
	got := chain.Enrich(context.Background(), seed)
	if got.Description != "Dragons." {
		t.Fatalf("by-id detail not used: %q", got.Description)
	}
}

// A blocked/slow provider must not starve the ones after it: enrich caps
// each provider and still fills from a later, responsive source.
func TestEnrichSlowProviderDoesNotStarve(t *testing.T) {
	slow := &paramFake{key: "slow", blockUntilCancel: true}
	fast := &paramFake{key: "fast", fn: func(p SearchParams) []BookMeta {
		return []BookMeta{{Provider: "fast", Title: "Loom", Description: "Vault."}}
	}}
	// slow ranked first; it would hang forever without the per-provider cap
	chain := NewChain(func() []string { return []string{"slow", "fast"} }, slow, fast)
	chain.PerProviderTimeout = 100 * time.Millisecond // shrink so the test is fast

	seed := BookMeta{Provider: "seed", Title: "Loom", Authors: []string{"Tess Arden"}}
	done := make(chan BookMeta, 1)
	go func() { done <- chain.Enrich(context.Background(), seed) }()
	select {
	case got := <-done:
		if got.Description != "Vault." {
			t.Fatalf("later provider did not fill: %q", got.Description)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("enrich hung on the slow provider")
	}
}

// gapFillFake is a WorksLister demoted to gap-fill duty, like Open Library.
type gapFillFake struct {
	fakeWorksProvider
	called bool
}

func (f *gapFillFake) BibliographyGapFillOnly() bool { return true }
func (f *gapFillFake) AuthorWorks(ctx context.Context, name string, limit int) ([]BookMeta, error) {
	f.called = true
	return f.works, f.err
}

// primaryFake is a WorksLister whose configured state is switchable, like
// Hardcover with/without a token.
type primaryFake struct {
	fakeWorksProvider
	configured bool
}

func (f *primaryFake) WorksConfigured() bool { return f.configured }

func TestAuthorWorksGapFillerSkippedWhenPrimaryConfigured(t *testing.T) {
	primary := &primaryFake{
		fakeWorksProvider: fakeWorksProvider{fakeProvider: fakeProvider{key: "hc"},
			works: []BookMeta{{Title: "Iron Dawn"}}},
		configured: true,
	}
	gap := &gapFillFake{fakeWorksProvider: fakeWorksProvider{fakeProvider: fakeProvider{key: "ol"},
		works: []BookMeta{{Title: "Alba de Hierro"}}}}
	c := NewChain(func() []string { return []string{"hc", "ol"} }, primary, gap)

	got, err := c.AuthorWorks(context.Background(), "Cole Merrick", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Iron Dawn" {
		t.Fatalf("want primary's bibliography, got %+v", got)
	}
	if gap.called {
		t.Error("gap-fill lister must not be queried while a primary is configured")
	}
}

func TestAuthorWorksPrimaryEmptyIsAuthoritative(t *testing.T) {
	primary := &primaryFake{
		fakeWorksProvider: fakeWorksProvider{fakeProvider: fakeProvider{key: "hc"}},
		configured:        true, // configured but knows nothing about this author
	}
	gap := &gapFillFake{fakeWorksProvider: fakeWorksProvider{fakeProvider: fakeProvider{key: "ol"},
		works: []BookMeta{{Title: "Junk Guess"}}}}
	c := NewChain(func() []string { return []string{"hc", "ol"} }, primary, gap)

	got, err := c.AuthorWorks(context.Background(), "Unknown Author", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("configured primary's empty answer must stand, got %+v", got)
	}
	if gap.called {
		t.Error("gap-fill lister must not override a configured primary's empty answer")
	}
}

func TestAuthorWorksGapFillerServesKeylessSetups(t *testing.T) {
	primary := &primaryFake{
		fakeWorksProvider: fakeWorksProvider{fakeProvider: fakeProvider{key: "hc"}},
		configured:        false, // no token
	}
	gap := &gapFillFake{fakeWorksProvider: fakeWorksProvider{fakeProvider: fakeProvider{key: "ol"},
		works: []BookMeta{{Title: "Loom"}}}}
	c := NewChain(func() []string { return []string{"hc", "ol"} }, primary, gap)

	got, err := c.AuthorWorks(context.Background(), "Tess Arden", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Loom" {
		t.Fatalf("keyless fallback should serve, got %+v", got)
	}
}

func TestAuthorWorksGapFillerServesWhenPrimaryErrors(t *testing.T) {
	primary := &primaryFake{
		fakeWorksProvider: fakeWorksProvider{fakeProvider: fakeProvider{key: "hc", err: errors.New("api down")}},
		configured:        true,
	}
	gap := &gapFillFake{fakeWorksProvider: fakeWorksProvider{fakeProvider: fakeProvider{key: "ol"},
		works: []BookMeta{{Title: "Loom"}}}}
	c := NewChain(func() []string { return []string{"hc", "ol"} }, primary, gap)

	got, err := c.AuthorWorks(context.Background(), "Tess Arden", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Title != "Loom" {
		t.Fatalf("gap filler should rescue an erroring primary, got %+v", got)
	}
}

// enrichOnlyFake originates search results but is flagged enrich-only, like
// Goodreads: Search must skip it, Enrich must still use it.
type enrichOnlyFake struct {
	fakeProvider
	searchCalls int
}

func (f *enrichOnlyFake) EnrichOnly() bool { return true }
func (f *enrichOnlyFake) Search(ctx context.Context, p SearchParams) ([]BookMeta, error) {
	f.searchCalls++
	return f.fakeProvider.Search(ctx, p)
}

func TestSearchSkipsEnrichOnlyProvider(t *testing.T) {
	gr := &enrichOnlyFake{fakeProvider: fakeProvider{key: "goodreads",
		results: []BookMeta{{Provider: "goodreads", Title: "Loom (Goodreads)"}}}}
	hc := &fakeProvider{key: "hardcover",
		results: []BookMeta{{Provider: "hardcover", Title: "Loom"}}}
	c := NewChain(func() []string { return []string{"goodreads", "hardcover"} }, gr, hc)

	got, err := c.Search(context.Background(), SearchParams{Query: "loom"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Provider != "hardcover" {
		t.Fatalf("search should be driven by hardcover, got %+v", got)
	}
	if gr.searchCalls != 0 {
		t.Errorf("enrich-only Goodreads must not originate search, called %d times", gr.searchCalls)
	}
}

// The wrong-book regression: a same-titled book by a COMPLETELY different
// author arrives as the provider's top title-search hit (an exact title match
// alone scores 100), and enrich used to merge its identity, series, and cover
// into the record — turning a correct book page into a chimera of the stored
// author and the wrong book's everything-else.
func TestEnrichRejectsSameTitleDifferentAuthor(t *testing.T) {
	hc := &paramFake{key: "hardcover", fn: func(p SearchParams) []BookMeta {
		if p.Title == "" {
			return nil
		}
		return []BookMeta{{
			Provider: "hardcover", Title: "The Current", Authors: []string{"Wade Merrell"},
			HardcoverID: "1552814", SeriesName: "Current", SeriesIndex: 1,
			CoverURL: "https://img.example/wrong.jpg", ReleaseDate: "2011-09-01", RatingsCount: 2100,
		}}
	}}
	chain := NewChain(func() []string { return []string{"hardcover"} }, hc)

	seed := BookMeta{Provider: "refresh", Title: "The Current", Authors: []string{"Nora Whitfield"}}
	got := chain.Enrich(context.Background(), seed)
	if got.HardcoverID != "" || got.SeriesName != "" || got.CoverURL != "" {
		t.Fatalf("wrong-author book must not merge, got %+v", got)
	}
}

// With several hits in hand, the author-verified match wins even when a more
// popular same-titled book by someone else outranks it in search relevance.
func TestEnrichPicksAuthorMatchBelowTopHit(t *testing.T) {
	hc := &paramFake{key: "hardcover", fn: func(p SearchParams) []BookMeta {
		if p.Title == "" {
			return nil
		}
		return []BookMeta{
			{Provider: "hardcover", Title: "The Current", Authors: []string{"Wade Merrell"}, HardcoverID: "1", RatingsCount: 2100},
			{Provider: "hardcover", Title: "The Current", Authors: []string{"Nora Whitfield"}, HardcoverID: "2", Description: "The right one."},
		}
	}}
	chain := NewChain(func() []string { return []string{"hardcover"} }, hc)

	seed := BookMeta{Provider: "refresh", Title: "The Current", Authors: []string{"Nora Whitfield"}}
	got := chain.Enrich(context.Background(), seed)
	if got.HardcoverID != "2" || got.Description != "The right one." {
		t.Fatalf("author-verified hit should win, got %+v", got)
	}
}

// hcByIDFake stands in for the Hardcover provider's exact by-id fetch.
type hcByIDFake struct {
	paramFake
	byID func(id string) *BookMeta
}

func (f *hcByIDFake) FetchByHardcoverID(ctx context.Context, id string) (*BookMeta, error) {
	if f.byID == nil {
		return nil, nil
	}
	return f.byID(id), nil
}

// Holding the canonical Hardcover ID, enrich fetches that exact book rather
// than gambling on a title search that could land on a same-titled stranger.
func TestEnrichUsesHardcoverByID(t *testing.T) {
	titleSearches := 0
	hc := &hcByIDFake{
		paramFake: paramFake{key: "hardcover", fn: func(p SearchParams) []BookMeta {
			if p.Title != "" {
				titleSearches++
			}
			return []BookMeta{{Provider: "hardcover", Title: "The Current", Authors: []string{"Wade Merrell"}, HardcoverID: "999"}}
		}},
		byID: func(id string) *BookMeta {
			if id != "42" {
				return nil
			}
			return &BookMeta{Provider: "hardcover", Title: "The Current", Authors: []string{"Nora Whitfield"}, HardcoverID: "42", Description: "Exact record."}
		},
	}
	chain := NewChain(func() []string { return []string{"hardcover"} }, hc)

	seed := BookMeta{Provider: "refresh", Title: "The Current", Authors: []string{"Nora Whitfield"}, HardcoverID: "42"}
	got := chain.Enrich(context.Background(), seed)
	if got.Description != "Exact record." {
		t.Fatalf("by-id fetch not used: %+v", got)
	}
	if titleSearches != 0 {
		t.Errorf("title search ran %d times despite an exact by-id answer", titleSearches)
	}
}

func TestEnrichStillUsesEnrichOnlyProvider(t *testing.T) {
	// Hardcover is the seed; Goodreads (enrich-only) must still fill the gap
	gr := &enrichOnlyFake{fakeProvider: fakeProvider{key: "goodreads",
		results: []BookMeta{{Provider: "goodreads", Title: "Loom", Description: "Vault.", GoodreadsID: "1"}}}}
	c := NewChain(func() []string { return []string{"hardcover", "goodreads"} }, gr)

	seed := BookMeta{Provider: "hardcover", Title: "Loom", Authors: []string{"Tess Arden"}}
	got := c.Enrich(context.Background(), seed)
	if got.Description != "Vault." {
		t.Fatalf("enrich-only provider should fill description, got %+v", got)
	}
	if gr.searchCalls == 0 {
		t.Error("enrich-only provider should be queried during Enrich")
	}
}
