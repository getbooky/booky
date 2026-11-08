package readalong

import (
	"fmt"
	"math"
	"math/rand"
	"strings"
	"testing"
	"time"
)

// synthBook builds n sentences of pseudo-natural text: a skewed vocabulary,
// so common words repeat a lot and rare ones barely at all, like real prose.
func synthBook(r *rand.Rand, sentences int) []Unit {
	vocab := make([]string, 3000)
	for i := range vocab {
		vocab[i] = fmt.Sprintf("w%dx", i)
	}
	var units []Unit
	for s := 0; s < sentences; s++ {
		n := 6 + r.Intn(18)
		words := make([]string, n)
		for i := range words {
			// squaring a uniform draw skews towards the start of the vocabulary
			f := r.Float64()
			words[i] = vocab[int(f*f*float64(len(vocab)))]
		}
		units = append(units, Unit{ID: fmt.Sprintf("booky-s%d", s+1), Href: "c.xhtml", Text: strings.Join(words, " ") + "."})
	}
	return units
}

// narrate reads units aloud at a steady pace, returning the ideal transcript
// and when each unit starts. skip says which units the narrator leaves out.
func narrate(units []Unit, skip func(i int) bool) ([]Word, map[string]float64) {
	var words []Word
	starts := map[string]float64{}
	t := 0.0
	for i, u := range units {
		if skip != nil && skip(i) {
			continue
		}
		starts[u.ID] = t
		for _, w := range tokenize(u.Text) {
			words = append(words, Word{Text: w, Start: t, End: t + 0.3})
			t += 0.35
		}
		t += 0.4 // pause between sentences
	}
	return words, starts
}

// garble is what speech recognition makes of a narration: misheard,
// dropped and invented words.
func garble(r *rand.Rand, words []Word, wrong, drop, insert float64) []Word {
	var out []Word
	for _, w := range words {
		switch f := r.Float64(); {
		case f < drop:
			continue
		case f < drop+wrong:
			w.Text = fmt.Sprintf("misheard%d", r.Intn(1e6))
		}
		out = append(out, w)
		if r.Float64() < insert {
			out = append(out, Word{Text: "um", Start: w.End, End: w.End + 0.1})
		}
	}
	return out
}

func timingErrors(t *testing.T, got []Timing, want map[string]float64) (maxErr float64, within1s float64) {
	t.Helper()
	ok := 0
	for _, g := range got {
		w, found := want[g.ID]
		if !found {
			continue
		}
		e := math.Abs(g.Begin - w)
		maxErr = math.Max(maxErr, e)
		if e <= 1.0 {
			ok++
		}
	}
	return maxErr, float64(ok) / float64(len(want))
}

func TestAlignPerfectTranscript(t *testing.T) {
	r := rand.New(rand.NewSource(1)) //nolint:gosec // G404: deterministic test noise
	units := synthBook(r, 300)
	words, starts := narrate(units, nil)
	timings, stats := Align(units, words)
	if stats.Coverage != 1 || stats.MatchedWords != stats.Words {
		t.Fatalf("stats = %+v", stats)
	}
	for _, tm := range timings {
		if math.Abs(tm.Begin-starts[tm.ID]) > 1e-9 {
			t.Fatalf("%s begins %.3f, want %.3f", tm.ID, tm.Begin, starts[tm.ID])
		}
	}
}

func TestAlignNoisyNarration(t *testing.T) {
	for seed := int64(1); seed <= 5; seed++ {
		r := rand.New(rand.NewSource(seed)) //nolint:gosec // G404: deterministic test noise
		units := synthBook(r, 1500)
		words, starts := narrate(units, nil)
		// a narrator's intro and chapter announcements the book doesn't have
		intro := []Word{}
		for i, w := range strings.Fields("this is a librivox recording all librivox recordings are in the public domain") {
			intro = append(intro, Word{Text: w, Start: float64(i) * 0.3, End: float64(i)*0.3 + 0.25})
		}
		shift := 6.0
		for i := range words {
			words[i].Start += shift
			words[i].End += shift
		}
		for k := range starts {
			starts[k] += shift
		}
		heard := append(intro, garble(r, words, 0.15, 0.04, 0.03)...)

		timings, stats := Align(units, heard)
		maxErr, within := timingErrors(t, timings, starts)
		if stats.Coverage < 0.98 {
			t.Errorf("seed %d: only %.1f%% of units timed", seed, 100*stats.Coverage)
		}
		if within < 0.97 {
			t.Errorf("seed %d: only %.1f%% of units within 1s (max error %.2fs)", seed, 100*within, maxErr)
		}
		for i := 1; i < len(timings); i++ {
			if timings[i].Begin < timings[i-1].End-1e-9 {
				t.Fatalf("seed %d: %s overlaps the unit before it", seed, timings[i].ID)
			}
		}
	}
}

