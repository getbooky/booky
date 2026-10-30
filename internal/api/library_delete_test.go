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

// There is one library holding every book. Deleting it would empty the
// install, and a second one would bring back the per-person split tags
// replaced — both are refused, even for an admin.
func TestTheLibraryIsTheOnlyLibrary(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, path := addLibraryWithBook(t, srv, h)

	rec, _ := doJSON(t, h, "DELETE", "/api/v1/libraries/"+intToStr(libID)+"?mode=files", nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("deleting the library: got %d, want 400", rec.Code)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("a refused delete touched the file: %v", err)
	}
	rec, _ = doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Second", "rootPath": filepath.Join(srv.mediaRoot, "second")})
	if rec.Code != http.StatusConflict {
		t.Fatalf("creating a second library: got %d, want 409", rec.Code)
	}
	if n := libraryCount(t, h); n != 1 {
		t.Fatalf("libraries = %d, want 1", n)
	}
}
