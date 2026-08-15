package api

import (
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// addLibraryWithBook creates a library rooted at a temp dir holding one
// shelved book with a real file, and returns the library id and file path.
func addLibraryWithBook(t *testing.T, srv *Server, h http.Handler) (int64, string) {
	t.Helper()
	root := t.TempDir()
	srv.mediaRoot = root // library roots must sit under the media mount
	rec, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": root})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library: %d %s", rec.Code, rec.Body)
	}
	libID := int64(out["id"].(float64))

	rec, out = doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "manual", "title": "Burrow", "authors": []string{"Emmett Hale"}},
		"libraryId": libID,
		"monitored": true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add book: %d %s", rec.Code, rec.Body)
	}
	bookID := int64(out["id"].(float64))

	path := filepath.Join(root, "Emmett Hale", "Burrow.epub")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("book"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := srv.Catalog.SetFile(libID, bookID, path, "epub", 4); err != nil {
		t.Fatal(err)
	}
	return libID, path
}

func libraryCount(t *testing.T, h http.Handler) int {
	t.Helper()
	rec, out := doJSON(t, h, "GET", "/api/v1/libraries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list libraries: %d %s", rec.Code, rec.Body)
	}
	libs, _ := out["libraries"].([]any)
	return len(libs)
}

func TestDeleteLibraryKeepMode(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, path := addLibraryWithBook(t, srv, h)

	rec, out := doJSON(t, h, "DELETE", "/api/v1/libraries/"+intToStr(libID)+"?mode=keep", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete library: %d %s", rec.Code, rec.Body)
	}
	if out["deletedFiles"].(float64) != 0 {
		t.Errorf("keep mode reported deleted files: %v", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file must survive mode=keep: %v", err)
	}
	if n := libraryCount(t, h); n != 0 {
		t.Errorf("library still listed: %d", n)
	}
	// the book leaves the library view but keeps its catalog row
	rec, out = doJSON(t, h, "GET", "/api/v1/books?libraryId="+intToStr(libID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list books: %d %s", rec.Code, rec.Body)
	}
	if books, _ := out["books"].([]any); len(books) != 0 {
		t.Errorf("deleted library still lists books: %v", books)
	}
}

func TestDeleteLibraryFilesMode(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, path := addLibraryWithBook(t, srv, h)

	rec, out := doJSON(t, h, "DELETE", "/api/v1/libraries/"+intToStr(libID)+"?mode=files", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete library: %d %s", rec.Code, rec.Body)
	}
	if out["deletedFiles"].(float64) != 1 {
		t.Errorf("deletedFiles = %v, want 1", out["deletedFiles"])
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("mode=files must delete the library's files")
	}
	if n := libraryCount(t, h); n != 0 {
		t.Errorf("library still listed: %d", n)
	}
}

func TestDeleteLibraryRejectsBadMode(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, _ := addLibraryWithBook(t, srv, h)

	rec, _ := doJSON(t, h, "DELETE", "/api/v1/libraries/"+intToStr(libID)+"?mode=nuke", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if n := libraryCount(t, h); n != 1 {
		t.Errorf("rejected delete must leave the library alone: %d", n)
	}
}

func TestDeleteLibraryUnknownID(t *testing.T) {
	h := testServer(t).Handler()
	rec, _ := doJSON(t, h, "DELETE", "/api/v1/libraries/404", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// The per-book delete has three modes now; "library" is the one that leaves
// the file alone.
func TestRemoveBookLibraryModeKeepsFile(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, path := addLibraryWithBook(t, srv, h)

	rec, _ := doJSON(t, h, "DELETE",
		"/api/v1/libraries/"+intToStr(libID)+"/books/1?mode=library", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove book: %d %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("mode=library must leave the file on disk: %v", err)
	}
	rec, out := doJSON(t, h, "GET", "/api/v1/books?libraryId="+intToStr(libID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list books: %d", rec.Code)
	}
	if books, _ := out["books"].([]any); len(books) != 0 {
		t.Errorf("removed book still in the library: %v", books)
	}
}
