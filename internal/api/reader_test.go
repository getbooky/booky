package api

import (
	"net/http"
	"strings"
	"testing"
)

// TestReaderFileAndProgress: the web reader streams the shelved file through
// the session-authed API and round-trips a reading position per user.
func TestReaderFileAndProgress(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	_, bookID, _ := seedShelf(t, srv)

	// the shelved file streams back inline with its real filename
	rec, _ := doJSON(t, h, "GET", "/api/v1/books/"+intToStr(bookID)+"/file", nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "epub-bytes" {
		t.Fatalf("file: %d %q", rec.Code, rec.Body.String())
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, `inline`) || !strings.Contains(cd, "Burrow.epub") {
		t.Fatalf("content-disposition: %q", cd)
	}

	// a book with nothing on disk has nothing to stream
	rec, _ = doJSON(t, h, "GET", "/api/v1/books/999999/file", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("file for missing book: %d", rec.Code)
	}

	// progress starts empty, round-trips, and upserts in place
	rec, out := doJSON(t, h, "GET", "/api/v1/books/"+intToStr(bookID)+"/progress", nil)
	if rec.Code != http.StatusOK || out["locator"] != "" {
		t.Fatalf("empty progress: %d %v", rec.Code, out)
	}
	rec, _ = doJSON(t, h, "PUT", "/api/v1/books/"+intToStr(bookID)+"/progress",
		map[string]any{"locator": "epubcfi(/6/4!/4/2/1:0)", "percent": 0.42})
	if rec.Code != http.StatusOK {
		t.Fatalf("put progress: %d %s", rec.Code, rec.Body)
	}
	rec, _ = doJSON(t, h, "PUT", "/api/v1/books/"+intToStr(bookID)+"/progress",
		map[string]any{"locator": "epubcfi(/6/8!/4/2/1:0)", "percent": 0.6})
	if rec.Code != http.StatusOK {
		t.Fatalf("upsert progress: %d %s", rec.Code, rec.Body)
	}
	rec, out = doJSON(t, h, "GET", "/api/v1/books/"+intToStr(bookID)+"/progress", nil)
	if rec.Code != http.StatusOK || out["locator"] != "epubcfi(/6/8!/4/2/1:0)" || out["percent"] != 0.6 {
		t.Fatalf("progress round-trip: %d %v", rec.Code, out)
	}

	// out-of-range percent is refused
	rec, _ = doJSON(t, h, "PUT", "/api/v1/books/"+intToStr(bookID)+"/progress",
		map[string]any{"locator": "x", "percent": 1.5})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad percent must refuse: %d", rec.Code)
	}
}
