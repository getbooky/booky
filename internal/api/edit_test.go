package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbooky/booky/internal/metadata"
)

func TestEditBookLocksFields(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	_, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/x"})
	libID := int64(out["id"].(float64))
	_, book := doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "manual", "title": "Burrow", "authors": []string{"Emmett Hale"}, "description": "orig"},
		"libraryId": libID, "monitored": true,
	})
	id := int64(book["id"].(float64))

	rec, edited := doJSON(t, h, "PATCH", fmt.Sprintf("/api/v1/books/%d", id), map[string]any{
		"fields": map[string]string{"description": "my edit"},
	})
	if rec.Code != http.StatusOK || edited["description"] != "my edit" {
		t.Fatalf("edit: %d %v", rec.Code, edited)
	}

	// a provider refresh (upsert) must not clobber the locked field
	if _, err := srv.Catalog.UpsertBook(stubBurrow()); err != nil {
		t.Fatal(err)
	}
	got, _ := srv.Catalog.GetBook(id)
	if got.Description != "my edit" {
		t.Errorf("locked field overwritten: %q", got.Description)
	}

	rec, _ = doJSON(t, h, "PATCH", fmt.Sprintf("/api/v1/books/%d", id), map[string]any{
		"fields": map[string]string{"nope": "x"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field should 400, got %d", rec.Code)
	}
}

// Genres, series and ISBN-13 are editable; series edits resolve the relation
// (find-or-create under the book's author) and lock as "series" so refreshes
// keep manual values.
func TestEditBookGenresSeriesISBN(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	_, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/x"})
	libID := int64(out["id"].(float64))
	_, book := doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "manual", "title": "Burrow", "authors": []string{"Emmett Hale"}},
		"libraryId": libID, "monitored": true,
	})
	id := int64(book["id"].(float64))

	rec, edited := doJSON(t, h, "PATCH", fmt.Sprintf("/api/v1/books/%d", id), map[string]any{
		"fields": map[string]string{
			"genres":     "Science Fiction, Dystopia,  ,Post-Apocalyptic",
			"seriesName": "Vault",
			"seriesNum":  "1.5",
			"isbn13":     "9780358447849",
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("edit: %d %v", rec.Code, edited)
	}
	got, err := srv.Catalog.GetBook(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Genres) != 3 || got.Genres[0] != "Science Fiction" || got.Genres[2] != "Post-Apocalyptic" {
		t.Errorf("genres = %v", got.Genres)
	}
	if got.SeriesName != "Vault" || got.SeriesNum != 1.5 {
		t.Errorf("series = %q #%v", got.SeriesName, got.SeriesNum)
	}
	if got.ISBN13 != "9780358447849" {
		t.Errorf("isbn13 = %q", got.ISBN13)
	}

	// a provider refresh must not clobber the locked genres or series
	refresh := stubBurrow()
	refresh.Genres = []string{"Provider Genre"}
	refresh.SeriesName = "Wrong Series"
	refresh.SeriesIndex = 9
	if _, err := srv.Catalog.UpsertBook(refresh); err != nil {
		t.Fatal(err)
	}
	got, _ = srv.Catalog.GetBook(id)
	if got.SeriesName != "Vault" || got.SeriesNum != 1.5 || len(got.Genres) != 3 {
		t.Errorf("refresh clobbered locked fields: series=%q #%v genres=%v", got.SeriesName, got.SeriesNum, got.Genres)
	}

	// clearing the series detaches book and position
	rec, _ = doJSON(t, h, "PATCH", fmt.Sprintf("/api/v1/books/%d", id), map[string]any{
		"fields": map[string]string{"seriesName": ""},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("clear series: %d", rec.Code)
	}
	got, _ = srv.Catalog.GetBook(id)
	if got.SeriesName != "" || got.SeriesNum != 0 {
		t.Errorf("series not cleared: %q #%v", got.SeriesName, got.SeriesNum)
	}

	// junk series number is rejected
	rec, _ = doJSON(t, h, "PATCH", fmt.Sprintf("/api/v1/books/%d", id), map[string]any{
		"fields": map[string]string{"seriesNum": "one and a half"},
	})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("bad seriesNum should 400, got %d", rec.Code)
	}
}

// Ratings arrive from providers and persist through upserts; bigger counts
// win so a Goodreads refresh never regresses to Hardcover's smaller scale.
func TestRatingsPersistAndGrow(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	_, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/x"})
	libID := int64(out["id"].(float64))
	meta := stubBurrow()
	meta.RatingsCount = 490
	_, book := doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"}, "ratingsCount": 490},
		"libraryId": libID, "monitored": true,
	})
	id := int64(book["id"].(float64))
	got, err := srv.Catalog.GetBook(id)
	if err != nil {
		t.Fatal(err)
	}
	if got.RatingsCount != 490 {
		t.Errorf("ratings = %d, want 490", got.RatingsCount)
	}
	refresh := stubBurrow()
	refresh.RatingsCount = 77729
	if _, err := srv.Catalog.UpsertBook(refresh); err != nil {
		t.Fatal(err)
	}
	got, _ = srv.Catalog.GetBook(id)
	if got.RatingsCount != 77729 {
		t.Errorf("refresh ratings = %d, want 77729", got.RatingsCount)
	}
}

