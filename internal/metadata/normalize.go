package metadata

import (
	"math"
	"regexp"
	"strings"
	"sync"
)

var (
	nonAlnum = regexp.MustCompile(`[^a-z0-9 ]`)
	spaces   = regexp.MustCompile(`\s+`)
	// Includes promo samplers ("A Quiet Deception: The First Three Chapters",
	// "Sneak Peek", "Chapters 1-5") — providers list them as separate books
	// and they'd otherwise sit next to the real title forever.
	derivative = regexp.MustCompile(`(?i)\b(summary|study guide|analysis|reading guide|literature guide|workbook|digest|breakdown|companion|trivia|sampler|sneak peek|teaser|excerpt|free preview)\b|\bfirst\s+(one|two|three|four|five|six|seven|eight|nine|ten|\d+)\s+chapters?\b|\bchapters?\s+\d+\s*[-–]\s*\d+\b`)
	// Structural compilation shapes — "Books 1–3", "#1–3" — are never a real
	// title, so they stay built in. The keyword half (omnibus, box set, …)
	// lives in DefaultExcludeTerms so users can drop individual terms.
	compilation = regexp.MustCompile(`(?i)\bbooks?\s*\d+\s*[-–&]\s*\d+|#\d+\s*[-–]\s*\d+`)
	// Generic edition/marketing descriptors removed when computing a dedup
	// key, so "Into the Storm" and "Into the Storm: A Thriller" collapse. These
	// are safe to strip anywhere — they never carry the distinguishing part
	// of a real title.
	editionDescriptor = regexp.MustCompile(`(?i)\b(a|an|the)\s+(thriller|novel|mystery|story|series)\b|\bbook\s+\d+\b`)
	// seriesParenthetical decides whether the content of a trailing "(...)"
	// group reads as a series designation rather than part of the title:
	// a series/marketing keyword ("The Alton Reed Series Book 1", "A Marcus
	// Cole Thriller"), a numbered marker anywhere ("Book 1", "#2", "Vol. 3"),
	// or a bare trailing number ("The Ember Cycle, #1", "Vault, 2").
	seriesParenthetical = regexp.MustCompile(`(?i)\b(series|trilogy|duology|saga|chronicles|cycle|thriller|novel|novella|mystery)s?\b|(\b(book|vol|volume|no|part)\b\.?\s*|#\s*)\d+(\.\d+)?|(^|,)\s*#?\d+(\.\d+)?\s*$`)
	// editionParenthetical marks a trailing "(...)" group as edition noise for
	// the dedup key: media tie-ins ("(TV Tie In)", "(Movie Tie-in)"),
	// anniversary/annotated/illustrated editions, and the like.
	editionParenthetical = regexp.MustCompile(`(?i)\btie.?in\b|\b(edition|anniversary|annotated|illustrated|unabridged|abridged|movie|film|netflix|motion\s*picture|tv)\b|\blarge\s*print\b`)
	// invertedArticle catches sort-order titles ("Hounds of Alton, The")
	// so they key identically to their natural form.
	invertedArticle = regexp.MustCompile(`(?i),\s*(the|a|an)\s*$`)
)

func normalizeText(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "‘", "'")
	s = strings.ReplaceAll(s, "’", "'")
	s = strings.ReplaceAll(s, "&", " and ")
	s = nonAlnum.ReplaceAllString(s, " ")
	return strings.TrimSpace(spaces.ReplaceAllString(s, " "))
}

func tokens(s string) []string {
	var out []string
	for _, t := range strings.Split(s, " ") {
		if len(t) > 1 {
			out = append(out, t)
		}
	}
	return out
}

// NormalizeName canonicalizes an author name for comparison — case,
// punctuation and spacing differences collapse.
func NormalizeName(name string) string {
	return normalizeText(name)
}

// StripSeriesSuffix removes the trailing parenthetical series designation
// Goodreads appends to list titles — "The Hounds of Alton (The Alton Reed
// Series Book 1)" → "The Hounds of Alton", "First Ember (The Ember Cycle, #1)"
// → "First Ember". Only parentheticals that read as series designations are
// removed (see seriesParenthetical); one that is part of the real title
// stays. Search endpoints choke on the noise — Hardcover's Typesense returns
// zero hits for the full string while the bare title matches fine.
func StripSeriesSuffix(title string) string {
	t := strings.TrimSpace(title)
	// several groups can stack ("Title (Series, #1) (Book 1)"); peel from the end
	for {
		if !strings.HasSuffix(t, ")") {
			return t
		}
		open := strings.LastIndex(t, "(")
		if open <= 0 || !seriesParenthetical.MatchString(t[open+1:len(t)-1]) {
			return t
		}
		stripped := strings.TrimSpace(t[:open])
		if stripped == "" {
			return t
		}
		t = stripped
	}
}

