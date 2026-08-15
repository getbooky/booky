// Package release ranks candidate downloads for a book. Ranking is
// format-first: the quality profile's format order dominates, then
// preferred/avoided terms, then the user's source priority order.
package release

import (
	"mime"
	"net/http"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

// Release is one candidate download, from an indexer or a direct source.
type Release struct {
	Title       string `json:"title"`
	Source      string `json:"source"`   // "prowlarr:<indexer>", "annas", "libgen"
	Protocol    string `json:"protocol"` // "usenet", "torrent", "direct"
	Format      string `json:"format"`   // epub, azw3, mobi, pdf, cbz... ("" = unknown)
	SizeBytes   int64  `json:"sizeBytes"`
	DownloadURL string `json:"downloadUrl"`
	InfoURL     string `json:"infoUrl,omitempty"`
	Indexer     string `json:"indexer,omitempty"`
	Language    string `json:"language,omitempty"`
	// Score is filled by Rank for display; higher is better.
	Score int `json:"score"`
}

// Profile is the slice of a quality profile that ranking needs.
type Profile struct {
	Formats        []string // best first, e.g. ["epub","azw3","mobi"]
	PreferredTerms []string // e.g. ["retail"]
	AvoidedTerms   []string // e.g. ["scan","ocr"]
	// Languages the user accepts (canonical lowercase names, e.g. "english").
	// A release whose language is detected and not in this list is rejected
	// harder than a wrong format. Empty = accept everything. Releases that
	// don't declare a language are assumed fine — most untagged releases are
	// in the local lingua franca, and rejecting them would empty every search.
	Languages []string
	// BookTitle guards title-based language detection: when the language word
	// is part of the book's own title ("French Knot"), every release for the
	// book would carry it, so it proves nothing and must not be penalized.
	// Explicit per-release language metadata is unaffected by this.
	BookTitle string
}

var knownFormats = []string{"epub", "azw3", "azw", "mobi", "pdf", "cbz", "cbr", "djvu", "fb2", "txt"}

var formatRe = func() *regexp.Regexp {
	return regexp.MustCompile(`(?i)\b(` + strings.Join(knownFormats, "|") + `)\b`)
}()

// DetectFormat pulls a book format out of a release title or file name.
func DetectFormat(title string) string {
	m := formatRe.FindString(title)
	return strings.ToLower(m)
}

// DownloadFilename picks the name to save a downloaded file under, best source
// first: the response's Content-Disposition filename (authoritative — it's what
// the server calls the file), then the URL path's base, then fallback. A direct
// source often serves the bytes from a redirect endpoint whose URL path is a
// meaningless token like ".../redirection", so the URL base alone yields a name
// with no extension — and the importer keys the delivered file's FORMAT off that
// extension, mislabeling e.g. a pdf as the epub default. Content-Disposition
// carries the real name (and extension), so it must win.
//
// wantFormat, when set (the release's known format), supplies the extension if
// the chosen name still lacks one — so the format is never lost.
func DownloadFilename(res *http.Response, fileURL, wantFormat, fallback string) string {
	name := ""
	if res != nil {
		if cd := res.Header.Get("Content-Disposition"); cd != "" {
			if _, params, err := mime.ParseMediaType(cd); err == nil {
				name = strings.TrimSpace(params["filename"])
			}
		}
	}
	if name == "" {
		name = filepath.Base(strings.SplitN(fileURL, "?", 2)[0])
	}
	// strip any path components a filename* value might smuggle in
	name = filepath.Base(name)
	if name == "" || name == "." || name == "/" {
		name = fallback
	}
	if filepath.Ext(name) == "" && wantFormat != "" {
		name += "." + strings.ToLower(wantFormat)
	}
	return truncateFilename(name)
}

// maxFilenameBytes caps saved names well under the common 255-byte
// per-component filesystem limit. Anna's partner servers name files with the
// full title, subtitle and author list — long enough to make the initial
// write fail with "file name too long" before the importer ever renames it.
const maxFilenameBytes = 200

// truncateFilename shortens an overlong name, keeping its extension and
// never splitting a multi-byte rune.
func truncateFilename(name string) string {
	if len(name) <= maxFilenameBytes {
		return name
	}
	ext := filepath.Ext(name)
	if len(ext) > 16 {
		ext = "" // a "." deep inside prose, not a real extension
	}
	base := strings.TrimSuffix(name, ext)
	keep := maxFilenameBytes - len(ext)
	if keep > len(base) {
		keep = len(base)
	}
	for keep > 0 && keep < len(base) && !utf8.RuneStart(base[keep]) {
		keep--
	}
	return strings.TrimRight(base[:keep], " .") + ext
}

const (
	languageWeight = 100_000_000 // a wrong language sinks below every wrong format
	formatWeight   = 1_000_000   // format order dominates the rest
	termWeight     = 1_000       // then preferred/avoided terms
	sourceWeight   = 1           // then source priority breaks ties
)

// languageAliases maps codes and native names to one canonical lowercase
// English name, so "sv", "swe", "svenska" and "Swedish" all compare equal.
var languageAliases = map[string]string{
	"en": "english", "eng": "english", "english": "english",
	"sv": "swedish", "swe": "swedish", "swedish": "swedish", "svenska": "swedish",
	"de": "german", "ger": "german", "deu": "german", "german": "german", "deutsch": "german",
	"fr": "french", "fre": "french", "fra": "french", "french": "french", "francais": "french", "français": "french",
	"es": "spanish", "spa": "spanish", "spanish": "spanish", "espanol": "spanish", "español": "spanish", "castellano": "spanish",
	"it": "italian", "ita": "italian", "italian": "italian", "italiano": "italian",
	//nolint:misspell // "portugues" is the Portuguese-language spelling, not a typo
	"pt": "portuguese", "por": "portuguese", "portuguese": "portuguese", "portugues": "portuguese", "português": "portuguese",
	"nl": "dutch", "dut": "dutch", "nld": "dutch", "dutch": "dutch", "nederlands": "dutch",
	"pl": "polish", "pol": "polish", "polish": "polish", "polski": "polish",
	"ru": "russian", "rus": "russian", "russian": "russian",
	"da": "danish", "dan": "danish", "danish": "danish", "dansk": "danish",
	"no": "norwegian", "nor": "norwegian", "norwegian": "norwegian", "norsk": "norwegian",
	"fi": "finnish", "fin": "finnish", "finnish": "finnish", "suomi": "finnish",
	"hu": "hungarian", "hun": "hungarian", "hungarian": "hungarian", "magyar": "hungarian",
	"cs": "czech", "cze": "czech", "ces": "czech", "czech": "czech",
	"ja": "japanese", "jpn": "japanese", "japanese": "japanese",
	"zh": "chinese", "chi": "chinese", "zho": "chinese", "chinese": "chinese",
	"ko": "korean", "kor": "korean", "korean": "korean",
	"ar": "arabic", "ara": "arabic", "arabic": "arabic",
	"tr": "turkish", "tur": "turkish", "turkish": "turkish",
	"el": "greek", "gre": "greek", "ell": "greek", "greek": "greek",
	"he": "hebrew", "heb": "hebrew", "hebrew": "hebrew",
	"ro": "romanian", "rum": "romanian", "ron": "romanian", "romanian": "romanian",
	"bg": "bulgarian", "bul": "bulgarian", "bulgarian": "bulgarian",
	"uk": "ukrainian", "ukr": "ukrainian", "ukrainian": "ukrainian",
	"vi": "vietnamese", "vie": "vietnamese", "vietnamese": "vietnamese",
	"th": "thai", "tha": "thai", "thai": "thai",
	"hi": "hindi", "hin": "hindi", "hindi": "hindi",
	"id": "indonesian", "ind": "indonesian", "indonesian": "indonesian",
	"sr": "serbian", "srp": "serbian", "serbian": "serbian",
	"hr": "croatian", "hrv": "croatian", "croatian": "croatian",
	"sk": "slovak", "slo": "slovak", "slk": "slovak", "slovak": "slovak",
	"sl": "slovenian", "slv": "slovenian", "slovenian": "slovenian",
	"lt": "lithuanian", "lit": "lithuanian", "lithuanian": "lithuanian",
	"lv": "latvian", "lav": "latvian", "latvian": "latvian",
	"et": "estonian", "est": "estonian", "estonian": "estonian",
	"fa": "persian", "per": "persian", "fas": "persian", "persian": "persian", "farsi": "persian",
	"ca": "catalan", "cat": "catalan", "catalan": "catalan",
}

// NormalizeLanguage canonicalizes explicit language metadata ("sv",
// "Swedish", "English [en]"). Unknown values pass through lowercased so an
// exotic language still filters consistently; empty stays empty.
func NormalizeLanguage(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return ""
	}
	// "English [en]"-style compound values: the word resolves first
	for _, tok := range strings.FieldsFunc(s, func(r rune) bool {
		return (r < 'a' || r > 'z') && (r < 'à' || r > 'ÿ')
	}) {
		if c, ok := languageAliases[tok]; ok {
			return c
		}
	}
	return s
}

