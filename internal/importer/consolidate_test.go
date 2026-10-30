package importer

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getbooky/booky/internal/catalog"
	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/metadata"
)

// Merging libraries never loses a byte: the kept copy comes home into the
// library root, a hardlink or identical twin is removed, a different copy is
// set aside, and only folders left truly empty are pruned.
func TestConsolidateNeverLosesContent(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	cat := catalog.New(conn)
	im := New(conn, cat, nil)

	media := t.TempDir()
	root := filepath.Join(media, "family")
	kids := filepath.Join(media, "kids")
	profile, _ := cat.EnsureDefaultProfile()
	lib, err := cat.CreateLibrary("Library", root, profile, "x", "unset")
	if err != nil {
		t.Fatal(err)
	}
	write := func(p, body string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	book := func(title string) int64 {
		t.Helper()
		id, err := cat.UpsertBook(metadata.BookMeta{Provider: "t", Title: title, Authors: []string{"Emmett Hale"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := cat.AddToLibrary(id, lib, true); err != nil {
			t.Fatal(err)
		}
		return id
	}

	// 1. kept file only in the old Kids folder → moves home
	away := book("Away")
	awayPath := write(filepath.Join(kids, "Emmett Hale", "Away.epub"), "away")
	if err := cat.SetFile(lib, away, awayPath, "epub", 4); err != nil {
		t.Fatal(err)
	}
	// 2. hard-linked duplicate → removed
	linked := book("Linked")
	linkedPath := write(filepath.Join(root, "Emmett Hale", "Linked.epub"), "linked")
	if err := cat.SetFile(lib, linked, linkedPath, "epub", 6); err != nil {
		t.Fatal(err)
	}
	linkCopy := filepath.Join(kids, "Emmett Hale", "Linked.epub")
	if err := os.Link(linkedPath, linkCopy); err != nil {
		t.Skipf("hardlinks unavailable: %v", err)
	}
	// 3. a different copy → set aside, not deleted
	diff := book("Different")
	diffPath := write(filepath.Join(root, "Emmett Hale", "Different.epub"), "kept edition")
	if err := cat.SetFile(lib, diff, diffPath, "epub", 12); err != nil {
		t.Fatal(err)
	}
	otherEdition := write(filepath.Join(kids, "Other", "Different.epub"), "another edition")
	// an unrelated file in the old folder keeps that folder alive
	stray := write(filepath.Join(kids, "notes.txt"), "mine")

	for _, d := range []struct {
		book int64
		path string
	}{{linked, linkCopy}, {diff, otherEdition}} {
		if _, err := conn.Exec(`INSERT INTO library_merge (book_id, file_path) VALUES (?, ?)`, d.book, d.path); err != nil {
			t.Fatal(err)
		}
	}

	rep, err := im.Consolidate("{Author}/{Title}")
	if err != nil {
		t.Fatal(err)
	}
	if rep.Moved != 1 || rep.Removed != 1 || rep.SetAside != 1 || rep.Failed != 0 {
		t.Fatalf("report = %+v", rep)
	}

	b, _ := cat.GetBook(away)
	if want := filepath.Join(root, "Emmett Hale", "Away.epub"); b.FilePath != want {
		t.Errorf("kept file at %q, want %q", b.FilePath, want)
	}
	if got, _ := os.ReadFile(b.FilePath); string(got) != "away" {
		t.Error("moved file lost its content")
	}
	if _, err := os.Stat(linkCopy); !os.IsNotExist(err) {
		t.Error("a hard-linked duplicate should be removed")
	}
	if got, _ := os.ReadFile(linkedPath); string(got) != "linked" {
		t.Error("removing a hardlink damaged the kept file")
	}
	aside := filepath.Join(root, DuplicatesDir, "Emmett Hale", "Different.epub")
	if got, err := os.ReadFile(aside); err != nil || string(got) != "another edition" {
		t.Errorf("the differing copy must be set aside intact: %q %v", got, err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("a file that isn't a book copy was touched")
	}
	if _, err := os.Stat(filepath.Join(kids, "Emmett Hale")); !os.IsNotExist(err) {
		t.Error("a folder left empty should be pruned")
	}
	if _, err := os.Stat(kids); err != nil {
		t.Error("a folder that still holds something must never be pruned")
	}

	// the scanner never offers the set-aside copies for import
	res, err := im.Scan(t.Context(), lib, root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Scanned != 3 {
		t.Errorf("scan saw %d files, want the 3 shelved ones (duplicates folder skipped)", res.Scanned)
	}

	// a second run finds nothing to do
	rep, err = im.Consolidate("{Author}/{Title}")
	if err != nil || rep.Moved+rep.Removed+rep.SetAside+rep.Failed != 0 {
		t.Fatalf("second run = %+v %v", rep, err)
	}
}
