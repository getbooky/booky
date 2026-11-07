package audiobook

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/getbooky/booky/internal/readalong"
)

var mp3Head = []byte("ID3\x04\x00\x00\x00\x00\x00\x00 not really audio")

func TestNaturalLess(t *testing.T) {
	names := []string{"Part 10.mp3", "part 2.mp3", "Part 1.mp3", "CD2/01.mp3", "CD1/10.mp3", "CD1/9.mp3", "Part 002b.mp3"}
	sort.Slice(names, func(i, j int) bool { return naturalLess(names[i], names[j]) })
	want := []string{"CD1/9.mp3", "CD1/10.mp3", "CD2/01.mp3", "Part 1.mp3", "part 2.mp3", "Part 002b.mp3", "Part 10.mp3"}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", names)
	}
}

func TestParts(t *testing.T) {
	dir := t.TempDir()
	write := func(rel string, body []byte) string {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, body, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("CD1/10 - Ten.mp3", mp3Head)
	write("CD1/9 - Nine.mp3", mp3Head)
	write("CD2/01 - One.mp3", mp3Head)
	write("cover.jpg", []byte("\xff\xd8\xff"))
	write(".hidden/x.mp3", mp3Head)
	outside := filepath.Join(t.TempDir(), "secret.mp3")
	if err := os.WriteFile(outside, mp3Head, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "CD2", "02 - link.mp3")); err != nil {
		t.Skip("symlinks unavailable")
	}

	parts, err := Parts(dir)
	if err != nil {
		t.Fatal(err)
	}
	var titles []string
	for _, p := range parts {
		titles = append(titles, p.Title)
		if p.Format != readalong.MP3 {
			t.Errorf("%s format %q", p.Path, p.Format)
		}
	}
	if strings.Join(titles, "|") != "9 - Nine|10 - Ten|01 - One" {
		t.Fatalf("parts %q — symlinks, hidden folders and non-audio must be skipped", titles)
	}

	// a file named like audio that isn't, is refused rather than skipped
	write("CD2/03 - fake.mp3", []byte("#EXTM3U\nfile:///config/booky.db\n"))
	if _, err := Parts(dir); err == nil {
		t.Fatal("a disguised file must fail the import")
	}

	// a single file is its own list
	one, err := Parts(filepath.Join(dir, "CD1", "9 - Nine.mp3"))
	if err != nil || len(one) != 1 {
		t.Fatalf("single file: %v %v", one, err)
	}
	if _, err := Parts(filepath.Join(t.TempDir())); err == nil {
		t.Fatal("an empty folder must fail")
	}
}

func TestDestination(t *testing.T) {
	ebook := "/data/books/Lewis Carroll/Alice in Wonderland.epub"
	if got := Destination(ebook, readalong.MP4, "x.m4b"); got != "/data/books/Lewis Carroll/Alice in Wonderland.m4b" {
		t.Fatal(got)
	}
	if got := Destination(ebook, readalong.MP3, "x.MP3"); got != "/data/books/Lewis Carroll/Alice in Wonderland.mp3" {
		t.Fatal(got)
	}
}
