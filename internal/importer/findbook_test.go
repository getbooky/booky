package importer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindBookFileForPicksTheBookFromAPack(t *testing.T) {
	mk := func(files map[string]int) string {
		dir := t.TempDir()
		for name, size := range files {
			p := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, make([]byte, size), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	const maas = "Sarah J. Maas"

	// a series pack: the book asked for, not the biggest
	pack := mk(map[string]int{
		"ACOTAR/01 - A Court of Thorns and Roses.epub": 500,
		"ACOTAR/02 - A Court of Mist and Fury.epub":    900,
		"ACOTAR/04 - A Court of Silver Flames.epub":    1200,
	})
	got, err := FindBookFileFor(pack, "A Court of Mist and Fury", maas)
	if err != nil || !strings.HasSuffix(got, "02 - A Court of Mist and Fury.epub") {
		t.Fatalf("got %q, %v", got, err)
	}
	// a pack without the book is refused, not guessed at
	if got, err := FindBookFileFor(pack, "A Court of Frost and Starlight", maas); err == nil {
		t.Fatalf("picked %q from a pack without the book", got)
	}

	// one book, oddly named, in two formats: the biggest file, as before
	one := mk(map[string]int{"acotar_retail.epub": 800, "acotar_retail.pdf": 3000})
	if got, err := FindBookFileFor(one, "A Court of Thorns and Roses", maas); err != nil || !strings.HasSuffix(got, ".pdf") {
		t.Fatalf("one book: %q %v", got, err)
	}
	// a lone file, whatever its name
	lone := mk(map[string]int{"x.epub": 10})
	if got, err := FindBookFileFor(lone, "Anything", "Anyone"); err != nil || !strings.HasSuffix(got, "x.epub") {
		t.Fatalf("lone: %q %v", got, err)
	}
	// nothing at all
	if _, err := FindBookFileFor(mk(map[string]int{"cover.jpg": 10}), "A", "B"); err == nil {
		t.Fatal("no book file found nothing")
	}
}
