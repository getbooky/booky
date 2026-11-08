package release

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// TitleMatches reports whether a release is the book: its title must appear
// in the release name as a whole phrase, standing on its own.
//
// Searches come back with the author's other books, and those are the
// dangerous ones — "A Court of Silver Flames" for "A Court of Forgotten
// Melody". Worse, release names carry series names: "A Court of Frost and
// Starlight (A Court of Thorns and Roses)" and "Court of Thorns and Roses 3:
// A Court of Wings and Ruin" both contain the words of book one. So:
//
//   - the title's words must appear in order, as a run (small words like
//     "a", "of", "the" don't count either way);
//   - anything in brackets is ignored — that is where series names live;
//   - the run must stand alone: what comes before it may only be a
//     separator, the start, the author, a year or a number ("03 - Title");
//     what comes after only a separator, the end, the author, a year, or
//     release words like "epub" or "retail". "Dune" followed by "Messiah",
//     or a series name followed by its number, is another book.
//
// A subtitle is optional: "Project Hail Mary: A Novel" matches a release
// called "Project Hail Mary" and one that spells out the subtitle.
func TitleMatches(bookTitle, author, releaseTitle string) bool {
	main, sub, _ := strings.Cut(bookTitle, ":")
	want := significant(words(main))
	if len(want) == 0 {
		want = significant(words(bookTitle))
	}
	if len(want) == 0 {
		return true // nothing to check against
	}
	full := append(append([]string{}, want...), significant(words(sub))...)
	authorWords := map[string]bool{}
	for _, w := range words(author) {
		authorWords[w] = true
	}
	toks := releaseTokens(releaseTitle)
	return hasStandaloneRun(toks, full, authorWords) || hasStandaloneRun(toks, want, authorWords)
}