// DefaultExcludeTerms seeds the user-editable exclusion list (Settings →
// Metadata). Every term is removable there; the structural "Books 1–3"
// patterns in the compilation regex are not, because they can never be a
// real title. Deliberately keyword-based: a title that merely lists works
// with commas ("The Key, the Door, and the Garden") can be a real book, so
// no comma-list heuristic is used — that would hide genuine titles.
var DefaultExcludeTerms = []string{
	"box set", "boxed set", "omnibus", "collection", "bundle", "anthology",
	"complete series", "trilogy",
}

// DefaultExcludeValue is DefaultExcludeTerms in the exclude_patterns
// setting's on-disk shape (one term per line).
var DefaultExcludeValue = strings.Join(DefaultExcludeTerms, "\n")

// IsCompilation reports whether a title has the structural shape of a box
// set ("Books 1–3", "#1–3"). Keyword matching (omnibus, anthology, …) runs
// through Excluded, where the term list is user-editable.
func IsCompilation(title string) bool {
	return compilation.MatchString(title)
}

// termRegexps caches the compiled word-bounded matcher per exclusion term,
// so bibliography-sized loops don't recompile per title.
var (
	termMu      sync.Mutex
	termRegexps = map[string]*regexp.Regexp{}
)

func termRe(term string) *regexp.Regexp {
	termMu.Lock()
	defer termMu.Unlock()
	if re, ok := termRegexps[term]; ok {
		return re
	}
	words := strings.Fields(term)
	for i, w := range words {
		words[i] = regexp.QuoteMeta(w)
	}
	// \s* between words keeps the old "boxset"/"box set" tolerance; word
	// boundaries stop "collection" from biting "Recollection"
	re, err := regexp.Compile(`(?i)\b` + strings.Join(words, `\s*`) + `\b`)
	if err != nil {
		re = nil
	}
	termRegexps[term] = re
	return re
}

// dedupeKey collapses a book's edition variants to one key WITHOUT merging
// distinct books. It removes the book's own series designation (using the
// real SeriesName, so "Into the Storm: Winter Watch 5" -> "into the storm" while
// "Ashveil: The Last Bastion" keeps "the last bastion") and generic edition
// descriptors ("A Thriller"), never a title's distinguishing part. Non-Latin
// titles that normalize to empty fall back to the raw title so they're never
// dropped.
// stripTitleNoise peels trailing parentheticals that read as series or
// edition designations ("(The Ember Cycle, #1)", "(Movie Tie In)") and drops a
// sort-order trailing article ("Hounds of Alton, The"). Only for key
// computation — a parenthetical that is part of the real title stays because
// neither pattern matches it.
func stripTitleNoise(title string) string {
	t := strings.TrimSpace(title)
	for strings.HasSuffix(t, ")") {
		open := strings.LastIndex(t, "(")
		if open <= 0 {
			break
		}
		inner := t[open+1 : len(t)-1]
		if !seriesParenthetical.MatchString(inner) && !editionParenthetical.MatchString(inner) {
			break
		}
		next := strings.TrimSpace(t[:open])
		if next == "" {
			break
		}
		t = next
	}
	return strings.TrimSpace(invertedArticle.ReplaceAllString(t, ""))
}

func dedupeKey(title, seriesName string) string {
	s := strings.ToLower(stripTitleNoise(title))
	if sn := strings.ToLower(strings.TrimSpace(seriesName)); sn != "" {
		// drop the series name (word-bounded, so series "Ember" doesn't bite
		// "November") and any adjacent "book"/number designation
		desig := regexp.MustCompile(`(?i)[:\-,]?\s*\b` + regexp.QuoteMeta(sn) + `\b(\s*[,:]?\s*(book)?\s*#?\d+)?`)
		s = desig.ReplaceAllString(s, " ")
	}
	s = editionDescriptor.ReplaceAllString(s, " ")
	key := trimKeyEdges(normalizeText(s))
	if key != "" && !articlesOnly(key) {
		return key
	}
	// Series stripping consumed the whole title — the title IS the series
	// designation ("The Winter Watch" in series "Winter Watch" would key
	// on just "the", diverging from series-less variants of the same book).
	// Re-key on the full title with only edition descriptors removed.
	key = trimKeyEdges(normalizeText(editionDescriptor.ReplaceAllString(strings.ToLower(stripTitleNoise(title)), " ")))
	if key != "" {
		return key
	}
	// normalization stripped everything (e.g. a fully non-Latin title):
	// keep a stable, non-empty key so the book is never silently dropped
	return strings.Join(strings.Fields(strings.ToLower(title)), " ")
}