func TestAlignLeavesUnnarratedTextUntimed(t *testing.T) {
	r := rand.New(rand.NewSource(7)) //nolint:gosec // G404: deterministic test noise
	units := synthBook(r, 600)
	// front matter (units 0-19) and an appendix in the middle (300-339)
	// were never read aloud
	skipped := func(i int) bool { return i < 20 || (i >= 300 && i < 340) }
	words, starts := narrate(units, skipped)
	timings, _ := Align(units, garble(r, words, 0.1, 0.03, 0.02))

	timed := map[string]bool{}
	for _, tm := range timings {
		timed[tm.ID] = true
	}
	stray := 0
	for i, u := range units {
		if skipped(i) && timed[u.ID] {
			stray++
		}
	}
	// a unit or two at each edge can pick up a stray match; a section can't
	if stray > 4 {
		t.Errorf("%d unnarrated units got timings", stray)
	}
	_, within := timingErrors(t, timings, starts)
	if within < 0.97 {
		t.Errorf("only %.1f%% of narrated units within 1s", 100*within)
	}
}

func TestAlignRepetitiveText(t *testing.T) {
	// a refrain repeated word for word — no run of it is unique, so it has
	// to be placed from the text around it
	var units []Unit
	r := rand.New(rand.NewSource(3)) //nolint:gosec // G404: deterministic test noise
	base := synthBook(r, 200)
	for i, u := range base {
		units = append(units, u)
		if i%10 == 5 {
			units = append(units, Unit{ID: fmt.Sprintf("refrain%d", i), Href: "c.xhtml", Text: "Will you, won't you, will you, won't you, will you join the dance?"})
		}
	}
	words, starts := narrate(units, nil)
	timings, stats := Align(units, garble(r, words, 0.05, 0.02, 0.01))
	if stats.Coverage < 0.99 {
		t.Errorf("coverage %.3f", stats.Coverage)
	}
	if _, within := timingErrors(t, timings, starts); within < 0.98 {
		t.Errorf("only %.1f%% within 1s", 100*within)
	}
}

func TestAlignEdgeCases(t *testing.T) {
	if tm, st := Align(nil, nil); len(tm) != 0 || st.Units != 0 {
		t.Fatal("empty input")
	}
	units := []Unit{{ID: "a", Text: "Hello world."}}
	if tm, _ := Align(units, nil); len(tm) != 0 {
		t.Fatal("no narration should time nothing")
	}
	if tm, _ := Align(units, []Word{{Text: "unrelated", Start: 1, End: 2}}); len(tm) != 0 {
		t.Fatal("nothing matched should time nothing")
	}
	if tm, _ := Align([]Unit{{ID: "x", Text: "— * —"}}, []Word{{Text: "x", Start: 0, End: 1}}); len(tm) != 0 {
		t.Fatal("a unit without words can't be timed")
	}
}

func TestAlignFullLengthBookIsFast(t *testing.T) {
	if testing.Short() {
		t.Skip("long")
	}
	r := rand.New(rand.NewSource(11)) //nolint:gosec // G404: deterministic test noise
	units := synthBook(r, 8000)       // ~120k words, a long novel
	words, _ := narrate(units, nil)
	heard := garble(r, words, 0.12, 0.03, 0.02)
	begin := time.Now()
	_, stats := Align(units, heard)
	if took := time.Since(begin); took > 20*time.Second {
		t.Errorf("aligning %d words took %v", stats.Words, took)
	}
	if stats.Coverage < 0.98 {
		t.Errorf("coverage %.3f", stats.Coverage)
	}
}

func TestTokenize(t *testing.T) {
	got := tokenize("Alice’s rabbit-hole: “DON'T” 1865!")
	want := []string{"alices", "rabbit", "hole", "dont", "1865"}
	if !eq(got, want) {
		t.Fatalf("got %q", got)
	}
}

// TestSameBook: a narration heard through heavy noise is still this book;
// a narration of a different book that shares the vocabulary — the same
// author, the same characters — is not, however many words it shares.
func TestSameBook(t *testing.T) {
	for seed := int64(1); seed <= 3; seed++ {
		r := rand.New(rand.NewSource(seed)) //nolint:gosec // G404: deterministic test noise
		units := synthBook(r, 1200)
		other := synthBook(r, 1200) // drawn from the same vocabulary
		words, _ := narrate(units, nil)
		for _, wrong := range []float64{0, 0.3, 0.55} {
			heard := garble(r, words, wrong, 0.05, 0.05)
			_, s := Align(units, heard)
			s.Baseline = Baseline(units, heard)
			if !s.SameBook() {
				t.Errorf("seed %d, %.0f%% misheard: the right book refused (%+v)", seed, 100*wrong, s)
			}
			_, o := Align(other, heard)
			o.Baseline = Baseline(other, heard)
			if o.SameBook() {
				t.Errorf("seed %d, %.0f%% misheard: a different book accepted (%+v)", seed, 100*wrong, o)
			}
			if wrong == 0.55 && s.Match >= 0.45 {
				t.Errorf("seed %d: the noisy narration matched %.2f — not the case this test is for", seed, s.Match)
			}
		}
	}
}
