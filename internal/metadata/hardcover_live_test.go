package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// Live integration tests against the real Hardcover API. Skipped unless a
// token is supplied via the environment:
//
//	HARDCOVER_TOKEN=... go test ./internal/metadata/ -run TestHardcoverLive -v
//
// The token is read only from the environment at runtime — never store it in
// code, fixtures, or configuration inside the repository.
//
// HARDCOVER_TEST_AUTHORS may hold a comma-separated author list to probe
// (default: a set known to have foreign editions and edition variants).

func liveHardcover(t *testing.T) *Hardcover {
	t.Helper()
	token := os.Getenv("HARDCOVER_TOKEN")
	if token == "" {
		t.Skip("HARDCOVER_TOKEN not set — skipping live Hardcover API test")
	}
	return NewHardcover(func() string { return token })
}

func liveAuthors() []string {
	if v := os.Getenv("HARDCOVER_TEST_AUTHORS"); v != "" {
		var out []string
		for _, a := range strings.Split(v, ",") {
			if a = strings.TrimSpace(a); a != "" {
				out = append(out, a)
			}
		}
		return out
	}
	// Defaults chosen for known failure modes: heavily-translated thriller
	// authors (foreign editions), long series (edition variants + omnibuses).
	return []string{"Jack Carr", "Brandon Sanderson", "Agatha Christie"}
}

func TestHardcoverLiveToken(t *testing.T) {
	h := liveHardcover(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.Test(ctx); err != nil {
		t.Fatalf("token check failed: %v", err)
	}
	t.Log("token accepted by live API")
}

// TestHardcoverLiveAuthorWorksLanguage verifies the server-side English
// pre-filter and the client-side belt on real data: no bibliography entry may
// carry a recorded non-English language.
func TestHardcoverLiveAuthorWorksLanguage(t *testing.T) {
	h := liveHardcover(t)
	for _, author := range liveAuthors() {
		t.Run(author, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			metas, err := h.AuthorWorks(ctx, author, 100)
			if err != nil {
				t.Fatalf("AuthorWorks(%q): %v", author, err)
			}
			if len(metas) == 0 {
				t.Errorf("AuthorWorks(%q) returned nothing — name mismatch or filter too aggressive?", author)
			}
			langs := map[string]int{}
			for _, m := range metas {
				langs[m.Language]++
				if !englishOrUnknown(m.Language) {
					t.Errorf("non-English record leaked: %q (language %q)", m.Title, m.Language)
				}
			}
			t.Logf("%s: %d raw provider results; language distribution: %v", author, len(metas), langs)
		})
	}
}

// TestHardcoverLiveChainDedupe runs the full chain cleanup (exclusions,
// language belt, edition-variant dedupe, sibling-omnibus drop) over live
// bibliographies and reports what merged and what was dropped, so the
// behavior can be eyeballed with -v.
func TestHardcoverLiveChainDedupe(t *testing.T) {
	h := liveHardcover(t)
	chain := NewChain(func() []string { return []string{"hardcover"} }, h)

	for _, author := range liveAuthors() {
		t.Run(author, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()

			raw, err := h.AuthorWorks(ctx, author, 100)
			if err != nil {
				t.Fatalf("raw AuthorWorks(%q): %v", author, err)
			}
			cleaned, err := chain.AuthorWorks(ctx, author, 100)
			if err != nil {
				t.Fatalf("chain AuthorWorks(%q): %v", author, err)
			}

			// No two survivors may share an edition-variant identity.
			byKey := map[string][]string{}
			for _, m := range cleaned {
				k := DedupeKey(m.Title, m.SeriesName)
				byKey[k] = append(byKey[k], m.Title)
			}
			for k, titles := range byKey {
				if len(titles) > 1 {
					t.Errorf("dedupe missed: key %q kept %d rows: %v", k, len(titles), titles)
				}
			}

			// No compilation may survive the cleanup.
			for _, m := range cleaned {
				if IsCompilation(m.Title) {
					t.Errorf("compilation leaked through: %q", m.Title)
				}
				if !englishOrUnknown(m.Language) {
					t.Errorf("non-English leaked through chain: %q (%q)", m.Title, m.Language)
				}
			}

			// Report: which raw titles merged into which surviving row, and
			// which disappeared entirely (excluded/derivative/omnibus).
			survivors := map[string]string{} // dedupe key -> surviving title
			for _, m := range cleaned {
				survivors[DedupeKey(m.Title, m.SeriesName)] = m.Title
			}
			var merged, dropped []string
			for _, m := range raw {
				k := DedupeKey(m.Title, m.SeriesName)
				kept, ok := survivors[k]
				switch {
				case !ok:
					dropped = append(dropped, fmt.Sprintf("%q (lang %q, series %q)", m.Title, m.Language, m.SeriesName))
				case kept != m.Title:
					merged = append(merged, fmt.Sprintf("%q -> %q", m.Title, kept))
				}
			}
			t.Logf("%s: %d raw -> %d cleaned", author, len(raw), len(cleaned))
			for _, s := range merged {
				t.Logf("  merged  %s", s)
			}
			for _, s := range dropped {
				t.Logf("  dropped %s", s)
			}
			for _, m := range cleaned {
				series := ""
				if m.SeriesName != "" {
					series = fmt.Sprintf("  [%s #%g]", m.SeriesName, m.SeriesIndex)
				}
				t.Logf("  kept    %q%s lang=%q", m.Title, series, m.Language)
			}
		})
	}
}

