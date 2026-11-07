package release

import "testing"

var audioProfile = Profile{
	Audio:          true,
	Formats:        []string{"m4b", "mp3", "m4a"},
	PreferredTerms: []string{"unabridged"},
	RejectTerms:    []string{"abridged", "dramatized"},
	MinBytes:       5 << 20,
}

func TestDetectAudioFormat(t *testing.T) {
	cases := map[string]string{
		"Mara Voss - First Ember (Unabridged) [M4B]": "m4b",
		"First.Ember.2023.MP3.64kbps":                "mp3",
		"First Ember AAC 128k":                       "m4a",
		"First Ember m4a":                            "m4a",
		"First Ember EPUB":                           "",
		"First Ember mp3s":                           "", // not a whole word
	}
	for title, want := range cases {
		if got := DetectAudioFormat(title); got != want {
			t.Errorf("DetectAudioFormat(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestTermsMatchWholeWords(t *testing.T) {
	cases := []struct {
		title, term string
		want        bool
	}{
		{"First Ember (Unabridged) M4B", "abridged", false},
		{"First Ember (Abridged) M4B", "abridged", true},
		{"First Ember [abridged]", "abridged", true},
		{"The Scandal EPUB", "scan", false},
		{"The Scandal scan EPUB", "scan", true},
		{"Full Cast Dramatization", "full cast", true},
		{"Fullcast", "full cast", false},
		{"anything", "", false},
	}
	for _, c := range cases {
		if got := hasTerm(c.title, c.term); got != c.want {
			t.Errorf("hasTerm(%q, %q) = %v, want %v", c.title, c.term, got, c.want)
		}
	}
}

func TestAudioRanking(t *testing.T) {
	rels := []Release{
		{Title: "First Ember (Abridged) M4B", SizeBytes: 200 << 20},
		{Title: "First Ember MP3", SizeBytes: 300 << 20},
		{Title: "First Ember (Unabridged) M4B", SizeBytes: 400 << 20},
		{Title: "First Ember M4B sample", SizeBytes: 1 << 20}, // too small
		{Title: "First Ember EPUB", SizeBytes: 2 << 20},       // not audio
	}
	ranked := Rank(rels, audioProfile, nil)
	var titles []string
	for _, r := range ranked {
		titles = append(titles, r.Title)
	}
	// an abridged reading is skipped even in the better format: it can't be
	// read along with the book. So are the sample and the ebook.
	if len(ranked) != 2 || ranked[0].Title != "First Ember (Unabridged) M4B" || ranked[0].Format != "m4b" ||
		ranked[1].Title != "First Ember MP3" {
		t.Fatalf("ranked = %q", titles)
	}
	// an abridged-only search still has nothing to auto-grab
	if s := Score(Release{Title: "First Ember (Abridged) M4B", SizeBytes: 200 << 20}, audioProfile, nil); s > 0 {
		t.Errorf("abridged scored %d", s)
	}
	// the size limit only judges releases that say their size
	if s := Score(Release{Title: "First Ember M4B"}, audioProfile, nil); s <= 0 {
		t.Errorf("unknown size scored %d", s)
	}
	big := audioProfile
	big.MaxBytes = 300 << 20
	if s := Score(Release{Title: "First Ember M4B", SizeBytes: 400 << 20}, big, nil); s > 0 {
		t.Errorf("over the size limit scored %d", s)
	}
}

func TestPartOfSet(t *testing.T) {
	cases := map[string]bool{
		"Mara Voss - First Ember (Unabridged) Part 1 of 2 M4B": true,
		"First Ember Pt.2 of 2 MP3":                            true,
		"First Ember CD 3 of 12":                               true,
		"First Ember [Disc 2/2]":                               true,
		"First Ember (Unabridged) M4B":                         false,
		"Dune Part One":                                        false, // the book's own title
		"First Ember Part 3 of 2":                              false, // nonsense, not a set
		"First Ember Book 1 of 3":                              false, // a series, not a split
	}
	for title, want := range cases {
		if got := PartOfSet(title); got != want {
			t.Errorf("PartOfSet(%q) = %v, want %v", title, got, want)
		}
	}
	// half a book is never what an automatic grab takes
	rels := Rank([]Release{
		{Title: "First Ember (Unabridged) Part 1 of 2 M4B", SizeBytes: 300 << 20},
		{Title: "First Ember (Unabridged) MP3", SizeBytes: 300 << 20},
	}, audioProfile, nil)
	if len(rels) != 1 || rels[0].Title != "First Ember (Unabridged) MP3" {
		t.Fatalf("ranked = %+v", rels)
	}
	only := Rank([]Release{{Title: "First Ember (Unabridged) Part 1 of 2 M4B", SizeBytes: 300 << 20}}, audioProfile, nil)
	if len(only) != 1 || !only[0].PartOfSet || only[0].Score > 0 {
		t.Fatalf("a lone part = %+v", only)
	}
	// ebooks aren't judged on it
	if Rank([]Release{{Title: "First Ember Part 1 of 2 EPUB"}}, profile, nil)[0].PartOfSet {
		t.Error("ebook flagged as part of a set")
	}
}
