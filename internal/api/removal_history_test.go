package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/getbooky/booky/internal/catalog"
)

// historyKinds returns the recorded history, newest first.
func recentHistory(t *testing.T, srv *Server) []catalog.HistoryItem {
	t.Helper()
	items, err := srv.Catalog.ListHistory(50, nil)
	if err != nil {
		t.Fatalf("ListHistory: %v", err)
	}
	return items
}

// findHistory returns the first entry of kind whose detail contains want.
func findHistory(t *testing.T, srv *Server, kind, want string) catalog.HistoryItem {
	t.Helper()
	items := recentHistory(t, srv)
	for _, it := range items {
		if it.Kind == kind && strings.Contains(it.Detail, want) {
			return it
		}
	}
	t.Fatalf("no %q history mentioning %q; got %+v", kind, want, items)
	return catalog.HistoryItem{}
}

// A delete is the one action that leaves no other trace — the book simply
// stops being there. Each mode has to say how far it went, and name the book
// while the row still exists to be named.
func TestRemoveBookRecordsHistory(t *testing.T) {
	for mode, want := range map[string]string{
		"library": "file kept",
		"file":    "file deleted",
		"block":   "blocked from re-import",
	} {
		t.Run(mode, func(t *testing.T) {
			srv := testServer(t)
			h := srv.Handler()
			libID, _ := addLibraryWithBook(t, srv, h)
			books, err := srv.Catalog.ListBooks(0, libID, 0)
			if err != nil || len(books) != 1 {
				t.Fatalf("ListBooks: %v %+v", err, books)
			}
			rec, _ := doJSON(t, h, "DELETE",
				"/api/v1/libraries/"+intToStr(libID)+"/books/"+intToStr(books[0].ID)+"?mode="+mode, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("remove: %d %s", rec.Code, rec.Body)
			}
			it := findHistory(t, srv, "removed", "Burrow")
			if !strings.Contains(it.Detail, want) {
				t.Errorf("mode=%s detail = %q, want it to mention %q", mode, it.Detail, want)
			}
		})
	}
}

func TestRevokeDeviceRecordsHistory(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, _, _ := seedShelf(t, srv)
	if err := srv.Settings.Set("server_url", "http://booky.local:8787"); err != nil {
		t.Fatal(err)
	}

	rec, out := doJSON(t, h, "POST", "/api/v1/devices", map[string]any{
		"name": "Kobo", "libraryIds": []int64{libID}, "autoLibraryIds": []int64{libID}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create device: %d %s", rec.Code, rec.Body)
	}
	deviceID := int64(out["id"].(float64))

	rec, _ = doJSON(t, h, "DELETE", "/api/v1/devices/"+intToStr(deviceID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	it := findHistory(t, srv, "removed", "device")
	if !strings.Contains(it.Detail, `"Kobo"`) {
		t.Errorf("detail = %q, want the device name", it.Detail)
	}
	// scoped to one of the device's own libraries, so the owner sees it
	if it.LibraryID != libID {
		t.Errorf("library id = %d, want %d — an install-wide row hides from the owner", it.LibraryID, libID)
	}
}

// Deleting a watched list leaves everything it added on the shelf. The entry
// has to say so: "list deleted" alone reads like the books went too.
func TestDeleteListRecordsHistory(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, _, _ := seedShelf(t, srv)

	rec, out := doJSON(t, h, "POST", "/api/v1/lists", map[string]any{
		"name": "Alex's shelf", "kind": "hardcover", "sourceRef": "123",
		"libraryId": libID, "monitorScope": "book", "onRemove": "nothing", "enabled": true})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create list: %d %s", rec.Code, rec.Body)
	}
	listID := int64(out["id"].(float64))

	rec, _ = doJSON(t, h, "DELETE", "/api/v1/lists/"+intToStr(listID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete list: %d %s", rec.Code, rec.Body)
	}
	it := findHistory(t, srv, "removed", "watched list")
	if !strings.Contains(it.Detail, `"Alex's shelf"`) || !strings.Contains(it.Detail, "stay in the library") {
		t.Errorf("detail = %q, want the list name and the books' fate", it.Detail)
	}
	if it.LibraryID != libID {
		t.Errorf("library id = %d, want %d", it.LibraryID, libID)
	}
}

func TestDeleteLibraryRecordsHistory(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, _ := addLibraryWithBook(t, srv, h)

	rec, _ := doJSON(t, h, "DELETE", "/api/v1/libraries/"+intToStr(libID)+"?mode=files", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete library: %d %s", rec.Code, rec.Body)
	}
	it := findHistory(t, srv, "removed", "library")
	if !strings.Contains(it.Detail, `"Alex"`) || !strings.Contains(it.Detail, "1 file(s) deleted") {
		t.Errorf("detail = %q, want the library name and the file count", it.Detail)
	}
}

func TestDeleteAuthorRecordsHistory(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, _ := addLibraryWithBook(t, srv, h)
	books, err := srv.Catalog.ListBooks(0, libID, 0)
	if err != nil || len(books) != 1 {
		t.Fatalf("ListBooks: %v %+v", err, books)
	}

	rec, _ := doJSON(t, h, "DELETE", "/api/v1/authors/"+intToStr(books[0].AuthorID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete author: %d %s", rec.Code, rec.Body)
	}
	it := findHistory(t, srv, "removed", "author")
	if !strings.Contains(it.Detail, `"Emmett Hale"`) || !strings.Contains(it.Detail, "1 book(s)") {
		t.Errorf("detail = %q, want the author name and their book count", it.Detail)
	}
	// the author's books cascade away, so the entry must stand on its own
	if it.BookID != 0 {
		t.Errorf("book id = %d, want an install-wide entry", it.BookID)
	}
}
