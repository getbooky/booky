package readalong

import (
	"sort"
	"strings"
	"unicode"
)

// Word is one recognised word of the narration, with its time in seconds
// from the start of the audiobook.
type Word struct {
	Text       string
	Start, End float64
}

// Timing is when one unit is spoken, in seconds from the start of the
// audiobook.
type Timing struct {
	ID         string  `json:"id"`
	Href       string  `json:"href"`
	Begin, End float64 `json:"-"`
}

// Stats says how well a book lined up: the share of the book's words found
// in the narration, and of its units that got a time.
type Stats struct {
	Words        int     `json:"words"`
	HeardWords   int     `json:"heardWords"`
	MatchedWords int     `json:"matchedWords"`
	Units        int     `json:"units"`
	TimedUnits   int     `json:"timedUnits"`
	Coverage     float64 `json:"coverage"` // TimedUnits / Units
	// Match is the share of words that found their counterpart, out of the
	// smaller side — what was heard, or what the book holds. Two unrelated
	// texts still share their "the"s and "of"s, so this stays well short of
	// a real pairing, which Coverage (borrowed times included) can't tell.
	Match float64 `json:"match"`
}

const (
	// maxWordsPerSecond is faster than anyone narrates. A stretch of book
	// text that would have to be read faster than this to fit between two
	// matched words was not narrated (front matter, an appendix the reader
	// skipped) and gets no time rather than a blur of highlights.
	maxWordsPerSecond = 7.0
	// dpLimit caps the word-by-word alignment of a gap between anchors
	// (book words × transcript words); bigger gaps are interpolated.
	dpLimit = 400 * 400
	// joinGap: when a unit ends this close to the next one's start, it is
	// stretched to meet it, so the highlight doesn't flicker off between
	// sentences.
	joinGap = 2.0
)

// Align works out when each unit is spoken. The transcript is imperfect —
// speech recognition mishears names and rare words, narrators add intros and
// chapter announcements, books carry text nobody reads aloud — so it never
// needs every word to match: unique runs of words that do match pin the two
// texts together, the gaps between them are matched word by word where
// that's cheap, and whatever is left takes its time from its neighbours.
func Align(units []Unit, words []Word) ([]Timing, Stats) {
	// the book as one word sequence, remembering which unit each word is in
	var book []string
	var unitOf []int
	for ui, u := range units {
		for _, w := range tokenize(u.Text) {
			book = append(book, w)
			unitOf = append(unitOf, ui)
		}
	}
	var heard []string
	var heardAt []int // index into words
	for wi, w := range words {
		for _, t := range tokenize(w.Text) {
			heard = append(heard, t)
			heardAt = append(heardAt, wi)
		}
	}

	match := make([]int, len(book)) // book word → heard word, or -1
	for i := range match {
		match[i] = -1
	}
	a := aligner{book: book, heard: heard, match: match}
	a.align(0, len(book), 0, len(heard))

	// every book word gets a start and end time, or none
	start := make([]float64, len(book))
	end := make([]float64, len(book))
	timed := make([]bool, len(book))
	matched := 0
	for i, m := range match {
		if m >= 0 {
			w := words[heardAt[m]]
			start[i], end[i], timed[i] = w.Start, w.End, true
			matched++
		}
	}
	interpolate(start, end, timed, match)

	// a unit runs from its first timed word to its last
	stats := Stats{Words: len(book), HeardWords: len(heard), MatchedWords: matched, Units: len(units)}
	var out []Timing
	seen := make([]bool, len(units))
	first := make([]int, len(units))
	last := make([]int, len(units))
	for i := range book {
		if !timed[i] {
			continue
		}
		u := unitOf[i]
		if !seen[u] {
			seen[u], first[u] = true, i
		}
		last[u] = i
	}
	for ui, u := range units {
		if !seen[ui] {
			continue
		}
		out = append(out, Timing{ID: u.ID, Href: u.Href, Begin: start[first[ui]], End: end[last[ui]]})
	}
	smooth(out)
	stats.TimedUnits = len(out)
	if stats.Units > 0 {
		stats.Coverage = float64(stats.TimedUnits) / float64(stats.Units)
	}
	if n := min(stats.Words, stats.HeardWords); n > 0 {
		stats.Match = float64(matched) / float64(n)
	}
	return out, stats
}