// trimKeyEdges cleans the residue that series/descriptor stripping leaves at
// a key's edges. Removing a leading series name turns "Nora Vale and the
// Body in the Garden" into "and the body in the garden" — the dangling
// conjunction and article would key it apart from "The Body in the Garden"
// and "Body in the Garden: A Nora Vale Novel", which are the same book.
// Leading articles are dropped unconditionally (library title-sort style):
// same-author books distinguished ONLY by a leading article are vanishingly
// rare, while article-variant duplicate rows are what bibliographies actually
// produce. A key that would vanish entirely is left as-is.
func trimKeyEdges(key string) string {
	t := strings.Fields(key)
	for len(t) > 1 && (t[0] == "and" || t[0] == "or") {
		t = t[1:]
	}
	if len(t) > 1 && (t[0] == "a" || t[0] == "an" || t[0] == "the") {
		t = t[1:]
	}
	if out := strings.Join(t, " "); out != "" {
		return out
	}
	return key
}

// articlesOnly reports whether a normalized key retains no distinguishing
// words — only articles survived the series/descriptor stripping.
func articlesOnly(key string) bool {
	for _, t := range strings.Fields(key) {
		switch t {
		case "a", "an", "the":
		default:
			return false
		}
	}
	return true
}

// DedupeKey exposes the edition-variant identity key so the catalog can
// converge an incoming book on an existing row whose stored title is a
// different edition variant, instead of minting a sibling.
func DedupeKey(title, seriesName string) string {
	return dedupeKey(title, seriesName)
}

// Excluded reports whether a title is filtered out — by the structural
// compilation shapes or by the exclusion terms (seeded with
// DefaultExcludeTerms, user-editable in Settings → Metadata). Terms match
// word-bounded and case-insensitive, so "collection" never bites
// "Recollection".
func Excluded(title string, patterns []string) bool {
	if IsCompilation(title) {
		return true
	}
	for _, p := range patterns {
		if p = strings.TrimSpace(strings.ToLower(p)); p == "" {
			continue
		}
		if re := termRe(p); re != nil && re.MatchString(title) {
			return true
		}
	}
	return false
}

func isDerivative(title string) bool {
	return derivative.MatchString(title)
}

// ScoreCandidate exposes the ranking for match verification elsewhere
// (e.g. the importer deciding auto-match vs review).
func ScoreCandidate(m BookMeta, wantTitle, wantAuthor string) float64 {
	return scoreCandidate(m, wantTitle, wantAuthor)
}

// confidentMatchScore is the bar a candidate must clear before its fields may
// OVERRIDE what the user already sees (canonical adoption, re-match) rather
// than just fill gaps. Distinct from the ≥40 relevance filter inside search.
const confidentMatchScore = 60

// BestConfidentMatch picks the candidate that scores highest against the
// wanted title/author and clears the confident-match bar: score ≥ 60 plus
// real author overlap whenever an author is known. Titles are scored both raw
// and with their series suffix stripped, so a noisy Goodreads title still
// recognizes its clean Hardcover counterpart. Returns false when nothing
// qualifies — callers keep their existing metadata instead of guessing.
func BestConfidentMatch(candidates []BookMeta, wantTitle, wantAuthor string) (BookMeta, bool) {
	cleaned := StripSeriesSuffix(wantTitle)
	var best BookMeta
	bestScore := 0.0
	found := false
	for _, c := range candidates {
		score := scoreCandidate(c, wantTitle, wantAuthor)
		if cleaned != wantTitle {
			if s := scoreCandidate(c, cleaned, wantAuthor); s > score {
				score = s
			}
		}
		if score < confidentMatchScore {
			continue
		}
		if wantAuthor != "" && !authorsOverlap(c, wantAuthor) {
			continue
		}
		if !found || score > bestScore {
			best, bestScore, found = c, score, true
		}
	}
	return best, found
}

