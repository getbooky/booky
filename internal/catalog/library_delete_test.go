package catalog

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getbooky/booky/internal/metadata"
)

// shelveBook adds a book to libID with a real file on disk under root.
func shelveBook(t *testing.T, s *Store, libID int64, root, title, rel string) int64 {
	t.Helper()
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

func newLibrary(t *testing.T, s *Store, name, root string) int64 {
	t.Helper()
	profile, err := s.EnsureDefaultProfile()
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateLibrary(name, root, profile, name+"-shelf", "unset")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Deleting a library must empty the Library view of its books whichever mode
// was chosen — the memberships cascade away — while the book rows survive as
// catalog-only entries on their author and series pages.
func TestDeleteLibraryDropsBooksFromLibraryView(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	libID := newLibrary(t, s, "Main", root)
	bookID := shelveBook(t, s, libID, root, "Burrow", "Emmett Hale/Burrow.epub")

	if err := s.DeleteLibrary(libID); err != nil {
		t.Fatalf("DeleteLibrary: %v", err)
	}
	libs, err := s.ListLibraries()
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 0 {
		t.Errorf("library survived the delete: %+v", libs)
	}
	inLib, err := s.ListBooks(0, libID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(inLib) != 0 {
		t.Errorf("deleted library still lists books: %+v", inLib)
	}
	b, err := s.GetBook(bookID)
	if err != nil {
		t.Fatalf("book row pruned — it must survive for the author page: %v", err)
	}
	if b.LibraryID != 0 || b.Monitored || b.FilePath != "" {
		t.Errorf("book kept membership state after its library was deleted: %+v", b)
	}
}

// mode=keep is the whole point of the two-option delete: Booky forgets the
// library, the files stay exactly where they are.
func TestDeleteLibraryKeepsFilesOnDisk(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	libID := newLibrary(t, s, "Main", root)
	shelveBook(t, s, libID, root, "Burrow", "Emmett Hale/Burrow.epub")

	if err := s.DeleteLibrary(libID); err != nil {
		t.Fatalf("DeleteLibrary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "Emmett Hale", "Burrow.epub")); err != nil {
		t.Errorf("file must survive a library-only delete: %v", err)
	}
}

// mode=files deletes this library's files and prunes the folders they
// emptied, leaving unrelated files and the root itself alone.
func TestDeleteLibraryFilesPrunesEmptyDirs(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	libID := newLibrary(t, s, "Main", root)
	shelveBook(t, s, libID, root, "Burrow", "Emmett Hale/Burrow.epub")
	shelveBook(t, s, libID, root, "Drift", "Emmett Hale/Drift.epub")

	// a second library's book, in its own root, must not be touched
	otherRoot := t.TempDir()
	otherID := newLibrary(t, s, "Other", otherRoot)
	shelveBook(t, s, otherID, otherRoot, "Haze", "Emmett Hale/Haze.epub")

	stray := filepath.Join(root, "Emmett Hale", "notes.txt")

	deleted, err := s.DeleteLibraryFiles(libID)
	if err != nil {
		t.Fatalf("DeleteLibraryFiles: %v", err)
	}
	if deleted != 2 {
		t.Errorf("deleted = %d, want 2", deleted)
	}
	if _, err := os.Stat(filepath.Dir(stray)); !os.IsNotExist(err) {
		t.Error("emptied author folder should be pruned")
	}
	if _, err := os.Stat(root); err != nil {
		t.Error("the library root itself must never be removed")
	}
	if _, err := os.Stat(filepath.Join(otherRoot, "Emmett Hale", "Haze.epub")); err != nil {
		t.Error("another library's files must survive")
	}
	if err := s.DeleteLibrary(libID); err != nil {
		t.Fatalf("DeleteLibrary: %v", err)
	}
}

func TestDeleteLibraryUnknownID(t *testing.T) {
	s := testStore(t)
	if err := s.DeleteLibrary(404); err == nil {
		t.Error("deleting a library that doesn't exist should error")
	}
}

// The plain per-book "Delete" drops the membership but leaves the file — it's
// what separates it from "Delete & remove file".
func TestRemoveBookLibraryModeKeepsFile(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	libID := newLibrary(t, s, "Main", root)
	bookID := shelveBook(t, s, libID, root, "Burrow", "Emmett Hale/Burrow.epub")
	path := filepath.Join(root, "Emmett Hale", "Burrow.epub")

	if err := s.RemoveBookFromLibrary(bookID, libID, "library"); err != nil {
		t.Fatalf("RemoveBookFromLibrary: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("mode=library must leave the file on disk: %v", err)
	}
	inLib, err := s.ListBooks(0, libID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(inLib) != 0 {
		t.Errorf("book still in the library after removal: %+v", inLib)
	}
}

func TestRemoveBookFileModeDeletesFile(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	libID := newLibrary(t, s, "Main", root)
	bookID := shelveBook(t, s, libID, root, "Burrow", "Emmett Hale/Burrow.epub")
	path := filepath.Join(root, "Emmett Hale", "Burrow.epub")

	if err := s.RemoveBookFromLibrary(bookID, libID, "file"); err != nil {
		t.Fatalf("RemoveBookFromLibrary: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("mode=file must delete the file from disk")
	}
}

// The Library view's "Recently added" sort reads addedAt, so list queries
// have to carry it.
func TestListBooksReportsAddedAt(t *testing.T) {
	s := testStore(t)
	root := t.TempDir()
	libID := newLibrary(t, s, "Main", root)
	bookID := shelveBook(t, s, libID, root, "Burrow", "Emmett Hale/Burrow.epub")

	books, err := s.ListBooks(0, libID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(books) != 1 {
		t.Fatalf("got %d books, want 1", len(books))
	}
	if books[0].AddedAt == "" {
		t.Error("ListBooks must report addedAt — the library sorts on it")
	}
	b, err := s.GetBook(bookID)
	if err != nil {
		t.Fatal(err)
	}
	if b.AddedAt == "" {
		t.Error("GetBook must report addedAt too")
	}
}
