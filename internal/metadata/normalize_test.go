package metadata

import (
	"context"
	"testing"
)

// Edition variants of one book collapse to a single key, using the series
// name to safely strip the series designation.
func TestDedupeKeyCollapsesEditions(t *testing.T) {
	type v struct{ title, series string }
	groups := [][]v{
		{{"In the Smoke", ""}, {"In the Smoke: A Thriller", ""}, {"In the Smoke: Mason Holt 5", "Mason Holt"}},
		{{"Only the Lost", ""}, {"Only the Lost: A Thriller", ""}},
		{{"The Raven's Hand", ""}, {"The Raven's Hand: Mason Holt 4", "Mason Holt"}, {"The Raven's Hand: Mason Holt, Book 4", "Mason Holt"}},
	}
	for _, g := range groups {
		want := dedupeKey(g[0].title, g[0].series)
		for _, x := range g[1:] {
			if got := dedupeKey(x.title, x.series); got != want {
				t.Errorf("%q -> %q, want %q (same book as %q)", x.title, got, want, g[0].title)
			}
		}
	}
}

// The critical regression: distinct books in the SAME series must NOT
// collapse just because they share a colon-prefixed series name.
func TestDedupeKeyKeepsSeriesEntriesDistinct(t *testing.T) {
	ashveil := []string{"Ashveil: The Last Bastion", "Ashveil: The Well of Sorrows", "Ashveil: The Heir of Storms"}
	keys := map[string]bool{}
	for _, tt := range ashveil {
		k := dedupeKey(tt, "Ashveil")
		if keys[k] {
			t.Fatalf("distinct Ashveil books collapsed to the same key %q", k)
		}
		keys[k] = true
	}
	// non-Latin titles must produce a stable, non-empty key (never dropped)
	if dedupeKey("灰烬之城", "") == "" {
		t.Error("non-Latin title produced an empty dedupe key")
	}
	if dedupeKey("灰烬之城", "") == dedupeKey("Тихий берег", "") {
		t.Error("different non-Latin titles collapsed")
	}
}

func TestCompilationKeywordsOnly(t *testing.T) {
	yes := []string{
		"Vault Boxed Set (Books 1-3)",
		"The Ember Cycle Omnibus",
		"The Complete Collection",
	}
	for _, tt := range yes {
		if !Excluded(tt, DefaultExcludeTerms) {
			t.Errorf("should be flagged as a compilation: %q", tt)
		}
	}
	// real single books that a comma/"and" heuristic would wrongly hide
	no := []string{
		"In the Smoke",
		"Run, Swim, Fly",
		"Boy, Uninvited",
		"The Fox, the Crow, and the Lantern",
		"Sweat, Steel, and Circuits",
		"Ink, Static, and Marmalade",
		"Only the Lost: A Thriller",
		"Recollection", // word boundary: "collection" must not bite inside a word
	}
	for _, tt := range no {
		if Excluded(tt, DefaultExcludeTerms) {
			t.Errorf("should NOT be flagged (real book): %q", tt)
		}
	}
}

// End to end through AuthorWorks: a provider returning the same book under
// several edition titles yields one row, and the omnibus is dropped.
func TestAuthorWorksDedupesAndDropsOmnibus(t *testing.T) {
	works := []BookMeta{
		{Provider: "w", Title: "The Final Ledger", Authors: []string{"Cade Rennick"}},
		{Provider: "w", Title: "Hollow Truce", Authors: []string{"Cade Rennick"}},
		{Provider: "w", Title: "Broken Oath", Authors: []string{"Cade Rennick"}},
		{Provider: "w", Title: "In the Smoke", Authors: []string{"Cade Rennick"}},
		{Provider: "w", Title: "In the Smoke: A Thriller", Authors: []string{"Cade Rennick"}},
		{Provider: "w", Title: "In the Smoke: Mason Holt 5", Authors: []string{"Cade Rennick"}, SeriesName: "Mason Holt", SeriesIndex: 5},
		// the omnibus lists three sibling titles above — dropped by data, not regex
		{Provider: "w", Title: "The Final Ledger, Hollow Truce, and Broken Oath", Authors: []string{"Cade Rennick"}},
	}
	chain := NewChain(func() []string { return []string{"w"} }, &worksLister{works: works})
	got, err := chain.AuthorWorks(context.Background(), "Cade Rennick", 50)
	if err != nil {
		t.Fatal(err)
	}
	titles := map[string]BookMeta{}
	for _, m := range got {
		titles[m.Title] = m
	}
	// 4 real books: Final Ledger, Hollow Truce, Broken Oath, In the Smoke
	if len(got) != 4 {
		t.Fatalf("want 4 books, got %d: %v", len(got), titles)
	}
	// the three In-the-Blood editions merged, keeping the clean title + series
	itb, ok := titles["In the Smoke"]
	if !ok {
		t.Fatalf("expected the clean title 'In the Smoke', got %v", titles)
	}
	if itb.SeriesName != "Mason Holt" || itb.SeriesIndex != 5 {
		t.Errorf("series info lost in dedup merge: %+v", itb)
	}
	if _, leaked := titles["The Final Ledger, Hollow Truce, and Broken Oath"]; leaked {
		t.Error("omnibus leaked through")
	}
}

