package api

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/getbooky/booky/internal/metadata"
)

func pngBytes(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 3))
	for x := 0; x < 2; x++ {
		for y := 0; y < 3; y++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadCover(t *testing.T, h http.Handler, bookID int64, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "cover.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("PUT", "/api/v1/books/"+intToStr(bookID)+"/cover/custom", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// A cover lives at a stable URL while its bytes change, so the response must
// tell the browser to revalidate. With a day-long max-age an uploaded cover
// kept showing the old art everywhere that didn't append a cache-busting
// query — which is most views.
func TestCoverIsServedRevalidating(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, _ := addLibraryWithBook(t, srv, h)
	_ = libID

	first := pngBytes(t, color.RGBA{R: 255, A: 255})
	if rec := uploadCover(t, h, 1, first); rec.Code != http.StatusOK {
		t.Fatalf("upload cover: %d %s", rec.Code, rec.Body)
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/covers/1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("get cover: %d", rec.Code)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want no-cache so a replaced cover shows up", cc)
	}
	if rec.Header().Get("Last-Modified") == "" {
		t.Error("no Last-Modified — revalidation would re-download the bytes every time")
	}
	if !bytes.Equal(rec.Body.Bytes(), first) {
		t.Error("served bytes are not the uploaded cover")
	}

	// replacing the cover serves the new bytes at the same URL
	second := pngBytes(t, color.RGBA{B: 255, A: 255})
	if rec := uploadCover(t, h, 1, second); rec.Code != http.StatusOK {
		t.Fatalf("replace cover: %d %s", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/covers/1", nil))
	if !bytes.Equal(rec.Body.Bytes(), second) {
		t.Error("the replaced cover was not served")
	}
}

// Uploading a cover locks it, and a later refresh must leave it alone even
// though refresh now re-fetches covers.
func TestRefreshKeepsLockedCustomCover(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	addLibraryWithBook(t, srv, h)

	mine := pngBytes(t, color.RGBA{G: 255, A: 255})
	if rec := uploadCover(t, h, 1, mine); rec.Code != http.StatusOK {
		t.Fatalf("upload cover: %d %s", rec.Code, rec.Body)
	}
	rec, out := doJSON(t, h, "POST", "/api/v1/books/1/refresh", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d %s", rec.Code, rec.Body)
	}
	if locks, _ := out["fieldLocks"].(map[string]any); locks["cover"] != true {
		t.Errorf("cover lock lost across a refresh: %v", out["fieldLocks"])
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/covers/1", nil))
	if !bytes.Equal(rec.Body.Bytes(), mine) {
		t.Error("a refresh overwrote the user's locked cover")
	}
}

// The Hardcover-changed-its-art case: the provider now serves a different
// cover for the same book, and "Refresh metadata" is where people go to pick
// it up. It used to keep whatever was cached forever, because a cached cover
// short-circuits the fetch.
func TestRefreshAdoptsProviderCoverChange(t *testing.T) {
	art := "first-art"
	images := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(art)) //nolint:errcheck // test server
	}))
	defer images.Close()

	srv := testServer(t)
	// a provider that credits this book with a cover, served from above
	srv.Chain = metadata.NewChain(func() []string { return []string{"stub"} },
		&stubProvider{results: []metadata.BookMeta{{
			Provider: "stub", Title: "Burrow", Authors: []string{"Emmett Hale"}, CoverURL: images.URL,
		}}})
	h := srv.Handler()
	addLibraryWithBook(t, srv, h)

	if rec, _ := doJSON(t, h, "POST", "/api/v1/books/1/refresh", nil); rec.Code != http.StatusOK {
		t.Fatalf("first refresh: %d", rec.Code)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/covers/1", nil))
	if got := rec.Body.String(); got != "first-art" {
		t.Fatalf("cover = %q, want the provider's art", got)
	}

	art = "new-art" // the cover changed at the source
	if rec, _ := doJSON(t, h, "POST", "/api/v1/books/1/refresh", nil); rec.Code != http.StatusOK {
		t.Fatalf("second refresh: %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/covers/1", nil))
	if got := rec.Body.String(); got != "new-art" {
		t.Errorf("cover = %q, want the provider's updated art after a refresh", got)
	}
}

// unlockCover clears the auto-lock an uploaded cover carries, so the
// regenerate/refresh paths are allowed to touch it.
func unlockCover(t *testing.T, h http.Handler, bookID int64) {
	t.Helper()
	rec, _ := doJSON(t, h, "PUT", "/api/v1/books/"+intToStr(bookID)+"/lock",
		map[string]any{"field": "cover", "locked": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("unlock cover: %d %s", rec.Code, rec.Body)
	}
}

// Regenerate cover asks the providers first and swaps only on success. It
// used to delete the cached cover up front, so a book whose providers had no
// cover lost the one it had — including a cover the user uploaded and then
// unlocked.
func TestRegenCoverKeepsExistingWhenNoProviderCover(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler() // the default stub credits no cover url
	addLibraryWithBook(t, srv, h)

	mine := pngBytes(t, color.RGBA{G: 255, A: 255})
	if rec := uploadCover(t, h, 1, mine); rec.Code != http.StatusOK {
		t.Fatalf("upload cover: %d %s", rec.Code, rec.Body)
	}
	unlockCover(t, h, 1)

	rec, _ := doJSON(t, h, "POST", "/api/v1/books/1/cover", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404 when no provider has a cover", rec.Code)
	}
	got := httptest.NewRecorder()
	h.ServeHTTP(got, httptest.NewRequest("GET", "/api/v1/covers/1", nil))
	if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), mine) {
		t.Errorf("the existing cover was destroyed by a failed regenerate (status %d)", got.Code)
	}
}

// A regenerate whose download fails must also leave the old cover in place.
func TestRegenCoverKeepsExistingWhenFetchFails(t *testing.T) {
	images := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer images.Close()

	srv := testServer(t)
	srv.Chain = metadata.NewChain(func() []string { return []string{"stub"} },
		&stubProvider{results: []metadata.BookMeta{{
			Provider: "stub", Title: "Burrow", Authors: []string{"Emmett Hale"}, CoverURL: images.URL,
		}}})
	h := srv.Handler()
	addLibraryWithBook(t, srv, h)

	mine := pngBytes(t, color.RGBA{R: 255, A: 255})
	if rec := uploadCover(t, h, 1, mine); rec.Code != http.StatusOK {
		t.Fatalf("upload cover: %d %s", rec.Code, rec.Body)
	}
	unlockCover(t, h, 1)

	rec, _ := doJSON(t, h, "POST", "/api/v1/books/1/cover", nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 when the image can't be fetched", rec.Code)
	}
	got := httptest.NewRecorder()
	h.ServeHTTP(got, httptest.NewRequest("GET", "/api/v1/covers/1", nil))
	if !bytes.Equal(got.Body.Bytes(), mine) {
		t.Error("a failed download destroyed the existing cover")
	}
}

// The happy path still does what it says: the provider's art replaces the
// cached image.
func TestRegenCoverReplacesWithProviderArt(t *testing.T) {
	images := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("provider-art")) //nolint:errcheck // test server
	}))
	defer images.Close()

	srv := testServer(t)
	srv.Chain = metadata.NewChain(func() []string { return []string{"stub"} },
		&stubProvider{results: []metadata.BookMeta{{
			Provider: "stub", Title: "Burrow", Authors: []string{"Emmett Hale"}, CoverURL: images.URL,
		}}})
	h := srv.Handler()
	addLibraryWithBook(t, srv, h)

	if rec := uploadCover(t, h, 1, pngBytes(t, color.RGBA{B: 255, A: 255})); rec.Code != http.StatusOK {
		t.Fatalf("upload cover: %d %s", rec.Code, rec.Body)
	}
	unlockCover(t, h, 1)

	if rec, _ := doJSON(t, h, "POST", "/api/v1/books/1/cover", nil); rec.Code != http.StatusOK {
		t.Fatalf("regenerate: %d", rec.Code)
	}
	got := httptest.NewRecorder()
	h.ServeHTTP(got, httptest.NewRequest("GET", "/api/v1/covers/1", nil))
	if got.Body.String() != "provider-art" {
		t.Errorf("cover = %q, want the provider's art", got.Body.String())
	}
}

