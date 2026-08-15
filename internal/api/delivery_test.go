package api

import (
	"archive/zip"
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/getbooky/booky/internal/metadata"
)

// seedShelf puts one on-shelf book (with a real file on disk) into a fresh
// library and returns (libraryID, bookID, filePath).
func seedShelf(t *testing.T, srv *Server) (int64, int64, string) {
	t.Helper()
	root := t.TempDir()
	profileID, err := srv.Catalog.EnsureDefaultProfile()
	if err != nil {
		t.Fatal(err)
	}
	libID, err := srv.Catalog.CreateLibrary("Shelf", root, profileID, "shelf", "unset")
	if err != nil {
		t.Fatal(err)
	}
	bookID, err := srv.Catalog.UpsertBook(metadata.BookMeta{
		Provider: "test", Title: "Burrow", Authors: []string{"Emmett Hale"},
		Description: "Vault #1", SeriesName: "Vault", SeriesIndex: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := srv.Catalog.AddToLibrary(bookID, libID, true); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Emmett Hale", "Burrow.epub")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("epub-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := srv.Catalog.SetFile(libID, bookID, path, "epub", 10); err != nil {
		t.Fatal(err)
	}
	return libID, bookID, path
}

func TestAuthLifecycle(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()

	// no users yet: the API is open (first-run wizard) and me() says so
	rec, out := doJSON(t, h, "GET", "/api/v1/auth/me", nil)
	if rec.Code != http.StatusOK || out["authRequired"] != false {
		t.Fatalf("me before users: %d %v", rec.Code, out)
	}
	rec, _ = doJSON(t, h, "GET", "/api/v1/books", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("api must be open before first user: %d", rec.Code)
	}

	// first user is forced to admin even if the request says otherwise
	rec, _ = doJSON(t, h, "POST", "/api/v1/users", map[string]string{
		"username": "alex", "password": "correct horse battery", "role": "user"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create first user: %d %s", rec.Code, rec.Body)
	}

	// now the API is locked
	rec, _ = doJSON(t, h, "GET", "/api/v1/books", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("api must lock once a user exists: %d", rec.Code)
	}

	// login → cookie → access; and the forced-admin role is visible
	rec, _ = doJSON(t, h, "POST", "/api/v1/auth/login", map[string]string{
		"username": "alex", "password": "correct horse battery"})
	if rec.Code != http.StatusOK {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	cookie := rec.Result().Cookies()
	if len(cookie) == 0 {
		t.Fatal("login must set a session cookie")
	}
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.AddCookie(cookie[0])
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"role":"admin"`) {
		t.Fatalf("me after login: %d %s", rec2.Code, rec2.Body)
	}

	req = httptest.NewRequest("GET", "/api/v1/books", nil)
	req.AddCookie(cookie[0])
	rec2 = httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK {
		t.Fatalf("books with session: %d", rec2.Code)
	}

	// OPDS keeps its own auth: still 401 with a session cookie but no basic auth
	libID, _, _ := seedShelf(t, srv)
	req = httptest.NewRequest("GET", "/opds/"+intToStr(libID), nil)
	req.AddCookie(cookie[0])
	rec2 = httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("opds must ignore sessions: %d", rec2.Code)
	}
}

func intToStr(v int64) string { return strconv.FormatInt(v, 10) }

func TestOPDSFeedAndDownload(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, bookID, _ := seedShelf(t, srv)
	base := "/opds/" + intToStr(libID)

	// unset credentials refuse everything, even with a guess
	req := httptest.NewRequest("GET", base, nil)
	req.SetBasicAuth("shelf", "anything-goes")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unset opds creds must refuse: %d", rec.Code)
	}

	// set credentials, then fetch the feed
	rec2, _ := doJSON(t, h, "PUT", "/api/v1/libraries/"+intToStr(libID)+"/opds",
		map[string]string{"username": "alex-shelf", "password": "read my books"})
	if rec2.Code != http.StatusOK {
		t.Fatalf("set opds creds: %d %s", rec2.Code, rec2.Body)
	}

	req = httptest.NewRequest("GET", base, nil)
	req.SetBasicAuth("alex-shelf", "read my books")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("feed: %d %s", rec.Code, rec.Body)
	}
	feedXML := rec.Body.String()
	for _, want := range []string{"<title>Burrow</title>", "Emmett Hale", "application/epub+zip",
		base + "/download/" + intToStr(bookID), "Vault #1"} {
		if !strings.Contains(feedXML, want) {
			t.Fatalf("feed missing %q:\n%s", want, feedXML)
		}
	}

	// download with the right creds gets the bytes; wrong password refused
	req = httptest.NewRequest("GET", base+"/download/"+intToStr(bookID), nil)
	req.SetBasicAuth("alex-shelf", "read my books")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "epub-bytes" {
		t.Fatalf("download: %d %q", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest("GET", base+"/download/"+intToStr(bookID), nil)
	req.SetBasicAuth("alex-shelf", "wrong password")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password must refuse download: %d", rec.Code)
	}
}

func TestKoReaderDeviceFlow(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	libID, bookID, _ := seedShelf(t, srv)
	if err := srv.Settings.Set("server_url", "http://booky.local:8787"); err != nil {
		t.Fatal(err)
	}

	rec, out := doJSON(t, h, "POST", "/api/v1/devices", map[string]any{
		"name": "Kobo", "libraryIds": []int64{libID}, "autoLibraryIds": []int64{libID}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create device: %d %s", rec.Code, rec.Body)
	}
	deviceID := int64(out["id"].(float64))

	// plugin zip is a valid archive with the device token baked in
	rec, _ = doJSON(t, h, "GET", "/api/v1/devices/"+intToStr(deviceID)+"/plugin.zip", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("plugin zip: %d", rec.Code)
	}
	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("plugin is not a zip: %v", err)
	}
	device, err := srv.KoReader.Get(deviceID)
	if err != nil {
		t.Fatal(err)
	}
	// entries live at the zip root so an extractor's wrapper folder (named
	// after booky.koplugin.zip) becomes the plugin folder itself
	var sawConfig bool
	for _, f := range zr.File {
		if strings.Contains(f.Name, "/") {
			t.Fatalf("zip entry %q is nested — extractors would double-wrap the plugin folder", f.Name)
		}
		if f.Name != "config.lua" {
			continue
		}
		sawConfig = true
		rc, _ := f.Open()
		body := new(bytes.Buffer)
		if _, err := body.ReadFrom(rc); err != nil {
			t.Fatal(err)
		}
		rc.Close()
		if !strings.Contains(body.String(), device.Token) || !strings.Contains(body.String(), "http://booky.local:8787") {
			t.Fatalf("config.lua missing token or server url:\n%s", body.String())
		}
	}
	if !sawConfig {
		t.Fatal("zip has no config.lua")
	}

	// sync with the bearer token sees the book; download works; revoke kills it
	req := httptest.NewRequest("GET", "/api/koreader/v1/sync", nil)
	req.Header.Set("Authorization", "Bearer "+device.Token)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK || !strings.Contains(rec2.Body.String(), `"Burrow"`) {
		t.Fatalf("sync: %d %s", rec2.Code, rec2.Body)
	}
	devices, _ := srv.KoReader.List()
	if len(devices) != 1 || devices[0].LastSync == "" {
		t.Fatalf("sync must stamp last_sync: %+v", devices)
	}

	req = httptest.NewRequest("GET", "/api/koreader/v1/download/"+intToStr(libID)+"/"+intToStr(bookID), nil)
	req.Header.Set("Authorization", "Bearer "+device.Token)
	rec2 = httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK || rec2.Body.String() != "epub-bytes" {
		t.Fatalf("device download: %d %q", rec2.Code, rec2.Body.String())
	}

	// the mosaic view fetches covers under the device token
	if err := srv.Covers.SaveBytes(bookID, []byte("jpeg-bytes")); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest("GET", "/api/koreader/v1/cover/"+intToStr(libID)+"/"+intToStr(bookID), nil)
	req.Header.Set("Authorization", "Bearer "+device.Token)
	rec2 = httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusOK || rec2.Body.String() != "jpeg-bytes" {
		t.Fatalf("device cover: %d %q", rec2.Code, rec2.Body.String())
	}
	// no token → refused; book outside the library → not found
	req = httptest.NewRequest("GET", "/api/koreader/v1/cover/"+intToStr(libID)+"/"+intToStr(bookID), nil)
	rec2 = httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("cover without token must refuse: %d", rec2.Code)
	}
	req = httptest.NewRequest("GET", "/api/koreader/v1/cover/"+intToStr(libID)+"/999999", nil)
	req.Header.Set("Authorization", "Bearer "+device.Token)
	rec2 = httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("cover for unshelved book must 404: %d", rec2.Code)
	}

	rec, _ = doJSON(t, h, "DELETE", "/api/v1/devices/"+intToStr(deviceID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d", rec.Code)
	}
	req = httptest.NewRequest("GET", "/api/koreader/v1/sync", nil)
	req.Header.Set("Authorization", "Bearer "+device.Token)
	rec2 = httptest.NewRecorder()
	h.ServeHTTP(rec2, req)
	if rec2.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token must be refused: %d", rec2.Code)
	}
}

func TestBackupEndpoints(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	rec, out := doJSON(t, h, "POST", "/api/v1/backups", nil)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create backup: %d %s", rec.Code, rec.Body)
	}
	name := out["name"].(string)
	rec, _ = doJSON(t, h, "GET", "/api/v1/backups", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), name) {
		t.Fatalf("list backups: %d %s", rec.Code, rec.Body)
	}
}

// TestExpandAuthorCatalogOnly: a bibliography refresh creates catalog-only
// books — visible on the author page, in no library — and monitoring one is
// what adds it to a library. Box sets and user-excluded titles never appear.
func TestExpandAuthorCatalogOnly(t *testing.T) {
	srv := testServer(t)
	h := srv.Handler()
	// the exclusion list REPLACES the defaults now (they're removable), so a
	// list that still wants box sets filtered carries the term itself
	if err := srv.Settings.Set("exclude_patterns", "large print\nbox set"); err != nil {
		t.Fatal(err)
	}

	rec, out := doJSON(t, h, "POST", "/api/v1/libraries", map[string]string{"name": "Alex", "rootPath": "/data/books/alex"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library: %d %s", rec.Code, rec.Body)
	}
	libID := int64(out["id"].(float64))

	// add one book normally — monitored — which also creates the author
	rec, _ = doJSON(t, h, "POST", "/api/v1/books", map[string]any{
		"libraryId": libID, "monitored": true,
		"meta": map[string]any{"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"}, "isbn13": "9781476735115"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add book: %d %s", rec.Code, rec.Body)
	}

	// works = Burrow (already known), Drift, Haze, box set (filtered), large print (excluded)
	rec, out = doJSON(t, h, "POST", "/api/v1/authors/1/expand", nil)
	if rec.Code != http.StatusOK || int(out["added"].(float64)) != 2 {
		t.Fatalf("expand: %d %v", rec.Code, out)
	}

	// the author page sees the whole bibliography...
	rec, _ = doJSON(t, h, "GET", "/api/v1/books?authorId=1", nil)
	body := rec.Body.String()
	for _, want := range []string{`"Burrow"`, `"Drift"`, `"Haze"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %s in %s", want, body)
		}
	}
	for _, banned := range []string{"Box Set", "Large Print"} {
		if strings.Contains(body, banned) {
			t.Fatalf("%s must not be imported: %s", banned, body)
		}
	}
	// ...but the library holds only what the user curated
	libBooks, err := srv.Catalog.ListBooks(0, libID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(libBooks) != 1 || libBooks[0].Title != "Burrow" {
		t.Fatalf("library must hold only curated books: %+v", libBooks)
	}

	// monitoring a catalog-only book pulls it into the library
	var driftID int64
	all, _ := srv.Catalog.ListBooks(1, 0, 0)
	for _, b := range all {
		if b.Title == "Drift" {
			driftID = b.ID
		}
	}
	rec, _ = doJSON(t, h, "PUT", "/api/v1/libraries/"+intToStr(libID)+"/books/"+intToStr(driftID)+"/monitored",
		map[string]bool{"monitored": true})
	if rec.Code != http.StatusOK {
		t.Fatalf("monitor catalog-only book: %d %s", rec.Code, rec.Body)
	}
	libBooks, _ = srv.Catalog.ListBooks(0, libID, 0)
	titles := map[string]bool{}
	for _, b := range libBooks {
		titles[b.Title] = b.Monitored
	}
	if len(libBooks) != 2 || !titles["Drift"] {
		t.Fatalf("monitoring must add the book to the library, monitored: %+v", libBooks)
	}

	// idempotent: a second expand reports nothing new
	rec, out = doJSON(t, h, "POST", "/api/v1/authors/1/expand", nil)
	if rec.Code != http.StatusOK || int(out["added"].(float64)) != 0 {
		t.Fatalf("second expand should add 0: %d %v", rec.Code, out)
	}
}