func TestBookMonitorAndSeriesShelving(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	_, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/x"})
	libID := int64(out["id"].(float64))
	_, book := doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "manual", "title": "Burrow", "authors": []string{"Emmett Hale"}, "seriesName": "Vault", "seriesIndex": 1},
		"libraryId": libID, "monitored": true,
	})
	bookID := int64(book["id"].(float64))

	rec, _ := doJSON(t, h, "PUT", fmt.Sprintf("/api/v1/libraries/%d/books/%d/monitored", libID, bookID),
		map[string]bool{"monitored": false})
	if rec.Code != http.StatusOK {
		t.Fatalf("book monitor: %d", rec.Code)
	}
	books, _ := srv.Catalog.ListBooks(0, libID, 0)
	if books[0].Monitored {
		t.Error("book still monitored")
	}

	// Shelving the series names the library and re-monitors what's in it.
	// This replaced a toggle that flipped a global flag and cascaded into
	// every library holding any book of the series.
	_, series := doJSON(t, h, "GET", "/api/v1/series", nil)
	seriesID := int64(series["series"].([]any)[0].(map[string]any)["id"].(float64))
	rec, _ = doJSON(t, h, "POST", fmt.Sprintf("/api/v1/series/%d/library", seriesID),
		map[string]any{"libraryId": libID})
	if rec.Code != http.StatusOK {
		t.Fatalf("add series to library: %d", rec.Code)
	}
	books, _ = srv.Catalog.ListBooks(0, libID, 0)
	if !books[0].Monitored {
		t.Error("shelving the series should monitor its books in that library")
	}

	// naming no library is a bad request, not a guess
	rec, _ = doJSON(t, h, "POST", fmt.Sprintf("/api/v1/series/%d/library", seriesID), map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("shelving with no library: %d, want 400", rec.Code)
	}
}

func stubBurrow() metadata.BookMeta {
	return metadata.BookMeta{
		Provider: "stub", Title: "Burrow", Authors: []string{"Emmett Hale"},
		Description: "provider refresh text",
	}
}

// Saving a settings form without retyping a secret sends the "********" mask
// back — writing it would replace the real credential. The mask must round-
// trip as "unchanged".
func TestSecretMaskWriteIsNoOp(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	rec, _ := doJSON(t, h, "PUT", "/api/v1/settings/zlib_password", map[string]string{"value": "real-secret"})
	if rec.Code != http.StatusOK {
		t.Fatalf("set secret: %d %s", rec.Code, rec.Body)
	}
	rec, out := doJSON(t, h, "GET", "/api/v1/settings/zlib_password", nil)
	if rec.Code != http.StatusOK || out["value"] != "********" {
		t.Fatalf("secret must echo masked: %d %v", rec.Code, out)
	}
	rec, _ = doJSON(t, h, "PUT", "/api/v1/settings/zlib_password", map[string]string{"value": "********"})
	if rec.Code != http.StatusOK {
		t.Fatalf("mask write: %d %s", rec.Code, rec.Body)
	}
	if got := srv.Settings.Get("zlib_password"); got != "real-secret" {
		t.Fatalf("mask write clobbered the secret: %q", got)
	}
	// a real new value still writes
	rec, _ = doJSON(t, h, "PUT", "/api/v1/settings/zlib_password", map[string]string{"value": "rotated"})
	if rec.Code != http.StatusOK || srv.Settings.Get("zlib_password") != "rotated" {
		t.Fatal("real writes must still apply")
	}
}

