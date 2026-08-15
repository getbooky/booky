package catalog

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/metadata"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return New(conn)
}

var fourthWing = metadata.BookMeta{
	Provider: "goodreads", Title: "First Ember", Authors: []string{"Mara Voss"},
	Description: "Dragons.", SeriesName: "The Ember Cycle", SeriesIndex: 1,
	GoodreadsID: "61431922", ISBN13: "9781649374042", Language: "English",
	Publisher: "Ember Tower Books", ReleaseDate: "2023-05-02",
}

func TestUpsertCreatesAuthorSeriesBook(t *testing.T) {
	s := testStore(t)
	id, err := s.UpsertBook(fourthWing)
	if err != nil {
		t.Fatalf("UpsertBook: %v", err)
	}
	b, err := s.GetBook(id)
	if err != nil {
		t.Fatalf("GetBook: %v", err)
	}
	if b.Author != "Mara Voss" || b.SeriesName != "The Ember Cycle" || b.SeriesNum != 1 {
		t.Errorf("book = %+v", b)
	}

	// a bare catalog author is hidden from the Authors page until they're
	// shelved or asked for by name — that's what keeps co-author fallout
	// invisible. Being *tracked* no longer counts: that flag is set
	// automatically now, so keying the page off it kept every author who had
	// ever held a book listed forever.
	authors, _ := s.ListAuthors(nil)
	if len(authors) != 0 {
		t.Errorf("catalog-only author should be hidden: %+v", authors)
	}
	if err := s.MarkAuthorAdded(b.AuthorID); err != nil {
		t.Fatal(err)
	}
	authors, _ = s.ListAuthors(nil)
	if len(authors) != 1 || authors[0].SortName != "Voss, Mara" {
		t.Errorf("authors = %+v", authors)
	}
}