// TestHardcoverLiveNoisyListTitle reproduces the confirmed watched-list
// failure: a Goodreads feed title with the series parenthetical attached
// ("The Lions of Mercer (The Micah Mercer Series Book 1)") plus author must
// resolve to the clean Hardcover record. The old title+author-concatenated
// query returned zero hits for exactly this input.
func TestHardcoverLiveNoisyListTitle(t *testing.T) {
	h := liveHardcover(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	metas, err := h.Search(ctx, SearchParams{
		Title:  "The Lions of Mercer (The Micah Mercer Series Book 1)",
		Author: "John Lovell",
		Limit:  5,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(metas) == 0 {
		t.Fatal("noisy Goodreads list title still returns nothing — the series-suffix strip regressed")
	}
	for _, m := range metas {
		t.Logf("result %q authors=%v hardcover_id=%s cover=%q", m.Title, m.Authors, m.HardcoverID, m.CoverURL)
	}
	best, ok := BestConfidentMatch(metas, "The Lions of Mercer (The Micah Mercer Series Book 1)", "John Lovell")
	if !ok {
		t.Fatal("no result clears the canonical-adoption gate (score ≥ 60 + author overlap)")
	}
	if normalizeText(best.Title) != normalizeText("The Lions of Mercer") {
		t.Errorf("confident match resolved to %q, want the clean title", best.Title)
	}
	if best.CoverURL == "" {
		t.Error("confident match carries no cover — canonical adoption would keep the feed cover")
	}
}

// TestHardcoverLiveAuthorInfo verifies the AuthorInfo query shape against the
// live schema. AuthorInfo itself silently falls back to an unordered query if
// the ordered one is rejected, so this test first runs the ordered query
// directly — a users_count/ordering schema drift must fail loudly here, not
// hide behind the fallback. Then it checks that AuthorInfo resolves real
// presentation data (bio + portrait) for well-known authors.
func TestHardcoverLiveAuthorInfo(t *testing.T) {
	h := liveHardcover(t)

	// The exact ordered query AuthorInfo tries first, run without a fallback.
	ordered := `query ($name: String!) {
	  authors(where: {name: {_eq: $name}}, order_by: {users_count: desc}, limit: 5) {
	    id name bio cached_image
	  }
	}`
	var raw struct {
		Authors []struct {
			ID          json.Number     `json:"id"`
			Name        string          `json:"name"`
			Bio         string          `json:"bio"`
			CachedImage json.RawMessage `json:"cached_image"`
		} `json:"authors"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := h.do(ctx, ordered, map[string]any{"name": "Brandon Sanderson"}, &raw); err != nil {
		t.Fatalf("ordered authors query rejected by live schema (users_count/bio/cached_image drift?): %v", err)
	}
	if len(raw.Authors) == 0 {
		t.Fatal("ordered authors query returned no rows for Brandon Sanderson")
	}
	for _, a := range raw.Authors {
		t.Logf("raw row id=%s name=%q bio=%dB image=%q", a.ID, a.Name, len(a.Bio), hcImageURL(a.CachedImage))
	}

	for _, name := range []string{"Brandon Sanderson", "Agatha Christie"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			info, err := h.AuthorInfo(ctx, name)
			if err != nil {
				t.Fatalf("AuthorInfo(%q): %v", name, err)
			}
			if info == nil {
				t.Fatalf("AuthorInfo(%q) found nothing — exact-name match broke?", name)
			}
			t.Logf("id=%s name=%q image=%q bio=%.120q", info.ID, info.Name, info.ImageURL, info.Bio)
			if info.Name != name {
				t.Errorf("resolved name %q, want %q", info.Name, name)
			}
			if info.Bio == "" {
				t.Errorf("no bio for %s — a famous author should carry one", name)
			}
			if info.ImageURL == "" {
				t.Errorf("no portrait for %s — cached_image parse or field drift?", name)
			}
		})
	}

	// Unknown author: nil result, no error.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel2()
	info, err := h.AuthorInfo(ctx2, "Zzyzx Nonexistent Author 1c9d2")
	if err != nil {
		t.Fatalf("AuthorInfo(unknown): %v", err)
	}
	if info != nil {
		t.Errorf("AuthorInfo(unknown) = %+v, want nil", info)
	}
}

// TestHardcoverLiveSearchLanguage spot-checks title search: results for a
// well-known English title should resolve with English or unknown language
// on their representative editions.
func TestHardcoverLiveSearchLanguage(t *testing.T) {
	h := liveHardcover(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	metas, err := h.Search(ctx, SearchParams{Title: "The Terminal List", Author: "Jack Carr", Limit: 5})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(metas) == 0 {
		t.Fatal("search returned nothing for a well-known title")
	}
	for _, m := range metas {
		t.Logf("result %q lang=%q series=%q hardcover_id=%s genres=%v", m.Title, m.Language, m.SeriesName, m.HardcoverID, m.Genres)
	}
	if len(metas[0].Genres) == 0 {
		t.Error("top result for a popular thriller carried no genres — cached_tags drifted?")
	}
}

// The exact-record fetch behind "refresh from a known Hardcover ID". The id
// is resolved live: search a stable title first, then fetch its id and check
// the record round-trips.
func TestHardcoverLiveFetchByHardcoverID(t *testing.T) {
	h := liveHardcover(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	hits, err := h.Search(ctx, SearchParams{Title: "Project Hail Mary", Author: "Andy Weir", Limit: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].HardcoverID == "" {
		t.Fatalf("no live search hit to fetch by id: %+v", hits)
	}

	m, err := h.FetchByHardcoverID(ctx, hits[0].HardcoverID)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil || m.HardcoverID != hits[0].HardcoverID {
		t.Fatalf("by-id fetch = %+v, want id %s", m, hits[0].HardcoverID)
	}
	if m.Title == "" || len(m.Authors) == 0 {
		t.Errorf("by-id record is thin: %+v", m)
	}

	// a nonsense id must be a soft miss, not an error
	miss, err := h.FetchByHardcoverID(ctx, "999999999")
	if err != nil {
		t.Fatalf("dangling id should be soft: %v", err)
	}
	if miss != nil {
		t.Errorf("dangling id returned a record: %+v", miss)
	}
}

// The reported wrong-book regression, against live data: "The River" is a
// crowded title whose most popular record credits a different author
// entirely. Whatever the search pool returns, the enrich pick must never be
// a record credited to someone other than the wanted author — an empty pick
// (nothing adopted) is the correct outcome when the author's own record
// isn't on Hardcover.
func TestHardcoverLiveEnrichWrongAuthorGuard(t *testing.T) {
	h := liveHardcover(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const wantAuthor = "Noelle W. Ihli"
	hits, err := h.Search(ctx, SearchParams{Title: "The River", Author: wantAuthor, Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("live pool for %q: %d hits", "The River", len(hits))
	for _, m := range hits {
		t.Logf("  id=%s authors=%v ratings=%d series=%q", m.HardcoverID, m.Authors, m.RatingsCount, m.SeriesName)
	}

	best, ok := bestEnrichMatch(hits, "The River", wantAuthor)
	if !ok {
		t.Log("no candidate cleared the bar — correct when the author's own record is absent")
		return
	}
	if len(best.Authors) > 0 && !authorsOverlap(best, wantAuthor) {
		t.Errorf("picked a wrong-author record: %+v", best)
	} else {
		t.Logf("picked %q by %v (id %s)", best.Title, best.Authors, best.HardcoverID)
	}
}
