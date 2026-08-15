package catalog

import (
	"os"
	"path/filepath"
	"testing"
)

// Cached images are keyed by row id and SQLite reuses freed ids, so every
// delete path must drop the file with the row and startup must sweep files
// orphaned before that behavior existed — otherwise a re-added book wears its
// predecessor's cover.

func testStoreWithCaches(t *testing.T) *Store {
	t.Helper()
	s := testStore(t)
	dir := t.TempDir()
	s.Covers = NewCoverCache(filepath.Join(dir, "covers"))
	s.AuthorPhotos = NewCoverCache(filepath.Join(dir, "covers", "authors"))
	return s
}

func addShelvedBook(t *testing.T, s *Store) (bookID, libID int64) {
	t.Helper()
	profileID, err := s.EnsureDefaultProfile()
	if err != nil {
		t.Fatal(err)
	}
	libID, err = s.CreateLibrary("Test", t.TempDir(), profileID, "test-shelf", "unset")
	if err != nil {
		t.Fatal(err)
	}
	bookID, err = s.UpsertBook(fourthWing)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddToLibrary(bookID, libID, false); err != nil {
		t.Fatal(err)
	}
	return bookID, libID
}

func TestRemoveDemotesToCatalogOnly(t *testing.T) {
	// removing a book (any mode) drops only the library membership: the book
	// stays on its author/series pages as an unmonitored catalog row — one
	// monitor toggle re-adds it — and keeps its cached cover
	s := testStoreWithCaches(t)
	bookID, libID := addShelvedBook(t, s)
	if err := s.Covers.SaveBytes(bookID, []byte("jpeg")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveBookFromLibrary(bookID, libID, "file"); err != nil {
		t.Fatalf("RemoveBookFromLibrary: %v", err)
	}
	inLib, err := s.ListBooks(0, libID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(inLib) != 0 {
		t.Errorf("removed book still in library view: %+v", inLib)
	}
	b, err := s.GetBook(bookID)
	if err != nil {
		t.Fatalf("book row pruned — it must survive for the author/series pages: %v", err)
	}
	if b.Monitored || b.LibraryID != 0 {
		t.Errorf("removed book still carries membership state: %+v", b)
	}
	byAuthor, err := s.ListBooks(b.AuthorID, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(byAuthor) != 1 {
		t.Errorf("removed book missing from author page: %d books", len(byAuthor))
	}
	if s.Covers.Path(bookID) == "" {
		t.Error("cover removed although the book row survives")
	}
}

func TestBlockedBookKeepsCover(t *testing.T) {
	// block mode keeps the book row (the blocklist entry needs it), so the
	// cover must stay too
	s := testStoreWithCaches(t)
	bookID, libID := addShelvedBook(t, s)
	if err := s.Covers.SaveBytes(bookID, []byte("jpeg")); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveBookFromLibrary(bookID, libID, "block"); err != nil {
		t.Fatalf("RemoveBookFromLibrary: %v", err)
	}
	if s.Covers.Path(bookID) == "" {
		t.Error("cover removed although the book row survives")
	}
}

func TestDeleteAuthorDropsCoversAndPhoto(t *testing.T) {
	s := testStoreWithCaches(t)
	bookID, err := s.UpsertBook(fourthWing)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.GetBook(bookID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Covers.SaveBytes(bookID, []byte("jpeg")); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorPhotos.SaveBytes(b.AuthorID, []byte("jpeg")); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteAuthor(b.AuthorID); err != nil {
		t.Fatalf("DeleteAuthor: %v", err)
	}
	if p := s.Covers.Path(bookID); p != "" {
		t.Errorf("book cover survived the author cascade: %s", p)
	}
	if p := s.AuthorPhotos.Path(b.AuthorID); p != "" {
		t.Errorf("author photo survived the author row: %s", p)
	}
}

func TestSweepOrphanedCovers(t *testing.T) {
	s := testStoreWithCaches(t)
	bookID, err := s.UpsertBook(fourthWing)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.GetBook(bookID)
	if err != nil {
		t.Fatal(err)
	}
	// live rows keep their files; ids with no row lose them
	if err := s.Covers.SaveBytes(bookID, []byte("live")); err != nil {
		t.Fatal(err)
	}
	if err := s.Covers.SaveBytes(bookID+100, []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorPhotos.SaveBytes(b.AuthorID, []byte("live")); err != nil {
		t.Fatal(err)
	}
	if err := s.AuthorPhotos.SaveBytes(b.AuthorID+100, []byte("orphan")); err != nil {
		t.Fatal(err)
	}
	// a non-cache file in the covers dir is none of the sweep's business
	stray := filepath.Join(s.Covers.Dir, "notes.txt")
	if err := os.WriteFile(stray, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := s.SweepOrphanedCovers()
	if err != nil {
		t.Fatalf("SweepOrphanedCovers: %v", err)
	}
	if n != 2 {
		t.Errorf("removed %d files, want 2", n)
	}
	if s.Covers.Path(bookID) == "" {
		t.Error("live cover swept")
	}
	if s.AuthorPhotos.Path(b.AuthorID) == "" {
		t.Error("live author photo swept")
	}
	if s.Covers.Path(bookID+100) != "" || s.AuthorPhotos.Path(b.AuthorID+100) != "" {
		t.Error("orphaned files survived the sweep")
	}
	if _, err := os.Stat(stray); err != nil {
		t.Error("stray non-cache file removed by sweep")
	}
}