// AuthorNamed reports whether a release name names the book's author: their
// surname anywhere, or — since indexers cut long names short ("… Sarah J")
// — the start of their name at the very end of the release name. A first
// name alone elsewhere doesn't count: "It - Stephen Chbosky" isn't by
// Stephen King. With no author to look for, any release passes.
func AuthorNamed(author, releaseTitle string) bool {
	aw := words(author)
	if len(aw) == 0 {
		return true
	}
	rw := words(releaseTitle)
	surname := aw[len(aw)-1]
	for _, w := range rw {
		if w == surname {
			return true
		}
	}
	// the cut-short name: trailing format words and numbers set aside
	end := len(rw)
	for end > 0 && (releaseWords[rw[end-1]] || isNumber(rw[end-1]) || isNumber(strings.TrimSuffix(rw[end-1], "kbps"))) {
		end--
	}
	rw = rw[:end]
	if len(aw[0]) < 2 {
		return false // an initial alone says too little
	}
	// the name's last k words are the author's first k, the last of them
	// perhaps cut off mid-word ("Mara V")
	for k := len(aw); k >= 1; k-- {
		if k > len(rw) {
			continue
		}
		tail := rw[len(rw)-k:]
		ok := true
		for i, w := range tail {
			if w != aw[i] && (i != k-1 || i == 0 || !strings.HasPrefix(aw[i], w)) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// sep marks a boundary between parts of a release name.
const sep = "\x00"

// stopwords don't count as title words: "A Court of Thorns and Roses" is
// matched as court, thorns, roses.
var stopwords = map[string]bool{
	"a": true, "an": true, "the": true, "of": true, "and": true, "in": true, "on": true, "at": true,
	"to": true, "for": true, "by": true, "with": true, "from": true, "or": true,
}

// releaseWords may follow a title in a release name without making it a
// different book.
var releaseWords = map[string]bool{
	"epub": true, "mobi": true, "azw": true, "azw3": true, "pdf": true, "cbz": true, "cbr": true, "fb2": true,
	"djvu": true, "txt": true, "kepub": true, "ebook": true, "retail": true, "scan": true, "ocr": true,
	"m4b": true, "mp3": true, "m4a": true, "aac": true, "flac": true, "kbps": true, "audiobook": true,
	"unabridged": true, "abridged": true, "read": true, "narrated": true, "narrator": true,
	"edition": true, "illustrated": true, "special": true, "deluxe": true, "anniversary": true,
	"kindle": true, "novel": true, "english": true,
}

// words lowercases and splits on anything that isn't a letter or digit,
// joining contractions ("Ender's" is "enders") and spelling "&" as "and".
func words(s string) []string {
	s = strings.NewReplacer("&", " and ", "'", "", "’", "", "‘", "").Replace(strings.ToLower(s))
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

func significant(ws []string) []string {
	out := ws[:0:0]
	for _, w := range ws {
		if !stopwords[w] {
			out = append(out, w)
		}
	}
	return out
}

// releaseTokens turns a release name into words and boundaries, dropping
// bracketed text and small words.
func releaseTokens(s string) []string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '(', '[', '{':
			depth++
			b.WriteString(" " + sep + " ")
			continue
		case ')', ']', '}':
			if depth > 0 {
				depth--
			}
			b.WriteString(" " + sep + " ")
			continue
		}
		if depth > 0 {
			continue
		}
		switch r {
		case ':', '—', '–', '|', '/', ';', ',', '~':
			b.WriteString(" " + sep + " ")
		default:
			b.WriteRune(r)
		}
	}
	var out []string
	// " - " is a separator; a hyphen inside a word ("Spider-Man") is not
	for _, part := range strings.Split(b.String(), " - ") {
		if len(out) > 0 {
			out = append(out, sep)
		}
		for _, f := range strings.Fields(part) {
			if f == sep {
				out = append(out, sep)
				continue
			}
			for _, w := range words(f) {
				if !stopwords[w] {
					out = append(out, w)
				}
			}
		}
	}
	return out
}

func isNumber(w string) bool {
	for _, r := range w {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return w != ""
}

func isYear(w string) bool {
	return len(w) == 4 && isNumber(w) && w >= "1000" && w <= "2199"
}

// hasStandaloneRun finds want as a contiguous run of toks with acceptable
// neighbours on both sides.
func hasStandaloneRun(toks, want []string, author map[string]bool) bool {
	if len(want) == 0 {
		return false
	}
	for i := 0; i+len(want) <= len(toks); i++ {
		match := true
		for j, w := range want {
			if toks[i+j] != w {
				match = false
				break
			}
		}
		if !match {
			continue
		}
		before, after := sep, sep
		if i > 0 {
			before = toks[i-1]
		}
		if k := i + len(want); k < len(toks) {
			after = toks[k]
		}
		okBefore := before == sep || author[before] || isNumber(before)
		okAfter := after == sep || author[after] || isYear(after) || releaseWords[after]
		if okBefore && okAfter {
			return true
		}
	}
	return false
}

// partOfSetRe finds a release that says it's one piece of a set: "Part 1 of
// 2", "Pt.2 of 3", "CD 1 of 12", "Disc 2/2". Only the "N of M" forms count
// — a bare "Part One" is often the book's own title.
var partOfSetRe = regexp.MustCompile(`(?i)\b(?:part|pt|cd|disc|disk)\.?\s*(\d{1,3})\s*(?:of|/)\s*(\d{1,3})\b`)

// PartOfSet reports whether a release names itself as one piece of several
// separate downloads. An audiobook posted that way arrives half at a time,
// and a grab would file half a book as if it were all of it.
func PartOfSet(title string) bool {
	for _, m := range partOfSetRe.FindAllStringSubmatch(title, -1) {
		n, _ := strconv.Atoi(m[1])
		of, _ := strconv.Atoi(m[2])
		if of > 1 && n >= 1 && n <= of {
			return true
		}
	}
	return false
}

// bundleRe finds a release that says it holds several books: a range of
// book numbers ("Books 1-4", "#1-3", "Vol. 1 to 6") or a collection word.
// Some indexers strip the dash, leaving "Books 1 4": after a plural, two
// numbers side by side are a range too.
var bundleRe = regexp.MustCompile(`(?i)(?:\b(?:books?|bk|vols?|volumes?)\.?|#)\s*\d{1,3}\s*(?:-|–|—|to|thru|through|&|and)\s*\d{1,3}\b` +
	`|\b(?:books|vols|volumes)\.?\s*\d{1,3}\s+\d{1,3}\b` +
	`|\b(?:box(?:ed)?[ -]?set|collection|omnibus|anthology|complete series|series set|bundle)\b` +
	`|\b(?:trilogy|duology|quartet|series)\s*(?:$|[\[({]|\b(?:m4b|mp3|epub|unabridged|audiobook)\b)`)

// Bundle reports whether a release holds several books — a series pack —
// rather than one. Book names listed with " / " count too ("A / B / C").
// A bundle is never grabbed automatically: the download has to be split
// into its books, and only then is it known whether this book is in it.
func Bundle(title string) bool {
	if bundleRe.MatchString(title) {
		return true
	}
	// "Title A / Title B / Title C": two or more real names between slashes
	parts := strings.Split(title, " / ")
	if len(parts) < 2 {
		return false
	}
	named := 0
	for _, p := range parts {
		if len(significant(words(p))) >= 1 {
			named++
		}
	}
	return named >= 2
}

// BundleFor reports whether a release is a bundle that may hold this book:
// one that names the book, its series, or its author. Bundles of other
// authors' books aren't worth showing.
func BundleFor(bookTitle, author, series, releaseTitle string) bool {
	if !Bundle(releaseTitle) {
		return false
	}
	have := map[string]bool{}
	for _, w := range words(releaseTitle) {
		have[w] = true
	}
	// the author's surname
	if ws := words(author); len(ws) > 0 && len(ws[len(ws)-1]) > 1 && have[ws[len(ws)-1]] {
		return true
	}
	allIn := func(s string) bool {
		ws := significant(words(s))
		if len(ws) == 0 {
			return false
		}
		for _, w := range ws {
			if !have[w] {
				return false
			}
		}
		return true
	}
	return allIn(series) || allIn(bookTitle)
}

// emptyBrackets is what's left of "(Part 1 of 2)" once the part is cut out.
var emptyBrackets = regexp.MustCompile(`[(\[{]\s*[)\]}]`)

// SetPart reads a release that is one piece of a set: which part it is,
// how many there are, the name with the part cut out ("Book - Part 1 of 2
// M4B" → "Book M4B"), and a key that the set's other parts share.
func SetPart(title string) (base, key string, n, of int, ok bool) {
	for _, m := range partOfSetRe.FindAllStringSubmatchIndex(title, -1) {
		n, _ = strconv.Atoi(title[m[2]:m[3]])
		of, _ = strconv.Atoi(title[m[4]:m[5]])
		if of < 2 || n < 1 || n > of {
			continue
		}
		base = title[:m[0]] + " " + title[m[1]:]
		base = emptyBrackets.ReplaceAllString(base, " ")
		base = strings.Join(strings.Fields(base), " ")
		base = strings.Trim(base, " -–—_.,:")
		key = strings.Join(words(base), " ") + "|" + strconv.Itoa(of)
		return base, key, n, of, true
	}
	return "", "", 0, 0, false
}

// BundleNames reports whether a bundle's name points at this book: it names
// the book's series, or the book itself. That's what an automatic grab of a
// pack needs; a pack named only for the author could be any of their books.
func BundleNames(bookTitle, author, series, releaseTitle string) bool {
	if !Bundle(releaseTitle) {
		return false
	}
	have := map[string]bool{}
	for _, w := range words(releaseTitle) {
		have[w] = true
	}
	allIn := func(s string) bool {
		ws := significant(words(s))
		if len(ws) == 0 {
			return false
		}
		for _, w := range ws {
			if !have[w] {
				return false
			}
		}
		return true
	}
	return allIn(series) || TitleMatches(bookTitle, author, releaseTitle)
}