// langNameRe matches full language names as words — never bare 2-letter
// codes, which collide with ordinary words ("it", "no", "es").
var langNameRe = func() *regexp.Regexp {
	seen := map[string]bool{}
	var names []string
	for alias, canon := range languageAliases {
		if len(alias) > 3 || alias == canon {
			if !seen[alias] {
				seen[alias] = true
				names = append(names, regexp.QuoteMeta(alias))
			}
		}
	}
	sort.Strings(names)
	return regexp.MustCompile(`(?i)(?:\b|_)(` + strings.Join(names, "|") + `)(?:\b|_)`)
}()

// langCodeRe matches bracketed language codes: "[sv]", "(GER)", "{fre}".
// Brackets are required — a bare code inside a title is too ambiguous.
var langCodeRe = regexp.MustCompile(`(?i)[\[({]\s*([a-z]{2,3})\s*[\])}]`)

// DetectLanguage finds an explicit language marker in a release title and
// returns its canonical name, or "" when the title says nothing about
// language (which is the common case and means "don't judge").
func DetectLanguage(title string) string {
	if m := langNameRe.FindStringSubmatch(title); m != nil {
		return languageAliases[strings.ToLower(m[1])]
	}
	return DetectLanguageCode(title)
}

// DetectLanguageCode finds only a bracketed language code ("[en]", "(SWE)")
// and returns its canonical name; "" when none is present. Sources whose
// metadata blocks mix the title with a tagged language row use this so words
// inside the title can't masquerade as a language.
func DetectLanguageCode(s string) string {
	for _, m := range langCodeRe.FindAllStringSubmatch(s, -1) {
		if c, ok := languageAliases[strings.ToLower(m[1])]; ok {
			return c
		}
	}
	return ""
}