// bestEnrichMatch picks the candidate an Enrich gap-fill may trust: highest
// scoreCandidate ≥ 60 (titles scored raw and series-suffix-stripped, like
// BestConfidentMatch), with one extra gate — a candidate whose credited
// authors CONTRADICT the wanted author is rejected outright. An exact title
// match alone scores 100, so without the gate a same-titled book by a
// completely different author ("The River" by the wrong author entirely)
// passes verification and its identity, series, and cover merge into the
// record. Unlike BestConfidentMatch's bar for canonical adoption, a candidate
// crediting no authors at all is still allowed: it cannot contradict anyone,
// and enrich merges only ever fill blanks.
func bestEnrichMatch(candidates []BookMeta, wantTitle, wantAuthor string) (BookMeta, bool) {
	cleaned := StripSeriesSuffix(wantTitle)
	var best BookMeta
	bestScore := 0.0
	found := false
	for _, c := range candidates {
		score := scoreCandidate(c, wantTitle, wantAuthor)
		if cleaned != wantTitle {
			if s := scoreCandidate(c, cleaned, wantAuthor); s > score {
				score = s
			}
		}
		if score < confidentMatchScore {
			continue
		}
		if wantAuthor != "" && len(c.Authors) > 0 && !authorsOverlap(c, wantAuthor) {
			continue
		}
		if !found || score > bestScore {
			best, bestScore, found = c, score, true
		}
	}
	return best, found
}

// authorsOverlap reports whether a candidate's credited authors credibly
// include the wanted author — exact/containment match on normalized names, or
// at least half the wanted name's tokens present (initials and middle names
// drift between providers) AND the surname among them.
//
// The surname requirement is what keeps a shared first name from passing: on
// a two-token name, half the tokens is one token, so "Mara Voss" credited a
// stray "Breaking the Rules" by any other Mara as a confident match and
// adopted its cover and metadata. Given names drift; the last name is the
// part that has to line up.
func authorsOverlap(m BookMeta, wantAuthor string) bool {
	qa := normalizeText(wantAuthor)
	if qa == "" || len(m.Authors) == 0 {
		return false
	}
	all := normalizeText(strings.Join(m.Authors, " "))
	if all == qa || strings.Contains(all, qa) || strings.Contains(qa, all) {
		return true
	}
	if overlap(qa, all) < 0.5 {
		return false
	}
	want := tokens(qa)
	if len(want) == 0 {
		return false // an all-initials name has no surname to vouch for it
	}
	surname := want[len(want)-1]
	for _, t := range tokens(all) {
		if t == surname {
			return true
		}
	}
	return false
}

// scoreCandidate ranks a provider result against the query. Exact and prefix
// title matches dominate; author overlap helps; popularity nudges; derivative
// junk (study guides etc.) and compilations are pushed to the bottom.
func scoreCandidate(m BookMeta, wantTitle, wantAuthor string) float64 {
	score := 0.0
	title := normalizeText(m.Title)
	qt := normalizeText(wantTitle)

	if qt != "" {
		switch {
		case title == qt:
			score += 100
		case strings.HasPrefix(title, qt):
			score += 70
		case strings.HasPrefix(qt, title):
			score += 55
		case strings.Contains(title, qt):
			score += 45
		default:
			score += overlap(qt, title) * 30
		}
	}

	qa := normalizeText(wantAuthor)
	if qa != "" && len(m.Authors) > 0 {
		author := normalizeText(strings.Join(m.Authors, " "))
		if author == qa || strings.Contains(author, qa) || strings.Contains(qa, author) {
			score += 30
		} else {
			score += overlap(qa, author) * 15
		}
	}

	if m.RatingsCount > 0 {
		score += math.Min(math.Log10(float64(m.RatingsCount)+1)*3, 20)
	}
	if isDerivative(m.Title) {
		score -= 80
	}
	// scoring keeps the default term list regardless of user edits — an
	// omnibus should never outrank the real book in a match, even when the
	// user allows omnibuses to import
	if m.Compilation || Excluded(m.Title, DefaultExcludeTerms) {
		score -= 60
	}
	return score
}

func overlap(query, target string) float64 {
	qt := tokens(query)
	if len(qt) == 0 {
		return 0
	}
	set := map[string]bool{}
	for _, t := range tokens(target) {
		set[t] = true
	}
	hit := 0
	for _, t := range qt {
		if set[t] {
			hit++
		}
	}
	return float64(hit) / float64(len(qt))
}

// englishOrUnknown reports whether a provider's language value is English or
// absent. Bibliography filters treat "unknown" as keep — missing language
// data must never drop a legitimate work — while any recorded non-English
// language excludes the record.
func englishOrUnknown(lang string) bool {
	return strings.TrimSpace(lang) == "" || explicitlyEnglish(lang)
}

// explicitlyEnglish reports whether a provider positively recorded English.
// Accepts human names and ISO 639 codes since providers disagree on which
// they return.
func explicitlyEnglish(lang string) bool {
	switch strings.ToLower(strings.TrimSpace(lang)) {
	case "english", "eng", "en", "en-us", "en-gb":
		return true
	}
	return false
}
