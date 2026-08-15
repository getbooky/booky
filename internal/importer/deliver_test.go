package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/epub"
	"github.com/getbooky/booky/internal/metadata"
)

func TestRenderName(t *testing.T) {
	im, lib, _ := testImporter(t)
	_ = im
	_ = lib
	b := bookFixture()
	cases := map[string]string{
		"{Author}/{Title}":                      "Mara Voss/First Ember",
		"{Author}/{Series}/{SeriesNum} {Title}": "Mara Voss/The Ember Cycle/1 First Ember",
		"{Title} ({Year})":                      "First Ember (2023)",
		"":                                      "Mara Voss/First Ember",
	}
	for scheme, want := range cases {
		if got := RenderName(scheme, b); got != filepath.FromSlash(want) {
			t.Errorf("RenderName(%q) = %q, want %q", scheme, got, want)
		}
	}
}

func TestSanitizeStripsPathHazards(t *testing.T) {
	b := bookFixture()
	b.Title = `Vault: Burrow / Drift?`
	got := RenderName("{Title}", b)
	if got != "Vault - Burrow - Drift" {
		t.Errorf("sanitized = %q", got)
	}
}

// A metadata field of ".." or "." must never let an imported file escape the
// library root, whatever a provider or manual edit supplies.
func TestSanitizeNeutralizesTraversal(t *testing.T) {
	for _, evil := range []string{"..", ".", "...", " .. "} {
		b := bookFixture()
		b.Author = evil
		b.Title = "Book"
		got := RenderName("{Author}/{Title}", b)
		if got != filepath.Join("_", "Book") {
			t.Errorf("author %q produced %q, expected the component neutralized", evil, got)
		}
	}
}

func TestDeliverMovesWritesAndHardlinks(t *testing.T) {
	im, alexID, _ := testImporter(t)
	// a second library sharing the same filesystem, wanting the same book
	samRoot := t.TempDir()
	profile, _ := im.Catalog.EnsureDefaultProfile()
	samID, err := im.Catalog.CreateLibrary("Sam", samRoot, profile, "sam", "x")
	if err != nil {
		t.Fatal(err)
	}

	bookID, err := im.Catalog.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "First Ember", Authors: []string{"Mara Voss"},
		SeriesName: "The Ember Cycle", SeriesIndex: 1, ISBN13: "9781649374042", GoodreadsID: "61431922",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, lib := range []int64{alexID, samID} {
		if err := im.Catalog.AddToLibrary(bookID, lib, true); err != nil {
			t.Fatal(err)
		}
	}

	downloads := t.TempDir()
	src := writeEpub(t, downloads, "first_ember_download.epub", `<dc:title>ugly release title</dc:title>`)

	ns := NamingSettings{
		Scheme:        "{Author}/{Title}",
		WriteOnImport: true,
		WriteFields:   epub.Fields{"title": true, "author": true, "series": true, "identifiers": true},
	}
	dst, err := im.Deliver(bookID, alexID, src, ns)
	if err != nil {
		t.Fatalf("Deliver: %v", err)
	}

	// named by scheme, source gone
	if filepath.Base(filepath.Dir(dst)) != "Mara Voss" || filepath.Base(dst) != "First Ember.epub" {
		t.Errorf("dst = %s", dst)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("download source should be moved away")
	}

	// metadata written into the file, identity triple included
	got, err := epub.Read(dst)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "First Ember" || got.ISBN != "9781649374042" || got.GoodreadsID != "61431922" {
		t.Errorf("written meta = %+v", got)
	}
	if got.SeriesName != "The Ember Cycle" {
		t.Errorf("series = %q", got.SeriesName)
	}

	// hard-linked into Sam's library, same inode
	books, _ := im.Catalog.ListBooks(0, samID, 0)
	if len(books) != 1 || books[0].FilePath == "" {
		t.Fatalf("sam's copy missing: %+v", books)
	}
	a, _ := os.Stat(dst)
	b, _ := os.Stat(books[0].FilePath)
	if !os.SameFile(a, b) {
		t.Error("expected a hardlink (same inode) across libraries")
	}
}

func bookFixture() *catalog.Book {
	return &catalog.Book{
		Author: "Mara Voss", Title: "First Ember",
		SeriesName: "The Ember Cycle", SeriesNum: 1, ReleaseDate: "2023-05-02",
	}
}