// Manual import accepts a browser upload: the multipart file streams into
// the downloads dir and delivers into the library like any other import.
func TestManualImportUpload(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	if err := srv.Settings.Set("downloads_dir", t.TempDir()); err != nil {
		t.Fatal(err)
	}

	rec, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/books/alex"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library: %d %s", rec.Code, rec.Body)
	}
	libID := int64(out["id"].(float64))
	// the test library root must actually exist for delivery
	root := t.TempDir()
	if _, err := srv.db.Exec(`UPDATE libraries SET root_path = ? WHERE id = ?`, root, libID); err != nil {
		t.Fatal(err)
	}
	rec, out = doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"libraryId": libID, "monitored": false,
		"meta": map[string]any{"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add book: %d %s", rec.Code, rec.Body)
	}
	bookID := int64(out["id"].(float64))

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if err := mw.WriteField("libraryId", fmt.Sprint(libID)); err != nil {
		t.Fatal(err)
	}
	fw, err := mw.CreateFormFile("file", "Burrow Uploaded.epub")
	if err != nil {
		t.Fatal(err)
	}
	// real ZIP magic — the server sniffs content, not just the extension
	epubBytes := append([]byte("PK\x03\x04"), []byte("rest-of-epub")...)
	if _, err := fw.Write(epubBytes); err != nil {
		t.Fatal(err)
	}
	_ = mw.Close()

	req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/books/%d/import", bookID), &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("upload import: %d %s", rec2.Code, rec2.Body)
	}
	var resp struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(rec2.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(resp.Path)
	if err != nil || !bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		t.Fatalf("delivered file: %v %q", err, data)
	}
	if !strings.HasPrefix(resp.Path, root) {
		t.Errorf("delivered outside the library root: %q", resp.Path)
	}
}

// Uploads that aren't really books are rejected server-side: a disallowed
// extension, and an allowed extension whose bytes don't match the format.
func TestManualImportUploadRejectsNonBooks(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	dl := t.TempDir()
	if err := srv.Settings.Set("downloads_dir", dl); err != nil {
		t.Fatal(err)
	}
	rec, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/books/alex"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library: %d %s", rec.Code, rec.Body)
	}
	libID := int64(out["id"].(float64))
	rec, out = doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"libraryId": libID, "monitored": false,
		"meta": map[string]any{"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"}},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add book: %d %s", rec.Code, rec.Body)
	}
	bookID := int64(out["id"].(float64))

	upload := func(filename string, content []byte) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		_ = mw.WriteField("libraryId", fmt.Sprint(libID))
		fw, _ := mw.CreateFormFile("file", filename)
		_, _ = fw.Write(content)
		_ = mw.Close()
		req := httptest.NewRequest("POST", fmt.Sprintf("/api/v1/books/%d/import", bookID), &buf)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	if rec := upload("malware.exe", []byte("MZ\x90\x00")); rec.Code != http.StatusBadRequest {
		t.Errorf("exe upload accepted: %d %s", rec.Code, rec.Body)
	}
	if rec := upload("disguised.epub", []byte("MZ\x90\x00not-a-zip")); rec.Code != http.StatusBadRequest {
		t.Errorf("mislabeled epub accepted: %d %s", rec.Code, rec.Body)
	}
	if rec := upload("fake.pdf", []byte("<html>not a pdf</html>")); rec.Code != http.StatusBadRequest {
		t.Errorf("fake pdf accepted: %d %s", rec.Code, rec.Body)
	}
	// nothing may linger in the uploads dir after rejections
	entries, _ := os.ReadDir(filepath.Join(dl, "uploads"))
	if len(entries) != 0 {
		t.Errorf("rejected uploads left files behind: %v", entries)
	}
}