// A bibliography must never show the same book again in another language:
// records with a recorded non-English language are dropped whatever provider
// answered, and a title with no Latin characters at all is a translation
// record unless the provider explicitly says it's English.
func TestAuthorWorksDropsForeignLanguageRecords(t *testing.T) {
	works := []BookMeta{
		{Provider: "w", Title: "The Final Ledger", Authors: []string{"Cade Rennick"}},
		{Provider: "w", Title: "Ostatnia księga", Authors: []string{"Cade Rennick"}, Language: "Polish"},
		{Provider: "w", Title: "Последняя книга", Authors: []string{"Cade Rennick"}}, // no language recorded
		{Provider: "w", Title: "ファイナル・レジャー", Authors: []string{"Cade Rennick"}},      // no language recorded
		{Provider: "w", Title: "2149", Authors: []string{"Cade Rennick"}},            // digits-only titles are fine
	}
	chain := NewChain(func() []string { return []string{"w"} }, &worksLister{works: works})
	got, err := chain.AuthorWorks(context.Background(), "Cade Rennick", 50)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, m := range got {
		titles = append(titles, m.Title)
	}
	if len(got) != 2 || got[0].Title != "The Final Ledger" || got[1].Title != "2149" {
		t.Fatalf("want only the English records, got %v", titles)
	}
}

type worksLister struct{ works []BookMeta }

func (w *worksLister) Key() string { return "w" }
func (w *worksLister) Search(ctx context.Context, p SearchParams) ([]BookMeta, error) {
	return nil, nil
}
func (w *worksLister) AuthorWorks(ctx context.Context, name string, limit int) ([]BookMeta, error) {
	return w.works, nil
}

// Regression: distinct numbered volumes with no series metadata must NOT
// merge — an earlier digitsTail rule collapsed "Nightmarch, Volume 1/2/3" to one.
func TestDedupeKeyKeepsNumberedVolumesDistinct(t *testing.T) {
	vols := []string{"Nightmarch, Volume 1", "Nightmarch, Volume 2", "Nightmarch, Volume 3"}
	keys := map[string]bool{}
	for _, tt := range vols {
		k := dedupeKey(tt, "") // no series name
		if keys[k] {
			t.Fatalf("numbered volumes collapsed to the same key %q", k)
		}
		keys[k] = true
	}
	// series designation with a number still collapses when the series is known
	if dedupeKey("In the Smoke: Mason Holt 5", "Mason Holt") != dedupeKey("In the Smoke", "") {
		t.Error("series-designated edition should still collapse")
	}
}

// When the title IS the series name ("The Final Ledger" in series "Final
// Ledger"), stripping the series must not reduce the key to a bare article —
// the record must still converge with series-less variants of the same book.
func TestDedupeKeySeriesTitledBook(t *testing.T) {
	base := dedupeKey("The Final Ledger", "")
	for _, x := range []struct{ title, series string }{
		{"The Final Ledger", "Final Ledger"},
		{"The Final Ledger: A Thriller", "Final Ledger"},
	} {
		if got := dedupeKey(x.title, x.series); got != base {
			t.Errorf("dedupeKey(%q, %q) = %q, want %q", x.title, x.series, got, base)
		}
	}
	// distinct series entries must stay distinct even with the fallback
	if dedupeKey("Ashveil: The Last Bastion", "Ashveil") == dedupeKey("Ashveil: The Heir of Storms", "Ashveil") {
		t.Error("distinct series entries collapsed via article fallback")
	}
}