// A locked cover is refused before anything is touched.
func TestRegenCoverRefusesWhileLocked(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	addLibraryWithBook(t, srv, h)

	mine := pngBytes(t, color.RGBA{R: 200, G: 200, A: 255})
	if rec := uploadCover(t, h, 1, mine); rec.Code != http.StatusOK {
		t.Fatalf("upload cover: %d %s", rec.Code, rec.Body)
	}
	rec, _ := doJSON(t, h, "POST", "/api/v1/books/1/cover", nil)
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409 for a locked cover", rec.Code)
	}
	got := httptest.NewRecorder()
	h.ServeHTTP(got, httptest.NewRequest("GET", "/api/v1/covers/1", nil))
	if !bytes.Equal(got.Body.Bytes(), mine) {
		t.Error("the locked cover was replaced")
	}
}

// The edit endpoint accepts an author and reports the moved book.
func TestEditBookAuthorEndpoint(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	addLibraryWithBook(t, srv, h)

	rec, out := doJSON(t, h, "PATCH", "/api/v1/books/1", map[string]any{
		"fields": map[string]string{"author": "Livia Sparrow"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("edit author: %d %s", rec.Code, rec.Body)
	}
	if out["author"] != "Livia Sparrow" {
		t.Errorf("author = %v, want Livia Sparrow", out["author"])
	}
	rec, out = doJSON(t, h, "GET", "/api/v1/authors", nil)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Code)
	}
	names := map[string]bool{}
	for _, a := range out["authors"].([]any) {
		names[a.(map[string]any)["name"].(string)] = true
	}
	if !names["Livia Sparrow"] {
		t.Errorf("the new author is missing from the authors list: %v", names)
	}
}