// NormalizeLanguages splits a multi-language value ("en,fr" — translated
// editions are often tagged with both the original and edition language)
// and canonicalizes each entry.
func NormalizeLanguages(s string) []string {
	var out []string
	seen := map[string]bool{}
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == '·' }) {
		if l := NormalizeLanguage(part); l != "" && !seen[l] {
			seen[l] = true
			out = append(out, l)
		}
	}
	return out
}

// languageAccepted reports whether lang (canonical) is in the accepted list.
func languageAccepted(lang string, accepted []string) bool {
	for _, a := range accepted {
		if NormalizeLanguage(a) == lang {
			return true
		}
	}
	return false
}

// Score computes a release's rank. Releases in a format the profile doesn't
// list at all score negative and are filtered by Rank unless nothing else
// exists. sourceOrder is the user's source priority, best first; entries are
// matched by Release.Source prefix ("prowlarr" covers every indexer).
func Score(r Release, p Profile, sourceOrder []string) int {
	score := 0

	// language gate: explicit metadata always counts, and a record tagged
	// with several languages (translated editions carry the original AND the
	// edition language) is rejected if ANY of them is unwanted — ambiguity
	// is not worth an automatic grab. A language word in the title counts
	// unless the book's own title carries the same word.
	if len(p.Languages) > 0 {
		if langs := NormalizeLanguages(r.Language); len(langs) > 0 {
			for _, lang := range langs {
				if !languageAccepted(lang, p.Languages) {
					score -= languageWeight
					break
				}
			}
		} else if lang := DetectLanguage(r.Title); lang != "" && !languageAccepted(lang, p.Languages) {
			if p.BookTitle == "" || DetectLanguage(p.BookTitle) != lang {
				score -= languageWeight
			}
		}
	}

	format := r.Format
	if format == "" {
		format = DetectFormat(r.Title)
	}
	rank := -1
	for i, f := range p.Formats {
		if strings.EqualFold(f, format) {
			rank = i
			break
		}
	}
	if rank == -1 {
		score -= formatWeight // unknown or unwanted format sinks below every listed one
	} else {
		score += (len(p.Formats) - rank) * formatWeight
	}

	lower := strings.ToLower(r.Title)
	for _, t := range p.PreferredTerms {
		if t != "" && strings.Contains(lower, strings.ToLower(t)) {
			score += termWeight
		}
	}
	for _, t := range p.AvoidedTerms {
		if t != "" && strings.Contains(lower, strings.ToLower(t)) {
			score -= termWeight
		}
	}

	for i, s := range sourceOrder {
		if s != "" && (r.Source == s || strings.HasPrefix(r.Source, s+":")) {
			score += (len(sourceOrder) - i) * sourceWeight
			break
		}
	}
	return score
}

// Rank scores and sorts releases best-first (stable for equal scores) and
// drops releases the profile rejects (wrong format or wrong language),
// unless that would leave nothing — a rejected copy still beats no copy for
// manual picks, and AutoGrab never takes anything non-positive anyway.
func Rank(releases []Release, p Profile, sourceOrder []string) []Release {
	scored := make([]Release, len(releases))
	copy(scored, releases)
	for i := range scored {
		if scored[i].Format == "" {
			scored[i].Format = DetectFormat(scored[i].Title)
		}
		scored[i].Score = Score(scored[i], p, sourceOrder)
	}
	// insertion sort keeps it stable without importing sort for a custom less
	for i := 1; i < len(scored); i++ {
		for j := i; j > 0 && scored[j].Score > scored[j-1].Score; j-- {
			scored[j], scored[j-1] = scored[j-1], scored[j]
		}
	}
	keep := scored[:0:0]
	for _, r := range scored {
		if r.Score > -formatWeight/2 {
			keep = append(keep, r)
		}
	}
	if len(keep) == 0 {
		return scored
	}
	return keep
}