// Goodreads list titles carry the series designation as a trailing
// parenthetical; searching with it attached returns zero hits, so it must
// strip — while a parenthetical that's part of the real title stays.
func TestStripSeriesSuffix(t *testing.T) {
	strip := map[string]string{
		"The Wolves of Calder (The Silas Calder Series Book 1)":      "The Wolves of Calder",
		"First Ember (The Ember Cycle, #1)":                          "First Ember",
		"The Fox, the Crow and the Lantern (Tales of Briarwood, #1)": "The Fox, the Crow and the Lantern",
		"Torn (A Marcus Bell Thriller)":                              "Torn",
		"Loom (Vault, 1)":                                            "Loom",
		"The Keepers of the Gate (The Crown of Ashes, Part 1)":       "The Keepers of the Gate",
		"Sable (Sable Chronicles #1)":                                "Sable",
		"The Wanderer (Everwood Universe)":                           "The Wanderer (Everwood Universe)", // no designation marker — kept
		"Glass Harvest":                                              "Glass Harvest",
		"(Don't) Call Me Ghost":                                      "(Don't) Call Me Ghost", // leading, not trailing
		"Lantern (Rise of the Lantern, #1)":                          "Lantern",
	}
	for in, want := range strip {
		if got := StripSeriesSuffix(in); got != want {
			t.Errorf("StripSeriesSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}

// BestConfidentMatch is the override gate: only a high-scoring candidate with
// author overlap may replace user-visible fields; anything weaker keeps the
// existing metadata.
func TestBestConfidentMatch(t *testing.T) {
	noisy := "The Wolves of Calder (The Silas Calder Series Book 1)"
	right := BookMeta{Title: "The Wolves of Calder", Authors: []string{"D.K. Vale"}, RatingsCount: 50, HardcoverID: "1"}
	wrongAuthor := BookMeta{Title: "The Wolves of Calder", Authors: []string{"Somebody Else"}, RatingsCount: 5000, HardcoverID: "2"}
	unrelated := BookMeta{Title: "Wolf Hearts of the Steppe", Authors: []string{"D.K. Vale"}, HardcoverID: "3"}

	got, ok := BestConfidentMatch([]BookMeta{unrelated, wrongAuthor, right}, noisy, "D.K. Vale")
	if !ok || got.HardcoverID != "1" {
		t.Fatalf("want the author-verified exact title to win, got %+v ok=%v", got, ok)
	}
	if _, ok := BestConfidentMatch([]BookMeta{wrongAuthor, unrelated}, noisy, "D.K. Vale"); ok {
		t.Fatal("no candidate passes the author gate — must report no confident match")
	}
	if _, ok := BestConfidentMatch(nil, noisy, "D.K. Vale"); ok {
		t.Fatal("empty candidate list must not match")
	}
	// initials/spacing drift between providers must still count as overlap
	drifted := BookMeta{Title: "The Wolves of Calder", Authors: []string{"DK Vale", "J.M. Frost"}, HardcoverID: "4"}
	if _, ok := BestConfidentMatch([]BookMeta{drifted}, noisy, "D. K. Vale"); !ok {
		t.Fatal("author name drift (initial punctuation, co-credits) must still match")
	}
}

// A common title plus a shared FIRST name is not a match. Half of a two-token
// name is one token, which let "Chasing the Storm" by any other Nora clear
// the author gate and hand Nora Quill's book someone else's cover.
func TestBestConfidentMatchRejectsSharedFirstName(t *testing.T) {
	title := "Chasing the Storm"
	impostor := BookMeta{Title: title, Authors: []string{"Nora Hastings"}, RatingsCount: 90000, HardcoverID: "impostor"}
	real := BookMeta{Title: title, Authors: []string{"Nora Quill"}, RatingsCount: 400, HardcoverID: "real"}

	if got, ok := BestConfidentMatch([]BookMeta{impostor}, title, "Nora Quill"); ok {
		t.Fatalf("a different Nora must not match, got %+v", got)
	}
	// ...and the popular impostor must not outrank the real one either
	got, ok := BestConfidentMatch([]BookMeta{impostor, real}, title, "Nora Quill")
	if !ok || got.HardcoverID != "real" {
		t.Fatalf("want the same-surname candidate, got %+v ok=%v", got, ok)
	}
	// a shared SURNAME with a drifting given name still matches
	married := BookMeta{Title: title, Authors: []string{"Nora Anne Quill"}, HardcoverID: "married"}
	if _, ok := BestConfidentMatch([]BookMeta{married}, title, "Nora Quill"); !ok {
		t.Fatal("a middle name must not break the match")
	}
}

// The Sadie Lark shape: one book arriving under a series-prefix title, a
// marketing-descriptor title, and a bare leading-article variant must
// collapse to one key — while the rest of the series stays distinct.
func TestDedupeKeyCollapsesSeriesPrefixAndArticleVariants(t *testing.T) {
	variants := []struct{ title, series string }{
		{"Sadie Lark and the Ghost in the Garden", "Sadie Lark"},
		{"Ghost in the Garden A Sadie Lark Novel", "Sadie Lark"},
		{"The Ghost in the Garden", "Sadie Lark"},
		{"The Ghost in the Garden", ""}, // variant that arrived without series info
	}
	want := dedupeKey(variants[0].title, variants[0].series)
	for _, v := range variants[1:] {
		if got := dedupeKey(v.title, v.series); got != want {
			t.Errorf("%q (series %q) -> %q, want %q", v.title, v.series, got, want)
		}
	}
	if dedupeKey("Sadie Lark and the Skeleton in the Cellar", "Sadie Lark") == want {
		t.Error("distinct series entries collapsed")
	}
}

// Promo samplers are derivative junk: they must never join a bibliography,
// while real titles that merely mention firsts or chapters stay.
func TestDerivativeCatchesSamplers(t *testing.T) {
	junk := []string{
		"A Faceless Girl: The First Three Chapters",
		"Vesper: Sneak Peek",
		"It Ends at Dawn: Chapters 1-5",
		"The Groundskeeper Free Preview",
		"First Ember Sampler",
	}
	for _, tt := range junk {
		if !isDerivative(tt) {
			t.Errorf("sampler slipped through: %q", tt)
		}
	}
	real := []string{
		"A Faceless Girl",
		"The First Fifteen Lives of Arthur Penn",
		"First House on the Left",
		"The Last Chapter",
	}
	for _, tt := range real {
		if isDerivative(tt) {
			t.Errorf("real book flagged as derivative: %q", tt)
		}
	}
}

// End to end: variants merge onto one row and the canonical (most-rated)
// variant's title is the one that survives.
func TestAuthorWorksMergesTitleVariantsByRatings(t *testing.T) {
	works := []BookMeta{
		{Provider: "w", Title: "The Ghost in the Garden", Authors: []string{"Nora Quill"}, RatingsCount: 40},
		{Provider: "w", Title: "Sadie Lark and the Ghost in the Garden", Authors: []string{"Nora Quill"}, SeriesName: "Sadie Lark", SeriesIndex: 1, RatingsCount: 21000},
		{Provider: "w", Title: "Ghost in the Garden A Sadie Lark Novel", Authors: []string{"Nora Quill"}, SeriesName: "Sadie Lark", SeriesIndex: 1, RatingsCount: 12},
		{Provider: "w", Title: "Sadie Lark and the Skeleton in the Cellar", Authors: []string{"Nora Quill"}, SeriesName: "Sadie Lark", SeriesIndex: 2, RatingsCount: 15000},
	}
	chain := NewChain(func() []string { return []string{"w"} }, &worksLister{works: works})
	got, err := chain.AuthorWorks(context.Background(), "Nora Quill", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		titles := make([]string, len(got))
		for i, m := range got {
			titles[i] = m.Title
		}
		t.Fatalf("want 2 books, got %d: %v", len(got), titles)
	}
	var one *BookMeta
	for i := range got {
		if got[i].SeriesIndex == 1 {
			one = &got[i]
		}
	}
	if one == nil || one.Title != "Sadie Lark and the Ghost in the Garden" {
		t.Fatalf("canonical title lost in merge: %+v", got)
	}
	if one.SeriesName != "Sadie Lark" || one.RatingsCount != 21000 {
		t.Errorf("merge dropped series/ratings: %+v", one)
	}
}

// Sort-order titles with edition parentheticals ("Lighthouse at Wrenfell,
// the (Screen Tie In)") must key identically to the natural title.
func TestDedupeKeyInvertedArticleAndTieIn(t *testing.T) {
	pairs := [][2]string{
		{"Lighthouse at Wrenfell, the (Screen Tie In)", "The Lighthouse at Wrenfell"},
		{"Songbird, The", "The Songbird"},
		{"It Ends at Dawn (Movie Tie-in)", "It Ends at Dawn"},
		{"Marisol (Annotated Edition)", "Marisol"},
	}
	for _, p := range pairs {
		if dedupeKey(p[0], "") != dedupeKey(p[1], "") {
			t.Errorf("%q and %q keyed apart: %q vs %q", p[0], p[1], dedupeKey(p[0], ""), dedupeKey(p[1], ""))
		}
	}
	// a parenthetical that is part of the real title must NOT be stripped
	if dedupeKey("(Don't) Call Me Ghost", "") == dedupeKey("Call Me Ghost", "") {
		t.Error("leading title parenthetical wrongly stripped")
	}
}