func TestUpsertIsIdempotentAcrossIdentities(t *testing.T) {
	s := testStore(t)
	id1, err := s.UpsertBook(fourthWing)
	if err != nil {
		t.Fatal(err)
	}
	// same book arriving from hardcover: no goodreads id, but same isbn
	id2, err := s.UpsertBook(metadata.BookMeta{
		Provider: "hardcover", Title: "First Ember", Authors: []string{"Mara Voss"},
		ISBN13: "9781649374042", HardcoverID: "433567",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("same isbn should match same book: %d vs %d", id1, id2)
	}
	b, _ := s.GetBook(id1)
	if b.HardcoverID != "433567" {
		t.Error("hardcover id should fill in")
	}
	if b.GoodreadsID != "61431922" {
		t.Error("existing goodreads id must not be lost")
	}

	// same title+author with no identifiers at all
	id3, err := s.UpsertBook(metadata.BookMeta{Provider: "openlibrary", Title: "first ember", Authors: []string{"Mara Voss"}})
	if err != nil {
		t.Fatal(err)
	}
	if id3 != id1 {
		t.Fatalf("case-insensitive title+author should match: %d vs %d", id3, id1)
	}
}

// A later sync can canonicalize a different edition-variant title while
// sharing none of the stored identifiers — it must converge on the existing
// row instead of minting a duplicate sibling.
func TestUpsertConvergesEditionVariantTitles(t *testing.T) {
	s := testStore(t)
	id1, err := s.UpsertBook(metadata.BookMeta{
		Provider: "goodreads", Title: "Cold Signal: A Thriller",
		Authors: []string{"Dane Mercer"}, GoodreadsID: "58936907",
	})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.UpsertBook(metadata.BookMeta{
		Provider: "hardcover", Title: "Cold Signal",
		Authors: []string{"Dane Mercer"}, HardcoverID: "433567",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("edition variant should match the existing row: %d vs %d", id1, id2)
	}
	b, _ := s.GetBook(id1)
	if b.Title != "Cold Signal" {
		t.Errorf("title should follow the fresh canonical variant: %q", b.Title)
	}
	if b.GoodreadsID != "58936907" || b.HardcoverID != "433567" {
		t.Errorf("identities should merge, got goodreads=%q hardcover=%q", b.GoodreadsID, b.HardcoverID)
	}

	// distinct numbered volumes must NOT converge
	v1, _ := s.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Riftline, Volume 1", Authors: []string{"Nolan T. Ashford"}})
	v2, _ := s.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Riftline, Volume 2", Authors: []string{"Nolan T. Ashford"}})
	if v1 == v2 {
		t.Fatal("distinct volumes merged into one row")
	}
}

// A leftover author row (books removed one by one, an old mid-sync
// resurrection) is invisible on the Authors page, so search must offer "Add
// author" again instead of a dead "Added" chip.
func TestAuthorStateMatchesAuthorsPageVisibility(t *testing.T) {
	s := testStore(t)
	bookID, err := s.UpsertBook(fourthWing)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetBook(bookID)

	// bare catalog row: hidden from Authors page → not "added" in search
	if exists, _ := s.AuthorState("Mara Voss"); exists {
		t.Error("leftover author row must not show as added")
	}

	if err := s.SetAuthorMonitored(b.AuthorID, true); err != nil {
		t.Fatal(err)
	}
	if exists, monitored := s.AuthorState("mara voss"); !exists || !monitored {
		t.Error("monitored author should show as added (case-insensitive)")
	}
	if err := s.SetAuthorMonitored(b.AuthorID, false); err != nil {
		t.Fatal(err)
	}

	// unmonitored but with a book in a library: visible → added
	profile, _ := s.EnsureDefaultProfile()
	lib, err := s.CreateLibrary("Main", "/data/books", profile, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToLibrary(bookID, lib, false); err != nil {
		t.Fatal(err)
	}
	if exists, _ := s.AuthorState("Mara Voss"); !exists {
		t.Error("author with a library book should show as added")
	}
}

func TestUpsertRespectsFieldLocks(t *testing.T) {
	s := testStore(t)
	id, _ := s.UpsertBook(fourthWing)
	if _, err := s.db.Exec(`UPDATE books SET description = 'my edit', field_locks = '{"description":true}' WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpsertBook(fourthWing); err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetBook(id)
	if b.Description != "my edit" {
		t.Errorf("locked description overwritten: %q", b.Description)
	}
}

// An empty incoming value must never blank an already-populated field, and an
// existing identity must never be overwritten by a different one — the two
// invariants the static partial-update relies on.
func TestUpsertNeverBlanksOrOverwrites(t *testing.T) {
	s := testStore(t)
	id, _ := s.UpsertBook(fourthWing) // has description, goodreads id, isbn

	// a sparse refresh: same book, but nothing but the title
	if _, err := s.UpsertBook(metadata.BookMeta{
		Provider: "thin", Title: "First Ember", Authors: []string{"Mara Voss"},
		ISBN13: "9781649374042", GoodreadsID: "99999999", // a DIFFERENT goodreads id
	}); err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetBook(id)
	if b.Description != fourthWing.Description {
		t.Errorf("empty description blanked the existing one: %q", b.Description)
	}
	if b.GoodreadsID != "61431922" {
		t.Errorf("existing goodreads id was overwritten: %q", b.GoodreadsID)
	}
}

func TestEditBookTouchesOnlySuppliedFields(t *testing.T) {
	s := testStore(t)
	id, _ := s.UpsertBook(fourthWing)

	if err := s.EditBook(id, map[string]string{"description": "hand-written blurb"}, true); err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetBook(id)
	if b.Description != "hand-written blurb" {
		t.Errorf("edit not applied: %q", b.Description)
	}
	if b.Title != "First Ember" {
		t.Errorf("editing description clobbered the title: %q", b.Title)
	}
	if b.Language != fourthWing.Language {
		t.Errorf("editing description clobbered the language: %q", b.Language)
	}
}

func TestLibraryMembershipAndCounts(t *testing.T) {
	s := testStore(t)
	profile, err := s.EnsureDefaultProfile()
	if err != nil {
		t.Fatal(err)
	}
	lib, err := s.CreateLibrary("Alex", "/data/books/alex", profile, "alex-shelf", "hash")
	if err != nil {
		t.Fatal(err)
	}
	bookID, _ := s.UpsertBook(fourthWing)
	if err := s.AddToLibrary(bookID, lib, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFile(lib, bookID, "/data/books/alex/Mara Voss/First Ember.epub", "epub", 1_800_000); err != nil {
		t.Fatal(err)
	}

	libs, _ := s.ListLibraries()
	if len(libs) != 1 || libs[0].BookCount != 1 || libs[0].OnShelf != 1 {
		t.Errorf("libraries = %+v", libs)
	}

	books, _ := s.ListBooks(0, lib, 0)
	if len(books) != 1 || !books[0].Monitored || books[0].FileFormat != "epub" {
		t.Errorf("books = %+v", books)
	}

	series, _ := s.ListSeries(nil)
	if len(series) != 1 || series[0].Total != 1 || series[0].OnShelf != 1 {
		t.Errorf("series = %+v", series)
	}
}

func TestCompilationSeriesNameRejected(t *testing.T) {
	s := testStore(t)
	id, err := s.UpsertBook(metadata.BookMeta{
		Provider: "goodreads", Title: "Burrow", Authors: []string{"Emmett Hale"},
		SeriesName: "Vault Boxed Set (Books 1-3)",
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := s.GetBook(id)
	if b.SeriesName != "" {
		t.Errorf("compilation series should be dropped, got %q", b.SeriesName)
	}
}

// TestSeriesVisibility: a series existing only through catalog-only books
// stays off the Series page until one of its books is shelved (or the
// series is monitored).
func TestSeriesVisibility(t *testing.T) {
	s := testStore(t)
	id, err := s.UpsertBook(fourthWing) // has series "The Ember Cycle", no library
	if err != nil {
		t.Fatal(err)
	}
	series, _ := s.ListSeries(nil)
	if len(series) != 0 {
		t.Fatalf("catalog-only series should be hidden: %+v", series)
	}

	profileID, _ := s.EnsureDefaultProfile()
	libID, err := s.CreateLibrary("Alex", t.TempDir(), profileID, "alex", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToLibrary(id, libID, true); err != nil {
		t.Fatal(err)
	}
	series, _ = s.ListSeries(nil)
	if len(series) != 1 || series[0].Name != "The Ember Cycle" {
		t.Fatalf("shelved book should surface its series: %+v", series)
	}
}

// UpsertBookForAuthor must never recreate an author that was deleted while a
// bibliography sync was still running — the "zombie author" that kept showing
// as added after deletion.
func TestUpsertBookForAuthorDoesNotResurrect(t *testing.T) {
	s := testStore(t)
	authorID, err := s.EnsureAuthor("Cole Merrick")
	if err != nil {
		t.Fatal(err)
	}
	// first book upserts fine while the author exists
	if _, err := s.UpsertBookForAuthor(metadata.BookMeta{
		Provider: "hardcover", Title: "Iron Dawn", Authors: []string{"Cole Merrick"},
	}, authorID); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// user deletes the author mid-sync
	if err := s.DeleteAuthor(authorID); err != nil {
		t.Fatal(err)
	}
	// the still-running sync tries the next book — it must be refused, not
	// resurrect the author
	_, err = s.UpsertBookForAuthor(metadata.BookMeta{
		Provider: "hardcover", Title: "Iron Crown", Authors: []string{"Cole Merrick"},
	}, authorID)
	if !errors.Is(err, ErrAuthorGone) {
		t.Fatalf("want ErrAuthorGone, got %v", err)
	}
	if exists, _ := s.AuthorState("Cole Merrick"); exists {
		t.Fatal("BUG: author resurrected by mid-sync upsert after delete")
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM authors`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("BUG: %d author rows remain after delete", n)
	}
}

// The normal by-name UpsertBook still creates authors as before.
func TestUpsertBookStillCreatesAuthor(t *testing.T) {
	s := testStore(t)
	if _, err := s.UpsertBook(metadata.BookMeta{
		Provider: "hardcover", Title: "Harbor Wakes", Authors: []string{"R. K. Marsh"},
	}); err != nil {
		t.Fatal(err)
	}
	// AuthorState hides bare catalog rows by design, so check the row itself
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM authors WHERE name = 'R. K. Marsh'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("UpsertBook should create the author row: n=%d err=%v", n, err)
	}
}

// The FK backstop (not the pre-check) must also map to ErrAuthorGone: pass a
// dangling author id straight to the shared body, simulating a delete landing
// between the pre-check and the insert.
func TestUpsertBookWithAuthorFKBackstop(t *testing.T) {
	s := testStore(t)
	const danglingID = 999999
	_, err := s.upsertBookWithAuthor(metadata.BookMeta{
		Provider: "hardcover", Title: "Ghost Book", Authors: []string{"Nobody"},
	}, danglingID)
	if !isForeignKeyErr(err) {
		t.Fatalf("want a foreign-key error from a dangling author id, got %v", err)
	}
	// and the public wrapper converts that to the sentinel
	if _, err := s.UpsertBookForAuthor(metadata.BookMeta{
		Provider: "hardcover", Title: "Ghost Book", Authors: []string{"Nobody"},
	}, danglingID); !errors.Is(err, ErrAuthorGone) {
		t.Fatalf("wrapper should return ErrAuthorGone, got %v", err)
	}
}

// Deleting an author with mode=files removes the books' files and prunes the
// emptied directories — including the author folder — while never touching a
// directory that still holds anything else.
func TestDeleteAuthorFilesPrunesEmptyDirs(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	profile, err := s.EnsureDefaultProfile()
	if err != nil {
		t.Fatal(err)
	}
	libID, err := s.CreateLibrary("Main", root, profile, "main", "x")
	if err != nil {
		t.Fatal(err)
	}
	add := func(title, rel string) int64 {
		id, err := s.UpsertBook(metadata.BookMeta{Provider: "t", Title: title, Authors: []string{"Emmett Hale"}})
		if err != nil {
			t.Fatal(err)
		}
		if err := s.AddToLibrary(id, libID, true); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("book"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := s.SetFile(libID, id, path, "epub", 4); err != nil {
			t.Fatal(err)
		}
		return id
	}
	add("Burrow", "Emmett Hale/Burrow.epub")
	add("Drift", "Emmett Hale/Drift.epub")
	// an unrelated file shares no directory with the author's books
	keeper := filepath.Join(root, "Other Author", "Keeper.epub")
	if err := os.MkdirAll(filepath.Dir(keeper), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keeper, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.DeleteAuthorFiles(1)
	if err != nil {
		t.Fatalf("DeleteAuthorFiles: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	if _, err := os.Stat(filepath.Join(root, "Emmett Hale")); !os.IsNotExist(err) {
		t.Error("author folder should be pruned once empty")
	}
	if _, err := os.Stat(keeper); err != nil {
		t.Error("unrelated files must survive")
	}
	if _, err := os.Stat(root); err != nil {
		t.Error("the library root itself must never be removed")
	}
	if err := s.DeleteAuthor(1); err != nil {
		t.Fatalf("DeleteAuthor: %v", err)
	}
}

// A folder still holding something else (a different format imported in
// place) survives the prune.
func TestDeleteAuthorFilesKeepsNonEmptyDirs(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	profile, _ := s.EnsureDefaultProfile()
	libID, err := s.CreateLibrary("Main", root, profile, "main", "x")
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.UpsertBook(metadata.BookMeta{Provider: "t", Title: "Burrow", Authors: []string{"Emmett Hale"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToLibrary(id, libID, true); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "Emmett Hale")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	book := filepath.Join(dir, "Burrow.epub")
	if err := os.WriteFile(book, []byte("book"), 0o600); err != nil {
		t.Fatal(err)
	}
	stray := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(stray, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SetFile(libID, id, book, "epub", 4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteAuthorFiles(1); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("stray file must survive")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Error("non-empty author folder must survive")
	}
}

// Monitoring a series must monitor ALL its books — catalog-only rows (the
// bulk of a synced series) get attached to the author's library, not skipped.
// Shelving a series is an explicit, single-library act: every book joins the
// named library monitored, and nothing outside that library is touched. It
// replaces a toggle that flipped a global flag and cascaded into every
// library holding any of those books.
func TestAddSeriesToLibraryShelvesOnlyThatLibrary(t *testing.T) {
	s := testStore(t)
	profile, err := s.EnsureDefaultProfile()
	if err != nil {
		t.Fatal(err)
	}
	mine, err := s.CreateLibrary("Mine", "/data/mine", profile, "mine", "unset")
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := s.CreateLibrary("Theirs", "/data/theirs", profile, "theirs", "unset")
	if err != nil {
		t.Fatal(err)
	}

	first, err := s.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "First Ember", Authors: []string{"Mara Voss"},
		SeriesName: "The Ember Cycle", SeriesIndex: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, title := range []string{"Ashen Flame", "Onyx Gale"} {
		if _, err := s.UpsertBook(metadata.BookMeta{
			Provider: "test", Title: title, Authors: []string{"Mara Voss"},
			SeriesName: "The Ember Cycle", SeriesIndex: 2,
		}); err != nil {
			t.Fatal(err)
		}
	}
	b, err := s.GetBook(first)
	if err != nil {
		t.Fatal(err)
	}
	if b.SeriesID == nil {
		t.Fatal("seed book has no series")
	}
	// the other library already holds one of them, deliberately unmonitored
	if err := s.AddToLibrary(first, theirs, false); err != nil {
		t.Fatal(err)
	}

	gained, err := s.AddSeriesToLibrary(*b.SeriesID, mine)
	if err != nil {
		t.Fatal(err)
	}
	if gained != 3 {
		t.Fatalf("AddSeriesToLibrary = %d, want 3", gained)
	}
	books, err := s.ListBooks(0, mine, 0)
	if err != nil {
		t.Fatal(err)
	}
	monitored := 0
	for _, bk := range books {
		if bk.Monitored {
			monitored++
		}
	}
	if len(books) != 3 || monitored != 3 {
		t.Fatalf("shelved series: %d in library (%d monitored), want 3/3", len(books), monitored)
	}

	// the other library is untouched — same one book, still unmonitored
	other, err := s.ListBooks(0, theirs, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 1 || other[0].Monitored {
		t.Fatalf("the other library was modified: %+v", other)
	}

	// A book that arrives later joins the library that was GIVEN the series —
	// and only that one. The other library holds an entry it picked up
	// separately; it never asked for the set, so it doesn't get fed.
	if _, err := s.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "Ember Four", Authors: []string{"Mara Voss"},
		SeriesName: "The Ember Cycle", SeriesIndex: 4,
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.AttachMonitoredSeriesBooks(b.AuthorID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("AttachMonitoredSeriesBooks = %d, want 1 (only the library given the series)", n)
	}
	books, _ = s.ListBooks(0, mine, 0)
	if len(books) != 4 {
		t.Fatalf("late book did not join the shelved library: %d, want 4", len(books))
	}
	other, _ = s.ListBooks(0, theirs, 0)
	if len(other) != 1 {
		t.Fatalf("late book was pushed into a library that never asked for the series: %d, want 1", len(other))
	}

	// ...and clearing the series out of a library withdraws its standing
	// request, so the next announcement doesn't refill the shelf
	for _, bk := range books {
		if err := s.RemoveBookFromLibrary(bk.ID, mine, "library"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "Ember Five", Authors: []string{"Mara Voss"},
		SeriesName: "The Ember Cycle", SeriesIndex: 5,
	}); err != nil {
		t.Fatal(err)
	}
	if n, err := s.AttachMonitoredSeriesBooks(b.AuthorID); err != nil {
		t.Fatal(err)
	} else if n != 0 {
		t.Fatalf("a cleared library was refilled: %d membership(s) created", n)
	}
}

// Tommy has 2 of 5 books in a series and never asked for the set. Jolene adds
// one of the same books to her library, then shelves the whole series. Tommy's
// library must not change: he never pressed the button, and holding a couple
// of entries is not a standing request for the rest.
func TestShelvingASeriesLeavesOtherLibrariesAlone(t *testing.T) {
	s := testStore(t)
	profile, err := s.EnsureDefaultProfile()
	if err != nil {
		t.Fatal(err)
	}
	tommy, err := s.CreateLibrary("Tommy", "/data/tommy", profile, "tommy", "unset")
	if err != nil {
		t.Fatal(err)
	}
	jolene, err := s.CreateLibrary("Jolene", "/data/jolene", profile, "jolene", "unset")
	if err != nil {
		t.Fatal(err)
	}

	var ids []int64
	for i, title := range []string{"One", "Two", "Three", "Four", "Five"} {
		id, err := s.UpsertBook(metadata.BookMeta{
			Provider: "test", Title: title, Authors: []string{"Mara Voss"},
			SeriesName: "The Ember Cycle", SeriesIndex: float64(i + 1),
		})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	// Tommy picked up two of them one at a time — no "add series" anywhere
	for _, id := range ids[:2] {
		if err := s.AddToLibrary(id, tommy, true); err != nil {
			t.Fatal(err)
		}
	}
	// Jolene found one by searching, then asked for the whole set
	if err := s.AddToLibrary(ids[2], jolene, true); err != nil {
		t.Fatal(err)
	}
	b, err := s.GetBook(ids[0])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddSeriesToLibrary(*b.SeriesID, jolene); err != nil {
		t.Fatal(err)
	}

	jHas, _ := s.ListBooks(0, jolene, 0)
	if len(jHas) != 5 {
		t.Fatalf("Jolene asked for the series and should hold all 5, got %d", len(jHas))
	}
	tHas, _ := s.ListBooks(0, tommy, 0)
	if len(tHas) != 2 {
		t.Fatalf("Tommy never asked for the series but gained books: holds %d, want 2", len(tHas))
	}

	// ...and a sixth book announced later goes to Jolene only, because only
	// her library carries the standing request
	if _, err := s.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "Six", Authors: []string{"Mara Voss"},
		SeriesName: "The Ember Cycle", SeriesIndex: 6,
	}); err != nil {
		t.Fatal(err)
	}
	n, err := s.AttachMonitoredSeriesBooks(b.AuthorID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("a new entry created %d membership(s), want 1 (Jolene's library only)", n)
	}
	jHas, _ = s.ListBooks(0, jolene, 0)
	tHas, _ = s.ListBooks(0, tommy, 0)
	if len(jHas) != 6 || len(tHas) != 2 {
		t.Fatalf("new entry landed wrong: Jolene %d (want 6), Tommy %d (want 2)", len(jHas), len(tHas))
	}
}

// Taking the last of an author's or series' books out of every library should
// take them off the Authors and Series pages too. Tracking flags are set
// automatically now and never cleared, so keying visibility off them left
// anything that had EVER been shelved stuck on those pages forever.
func TestEmptiedAuthorsAndSeriesLeaveTheirPages(t *testing.T) {
	s := testStore(t)
	profile, err := s.EnsureDefaultProfile()
	if err != nil {
		t.Fatal(err)
	}
	lib, err := s.CreateLibrary("Alex", "/data/alex", profile, "alex", "unset")
	if err != nil {
		t.Fatal(err)
	}
	bookID, err := s.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "First Ember", Authors: []string{"Mara Voss"},
		SeriesName: "The Ember Cycle", SeriesIndex: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToLibrary(bookID, lib, true); err != nil {
		t.Fatal(err)
	}

	authors, _ := s.ListAuthors(nil)
	series, _ := s.ListSeries(nil)
	if len(authors) != 1 || len(series) != 1 {
		t.Fatalf("baseline: %d authors, %d series, want 1/1", len(authors), len(series))
	}

	if err := s.RemoveBookFromLibrary(bookID, lib, "library"); err != nil {
		t.Fatal(err)
	}

	authors, _ = s.ListAuthors(nil)
	if len(authors) != 0 {
		t.Errorf("author still listed with nothing in any library: %+v", authors)
	}
	series, _ = s.ListSeries(nil)
	if len(series) != 0 {
		t.Errorf("series still listed with nothing in any library: %+v", series)
	}

	// an author added by hand is different — they were asked for, and their
	// bibliography is the point, so they stay with nothing shelved
	id, err := s.EnsureAuthor("Nora Vale")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.MarkAuthorAdded(id); err != nil {
		t.Fatal(err)
	}
	authors, _ = s.ListAuthors(nil)
	if len(authors) != 1 || authors[0].Name != "Nora Vale" {
		t.Fatalf("a hand-added author must stay: %+v", authors)
	}
}