// tokenize lowercases and splits on anything but letters and digits;
// apostrophes are dropped so "Alice’s" and "alice's" both read "alices".
func tokenize(s string) []string {
	s = strings.Map(func(r rune) rune {
		if r == '\'' || r == '’' || r == 'ʼ' {
			return -1
		}
		return unicode.ToLower(r)
	}, s)
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

type aligner struct {
	book, heard []string
	match       []int
}

// align matches book[b0:b1] against heard[h0:h1]: anchors first (runs of n
// words occurring exactly once in both ranges, kept in order), then the same
// again inside each gap with shorter runs, then word-by-word for small gaps.
func (a *aligner) align(b0, b1, h0, h1 int) {
	if b0 >= b1 || h0 >= h1 {
		return
	}
	for n := 4; n >= 2; n-- {
		anchors := a.anchors(b0, b1, h0, h1, n)
		if len(anchors) == 0 {
			continue
		}
		pb, ph := b0, h0
		for _, an := range anchors {
			a.align(pb, an.b, ph, an.h)
			for k := 0; k < n; k++ {
				a.match[an.b+k] = an.h + k
			}
			pb, ph = an.b+n, an.h+n
		}
		a.align(pb, b1, ph, h1)
		return
	}
	if (b1-b0)*(h1-h0) <= dpLimit {
		a.lcs(b0, b1, h0, h1)
	}
}

type anchor struct{ b, h int }

// anchors finds n-word runs that occur exactly once in book[b0:b1] and once
// in heard[h0:h1], then keeps the longest chain that runs forward in both
// without overlapping.
func (a *aligner) anchors(b0, b1, h0, h1, n int) []anchor {
	if b1-b0 < n || h1-h0 < n {
		return nil
	}
	key := func(ws []string, i int) string { return strings.Join(ws[i:i+n], " ") }
	type seen struct{ at, count int }
	inBook := map[string]*seen{}
	for i := b0; i+n <= b1; i++ {
		k := key(a.book, i)
		if s := inBook[k]; s != nil {
			s.count++
		} else {
			inBook[k] = &seen{at: i, count: 1}
		}
	}
	inHeard := map[string]*seen{}
	for i := h0; i+n <= h1; i++ {
		k := key(a.heard, i)
		if s := inHeard[k]; s != nil {
			s.count++
		} else if inBook[k] != nil {
			inHeard[k] = &seen{at: i, count: 1}
		}
	}
	var cands []anchor
	for k, sb := range inBook {
		sh := inHeard[k]
		if sb.count == 1 && sh != nil && sh.count == 1 {
			cands = append(cands, anchor{sb.at, sh.at})
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].b < cands[j].b })
	chain := increasingChain(cands)
	// drop overlaps — consecutive anchors must not share words on either side
	out := chain[:0]
	for _, c := range chain {
		if len(out) > 0 {
			p := out[len(out)-1]
			if c.b < p.b+n || c.h < p.h+n {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// increasingChain returns the longest run of candidates (already sorted by
// book position) whose heard positions also increase — patience sorting,
// O(n log n).
func increasingChain(c []anchor) []anchor {
	if len(c) == 0 {
		return nil
	}
	tails := []int{} // index into c of the smallest tail of each length
	prev := make([]int, len(c))
	for i := range c {
		k := sort.Search(len(tails), func(j int) bool { return c[tails[j]].h >= c[i].h })
		if k > 0 {
			prev[i] = tails[k-1]
		} else {
			prev[i] = -1
		}
		if k == len(tails) {
			tails = append(tails, i)
		} else {
			tails[k] = i
		}
	}
	out := make([]anchor, len(tails))
	for i, k := len(tails)-1, tails[len(tails)-1]; i >= 0; i, k = i-1, prev[k] {
		out[i] = c[k]
	}
	return out
}

// lcs matches a small gap word by word: the longest common subsequence of
// the two ranges.
func (a *aligner) lcs(b0, b1, h0, h1 int) {
	n, m := b1-b0, h1-h0
	// dp[i][j] = LCS of book[b0+i:b1] and heard[h0+j:h1], in one flat slice
	dp := make([]int32, (n+1)*(m+1))
	at := func(i, j int) *int32 { return &dp[i*(m+1)+j] }
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a.book[b0+i] == a.heard[h0+j] {
				*at(i, j) = *at(i+1, j+1) + 1
			} else {
				*at(i, j) = max(*at(i+1, j), *at(i, j+1))
			}
		}
	}
	for i, j := 0, 0; i < n && j < m; {
		switch {
		case a.book[b0+i] == a.heard[h0+j]:
			a.match[b0+i] = h0 + j
			i++
			j++
		case *at(i+1, j) >= *at(i, j+1):
			i++
		default:
			j++
		}
	}
}

// interpolate gives unmatched book words a time between their matched
// neighbours, spread evenly by word. Words before the first match or after
// the last stay untimed (nobody read them), and so do gaps that would have to
// be read impossibly fast to fit.
func interpolate(start, end []float64, timed []bool, match []int) {
	prev := -1
	for i := 0; i <= len(match); i++ {
		if i < len(match) && match[i] < 0 {
			continue
		}
		if prev >= 0 && i < len(match) && i-prev > 1 {
			gap := i - prev - 1
			from, to := end[prev], start[i]
			span := to - from
			if span > 0 && float64(gap)/span <= maxWordsPerSecond {
				step := span / float64(gap)
				for k := 1; k <= gap; k++ {
					w := prev + k
					start[w] = from + step*float64(k-1)
					end[w] = from + step*float64(k)
					timed[w] = true
				}
			}
		}
		prev = i
	}
}

// smooth keeps units in order and joins them up: a unit never starts before
// the one before it ends, and short pauses between units are absorbed so
// the highlight moves straight from one to the next.
func smooth(t []Timing) {
	for i := range t {
		if i > 0 && t[i].Begin < t[i-1].End {
			t[i].Begin = t[i-1].End
		}
		if t[i].End < t[i].Begin {
			t[i].End = t[i].Begin
		}
	}
	for i := 0; i+1 < len(t); i++ {
		if gap := t[i+1].Begin - t[i].End; gap > 0 && gap <= joinGap {
			t[i].End = t[i+1].Begin
		}
	}
}
