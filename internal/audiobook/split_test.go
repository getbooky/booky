package audiobook

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

var acotar = []BookRef{
	{ID: 1, Title: "A Court of Thorns and Roses", Author: "Sarah J. Maas", Series: "A Court of Thorns and Roses"},
	{ID: 2, Title: "A Court of Mist and Fury", Author: "Sarah J. Maas", Series: "A Court of Thorns and Roses"},
	{ID: 3, Title: "A Court of Wings and Ruin", Author: "Sarah J. Maas", Series: "A Court of Thorns and Roses"},
}

// tree makes a download folder from relative names; .m4b files get an M4B
// head, everything else an MP3 one.
func tree(t *testing.T, names ...string) string {
	t.Helper()
	dir := t.TempDir()
	for _, n := range names {
		p := filepath.Join(dir, n)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		head := mp3Head
		if strings.HasSuffix(n, ".m4b") {
			head = mp4Head
		}
		if err := os.WriteFile(p, head, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func titlesOf(s *Split, id int64) string {
	var out []string
	for _, p := range s.Books[id] {
		out = append(out, p.Title)
	}
	return strings.Join(out, "|")
}

func TestSplitOneBook(t *testing.T) {
	for name, dir := range map[string]string{
		"chapter mp3s":      tree(t, "01 - Chapter One.mp3", "02 - Chapter Two.mp3", "03 - The Pool.mp3"),
		"cd folders":        tree(t, "CD1/01.mp3", "CD1/02.mp3", "CD2/01.mp3"),
		"disc folders":      tree(t, "A Court of Thorns and Roses (Disc 1)/01.mp3", "A Court of Thorns and Roses (Disc 2)/01.mp3"),
		"two m4b parts":     tree(t, "ACOTAR - Part 1.m4b", "ACOTAR - Part 2.m4b"),
		"part n of m":       tree(t, "Book Part 1 of 2.m4b", "Book Part 2 of 2.m4b"),
		"m4b plus mp3 copy": tree(t, "A Court of Thorns and Roses.m4b", "mp3/01.mp3", "mp3/02.mp3", "sample.mp3"),
		"wrapped once":      tree(t, "Release.Name-GRP/01 - Chapter One.mp3", "Release.Name-GRP/02 - Chapter Two.mp3"),
		"single m4b":        tree(t, "Whatever.m4b", "cover.jpg", "info.nfo"),
	} {
		s, err := SplitByBook(dir, acotar)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if s.Bundle {
			t.Errorf("%s: read as a pack: %+v", name, s)
		}
	}
	// a single file is one book
	dir := tree(t, "a.mp3")
	if s, err := SplitByBook(filepath.Join(dir, "a.mp3"), acotar); err != nil || s.Bundle {
		t.Fatalf("single file: %+v %v", s, err)
	}
}

func TestSplitSeriesFolders(t *testing.T) {
	// books 1-4 in folders, wrapped in the release's own folder; book 4
	// isn't in the library; book 3 comes on two CDs
	dir := tree(t,
		"ACOTAR Books 1-4/01 - A Court of Thorns and Roses/01.mp3",
		"ACOTAR Books 1-4/01 - A Court of Thorns and Roses/02.mp3",
		"ACOTAR Books 1-4/02 - A Court of Mist and Fury (2016)/Part 1.m4b",
		"ACOTAR Books 1-4/03 - A Court of Wings and Ruin/CD2/01.mp3",
		"ACOTAR Books 1-4/03 - A Court of Wings and Ruin/CD1/01.mp3",
		"ACOTAR Books 1-4/04 - A Court of Frost and Starlight/01.mp3",
		"ACOTAR Books 1-4/cover.jpg",
	)
	s, err := SplitByBook(dir, acotar)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Bundle || len(s.Books) != 3 {
		t.Fatalf("split = %+v", s)
	}
	if got := titlesOf(s, 1); got != "01|02" {
		t.Errorf("book 1 parts %q", got)
	}
	if got := titlesOf(s, 2); got != "Part 1" {
		t.Errorf("book 2 parts %q", got)
	}
	// CD1 before CD2
	if parts := s.Books[3]; len(parts) != 2 || !strings.Contains(parts[0].Path, "CD1") {
		t.Errorf("book 3 parts %+v", parts)
	}
	if len(s.Unmatched) != 1 || s.Unmatched[0] != "04 - A Court of Frost and Starlight" {
		t.Errorf("unmatched %q", s.Unmatched)
	}
}

func TestSplitSeriesFiles(t *testing.T) {
	// one M4B per book, loose in the folder
	dir := tree(t,
		"Sarah J. Maas - A Court of Thorns and Roses.m4b",
		"Sarah J. Maas - A Court of Mist and Fury.m4b",
		"Sarah J. Maas - A Court of Frost and Starlight.m4b",
	)
	s, err := SplitByBook(dir, acotar)
	if err != nil {
		t.Fatal(err)
	}
	var got []int64
	for id := range s.Books {
		got = append(got, id)
	}
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	if !s.Bundle || len(got) != 2 || got[0] != 1 || got[1] != 2 || len(s.Unmatched) != 1 {
		t.Fatalf("split = %+v", s)
	}

	// the series name in a file's name doesn't make it book one
	dir = tree(t,
		"A Court of Thorns and Roses 2 - A Court of Mist and Fury.m4b",
		"A Court of Thorns and Roses 3 - A Court of Wings and Ruin.m4b",
	)
	s, err = SplitByBook(dir, acotar)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Bundle || len(s.Books[1]) != 0 || len(s.Books[2]) != 1 || len(s.Books[3]) != 1 {
		t.Fatalf("series-prefixed names: %+v", s)
	}
}

// A pack whose files name their part before the series in brackets, as
// one on nzbgeek does: every book's parts are found, joined in order, and
// the series in brackets doesn't make a file book one.
func TestSplitSeriesFilesInParts(t *testing.T) {
	dir := tree(t,
		"[ACOTAR #4] A Court of Silver Flames [GraphicAudio] (chapterized)/01.m4b",
		"A Court of Frost and Starlight_ Part 1 (A Court of Thorns and Roses #4) (Unabridged).m4b",
		"A Court of Mist and Fury_ Part 1 (A Court of Thorns and Roses #2) (Unabridged).m4b",
		"A Court of Mist and Fury_ Part 2 (A Court of Thorns and Roses #2) (Unabridged).m4b",
		"A Court of Thorns and Roses_ Part 1 (A Court of Thorns and Roses #1) (Unabridged).m4b",
		"A Court of Thorns and Roses_ Part 2 (A Court of Thorns and Roses #1) (Unabridged).m4b",
		"A Court of Wings and Ruin_ Part 1 (A Court of Thorns and Roses #3) (Unabridged).m4b",
		"A Court of Wings and Ruin_ Part 2 (A Court of Thorns and Roses #3) (Unabridged).m4b",
		"A Court of Wings and Ruin_ Part 3 (A Court of Thorns and Roses #3).m4b",
	)
	s, err := SplitByBook(dir, acotar)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Bundle {
		t.Fatal("not seen as a pack")
	}
	for id, want := range map[int64]int{1: 2, 2: 2, 3: 3} {
		if got := len(s.Books[id]); got != want {
			t.Errorf("book %d: %d parts, want %d (%+v)", id, got, want, s)
		}
	}
	for id := int64(1); id <= 3; id++ {
		parts := s.Books[id]
		for i := 1; i < len(parts); i++ {
			if parts[i-1].Path > parts[i].Path {
				t.Errorf("book %d parts out of order: %v", id, parts)
			}
		}
	}
	if len(s.Unmatched) != 2 { // Frost and Starlight, Silver Flames: not in the test library
		t.Errorf("unmatched %q", s.Unmatched)
	}
}

// The series' name first, when the series is named for its first book:
// each file still goes to its own book, and book one's own files to it.
func TestSplitSeriesNameFirst(t *testing.T) {
	for name, files := range map[string][]string{
		"dash": {
			"A Court of Thorns and Roses - A Court of Thorns and Roses.m4b",
			"A Court of Thorns and Roses - A Court of Mist and Fury.m4b",
			"A Court of Thorns and Roses - A Court of Wings and Ruin.m4b",
		},
		"numbered, in parts": {
			"A Court of Thorns and Roses 1 - A Court of Thorns and Roses_ Part 1.m4b",
			"A Court of Thorns and Roses 1 - A Court of Thorns and Roses_ Part 2.m4b",
			"A Court of Thorns and Roses 2 - A Court of Mist and Fury_ Part 1.m4b",
			"A Court of Thorns and Roses 2 - A Court of Mist and Fury_ Part 2.m4b",
			"A Court of Thorns and Roses 3 - A Court of Wings and Ruin_ Part 1.m4b",
		},
		"series, part, title": {
			"A Court of Thorns and Roses - Book 1 - A Court of Thorns and Roses.m4b",
			"A Court of Thorns and Roses - Book 2 - A Court of Mist and Fury.m4b",
			"A Court of Thorns and Roses - Book 3 - A Court of Wings and Ruin.m4b",
		},
	} {
		s, err := SplitByBook(tree(t, files...), acotar)
		if err != nil {
			t.Fatal(err)
		}
		if !s.Bundle || len(s.Unmatched) != 0 {
			t.Fatalf("%s: %+v", name, s)
		}
		for id := int64(1); id <= 3; id++ {
			parts := s.Books[id]
			if len(parts) == 0 {
				t.Errorf("%s: book %d has no parts: %+v", name, id, s)
			}
			for _, p := range parts {
				if id > 1 && !strings.Contains(p.Path, acotar[id-1].Title) {
					t.Errorf("%s: book %d got %s", name, id, p.Path)
				}
				if id == 1 && (strings.Contains(p.Path, "Mist") || strings.Contains(p.Path, "Wings")) {
					t.Errorf("%s: book one got %s", name, p.Path)
				}
			}
		}
	}
}
