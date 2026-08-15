package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/getbooky/booky/internal/metadata"
)

// scopedFixture builds the shape these tests all need: two libraries, an
// admin, and a plain user who may reach only the first one.
type scopedFixture struct {
	srv        *Server
	h          http.Handler
	mine, off  int64 // the user's library, and one they must never see
	adminCooke *http.Cookie
	userCookie *http.Cookie
}

func newScopedFixture(t *testing.T) *scopedFixture {
	t.Helper()
	srv := testServer(t)
	h := srv.Handler()
	f := &scopedFixture{srv: srv, h: h}

	// libraries first — while the API is still open, before any account
	rec, out := doJSON(t, h, "POST", "/api/v1/libraries",
		map[string]string{"name": "Alex", "rootPath": "/data/books/alex"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library: %d %s", rec.Code, rec.Body)
	}
	f.mine = int64(out["id"].(float64))
	rec, out = doJSON(t, h, "POST", "/api/v1/libraries",
		map[string]string{"name": "Private", "rootPath": "/data/books/private"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library: %d %s", rec.Code, rec.Body)
	}
	f.off = int64(out["id"].(float64))

	// first account is forced to admin and turns login on
	rec, _ = doJSON(t, h, "POST", "/api/v1/users", map[string]any{
		"username": "root", "password": "correct horse battery"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create admin: %d %s", rec.Code, rec.Body)
	}
	f.adminCooke = login(t, h, "root", "correct horse battery")

	rec, _ = f.do(t, f.adminCooke, "POST", "/api/v1/users", map[string]any{
		"username": "sam", "password": "another passphrase", "role": "user",
		"libraryIds": []int64{f.mine},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create scoped user: %d %s", rec.Code, rec.Body)
	}
	f.userCookie = login(t, h, "sam", "another passphrase")
	return f
}

func login(t *testing.T, h http.Handler, username, password string) *http.Cookie {
	t.Helper()
	rec, _ := doJSON(t, h, "POST", "/api/v1/auth/login",
		map[string]string{"username": username, "password": password})
	if rec.Code != http.StatusOK {
		t.Fatalf("login %s: %d %s", username, rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatalf("login %s set no cookie", username)
	}
	return cookies[0]
}

// list reads a JSON array field, tolerating the null an empty Go slice
// encodes to.
func list(out map[string]any, key string) []any {
	items, _ := out[key].([]any)
	return items
}

func (f *scopedFixture) do(t *testing.T, c *http.Cookie, method, path string, body any) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	out := map[string]any{}
	json.Unmarshal(rec.Body.Bytes(), &out) //nolint:errcheck,gosec // some responses aren't objects
	return rec, out
}

// A plain user must not reach anything that configures the install or edits
// and deletes what's in it. Each of these was reachable before roles were
// enforced — the whole point of the change.
func TestUserIsLockedOutOfAdminEndpoints(t *testing.T) {
	f := newScopedFixture(t)
	mine := strconv.FormatInt(f.mine, 10)

	forbidden := []struct {
		method, path string
		// adminOnly endpoints that DO something irreversible are only checked
		// from the user's side — calling restart as the admin would take the
		// test binary down with it.
		skipAdminCheck bool
	}{
		{method: "GET", path: "/api/v1/users"},
		{method: "POST", path: "/api/v1/users"},
		{method: "GET", path: "/api/v1/settings/prowlarr_url"},
		{method: "PUT", path: "/api/v1/settings/naming_scheme"},
		{method: "POST", path: "/api/v1/libraries"},
		{method: "DELETE", path: "/api/v1/libraries/" + mine},
		{method: "POST", path: "/api/v1/libraries/" + mine + "/scan"},
		{method: "GET", path: "/api/v1/libraries/" + mine + "/review"},
		{method: "PUT", path: "/api/v1/libraries/" + mine + "/opds"},
		{method: "PUT", path: "/api/v1/libraries/" + mine + "/profile"},
		{method: "GET", path: "/api/v1/system/logs"},
		{method: "GET", path: "/api/v1/system/health"},
		{method: "GET", path: "/api/v1/profiles"},
		{method: "GET", path: "/api/v1/system/browse?path=/"},
		{method: "POST", path: "/api/v1/system/restart", skipAdminCheck: true},
		{method: "POST", path: "/api/v1/system/test/prowlarr"},
		{method: "GET", path: "/api/v1/backups"},
		{method: "POST", path: "/api/v1/backups"},
		{method: "GET", path: "/api/v1/lists"},
		{method: "POST", path: "/api/v1/lists"},
		{method: "PUT", path: "/api/v1/profiles/1"},
	}
	for _, tc := range forbidden {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, map[string]any{})
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", tc.method, tc.path, rec.Code)
		}
		if tc.skipAdminCheck {
			continue
		}
		// the same call must still work for an admin, or the guard is just a
		// broken endpoint rather than an access rule
		rec, _ = f.do(t, f.adminCooke, tc.method, tc.path, map[string]any{})
		if rec.Code == http.StatusForbidden {
			t.Errorf("%s %s: admin got 403 too", tc.method, tc.path)
		}
	}
}

// The everyday work the role exists for has to keep working.
func TestUserCanWorkInTheirLibrary(t *testing.T) {
	f := newScopedFixture(t)
	mine := strconv.FormatInt(f.mine, 10)

	rec, out := f.do(t, f.userCookie, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"}},
		"libraryId": f.mine,
		"monitored": false,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("user must be able to add a book to their library: %d %s", rec.Code, rec.Body)
	}
	bookID := strconv.FormatInt(int64(out["id"].(float64)), 10)

	allowed := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/books/" + bookID, nil},
		{"POST", "/api/v1/books/" + bookID + "/refresh", nil},
		{"PUT", "/api/v1/libraries/" + mine + "/books/" + bookID + "/monitored", map[string]any{"monitored": true}},
		{"POST", "/api/v1/libraries/" + mine + "/refresh", nil},
		{"POST", "/api/v1/libraries/" + mine + "/search", nil},
		{"GET", "/api/v1/search?q=burrow", nil},
		{"GET", "/api/v1/wanted", nil},
		{"GET", "/api/v1/calendar", nil},
		{"GET", "/api/v1/devices", nil},
		{"GET", "/api/v1/settings/user_scopes", nil},
		{"PUT", "/api/v1/settings/user_scopes", map[string]any{"value": "[]"}},
		{"GET", "/api/v1/settings/server_url", nil},
		// a person who can shelve a series has to be able to unshelve it, or
		// they accumulate a mess they can't clear. Files are untouched.
		{"DELETE", "/api/v1/libraries/" + mine + "/books/" + bookID + "?mode=library", nil},
	}
	for _, tc := range allowed {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, tc.body)
		if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, must be allowed for a scoped user", tc.method, tc.path, rec.Code)
		}
	}

	// ...but not the two things the role explicitly withholds
	denied := []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/api/v1/books/" + bookID, map[string]any{"fields": map[string]string{"title": "Nope"}}},
		{"PUT", "/api/v1/books/" + bookID + "/lock", map[string]any{"field": "title", "locked": true}},
		{"PUT", "/api/v1/books/" + bookID + "/cover/custom", map[string]any{"url": "http://example.invalid/x.jpg"}},
		{"DELETE", "/api/v1/libraries/" + mine + "/books/" + bookID + "?mode=file", nil},
		{"DELETE", "/api/v1/libraries/" + mine + "/books/" + bookID + "?mode=block", nil},
	}
	for _, tc := range denied {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403 (metadata edits and deletes are admin-only)", tc.method, tc.path, rec.Code)
		}
	}
}

// A library the user wasn't granted must be invisible, not merely read-only.
func TestOtherLibrariesAreInvisible(t *testing.T) {
	f := newScopedFixture(t)
	off := strconv.FormatInt(f.off, 10)

	// seed a book into the off-limits library as the admin
	rec, out := f.do(t, f.adminCooke, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"}},
		"libraryId": f.off,
		"monitored": false,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed private book: %d %s", rec.Code, rec.Body)
	}
	privateBook := strconv.FormatInt(int64(out["id"].(float64)), 10)

	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/libraries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("libraries: %d", rec.Code)
	}
	libs := list(out, "libraries")
	if len(libs) != 1 {
		t.Fatalf("user must see exactly their one library, got %d", len(libs))
	}
	if int64(libs[0].(map[string]any)["id"].(float64)) != f.mine {
		t.Fatalf("user sees the wrong library: %v", libs[0])
	}

	// the book itself, its cover, its file and its progress are all closed
	for _, path := range []string{
		"/api/v1/books/" + privateBook,
		"/api/v1/covers/" + privateBook,
		"/api/v1/books/" + privateBook + "/file",
		"/api/v1/books/" + privateBook + "/progress",
	} {
		rec, _ := f.do(t, f.userCookie, "GET", path, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("GET %s: got %d, want 403", path, rec.Code)
		}
	}

	// and it never shows up in a list
	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/books", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("books: %d", rec.Code)
	}
	for _, raw := range list(out, "books") {
		b := raw.(map[string]any)
		if int64(b["libraryId"].(float64)) == f.off {
			t.Fatalf("a book from an off-limits library leaked into the list: %v", b)
		}
	}

	// asking for it by id is refused rather than silently widened
	rec, _ = f.do(t, f.userCookie, "GET", "/api/v1/books?libraryId="+off, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("books?libraryId=<other>: got %d, want 403", rec.Code)
	}

	// as are the write paths into it
	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/v1/books", map[string]any{
			"meta":      map[string]any{"provider": "stub", "title": "Drift", "authors": []string{"Emmett Hale"}},
			"libraryId": f.off,
		}},
		{"POST", "/api/v1/libraries/" + off + "/refresh", nil},
		{"POST", "/api/v1/libraries/" + off + "/search", nil},
		{"PUT", "/api/v1/libraries/" + off + "/books/" + privateBook + "/monitored", map[string]any{"monitored": true}},
		{"GET", "/api/v1/books/" + privateBook + "/releases?libraryId=" + off, nil},
	} {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

// A device's token bypasses session auth, so a user must not be able to mint
// one that reaches a library they don't hold.
func TestDevicesAreScopedToTheirOwner(t *testing.T) {
	f := newScopedFixture(t)
	if err := f.srv.Settings.Set("server_url", "http://booky.local:8787"); err != nil {
		t.Fatal(err)
	}

	rec, _ := f.do(t, f.userCookie, "POST", "/api/v1/devices", map[string]any{
		"name": "Sneaky Kobo", "libraryIds": []int64{f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("pairing a device to an off-limits library: got %d, want 403", rec.Code)
	}

	rec, out := f.do(t, f.userCookie, "POST", "/api/v1/devices", map[string]any{
		"name": "Sam's Kobo", "libraryIds": []int64{f.mine},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("pairing a device to their own library: %d %s", rec.Code, rec.Body)
	}
	samDevice := strconv.FormatInt(int64(out["id"].(float64)), 10)

	rec, out = f.do(t, f.adminCooke, "POST", "/api/v1/devices", map[string]any{
		"name": "Admin Kindle", "libraryIds": []int64{f.off},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin device: %d %s", rec.Code, rec.Body)
	}
	adminDevice := strconv.FormatInt(int64(out["id"].(float64)), 10)

	// the user sees only their own
	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("devices: %d", rec.Code)
	}
	devices := list(out, "devices")
	if len(devices) != 1 || devices[0].(map[string]any)["name"] != "Sam's Kobo" {
		t.Fatalf("user must see only their own device, got %v", devices)
	}

	// the admin sees both, labelled with who paired them
	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/devices", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin devices: %d", rec.Code)
	}
	devices = list(out, "devices")
	if len(devices) != 2 {
		t.Fatalf("admin must see every device, got %d", len(devices))
	}
	owners := map[string]string{}
	for _, raw := range devices {
		d := raw.(map[string]any)
		owners[d["name"].(string)], _ = d["ownerName"].(string)
	}
	if owners["Sam's Kobo"] != "sam" || owners["Admin Kindle"] != "root" {
		t.Fatalf("admin needs an owner label to know whose device to revoke: %v", owners)
	}

	// the user can't touch the admin's device, or download its token-bearing zip
	for _, tc := range []struct{ method, path string }{
		{"DELETE", "/api/v1/devices/" + adminDevice},
		{"GET", "/api/v1/devices/" + adminDevice + "/plugin.zip"},
	} {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, nil)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s %s: got %d, want 404", tc.method, tc.path, rec.Code)
		}
	}

	// but the admin can revoke the user's, which is the whole point of seeing it
	rec, _ = f.do(t, f.adminCooke, "DELETE", "/api/v1/devices/"+samDevice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin revoking a user's device: %d %s", rec.Code, rec.Body)
	}
}

// Grants are read per request, so an admin taking a library away lands on the
// user's next click — and cuts off the e-reader they already paired.
func TestRevokingAccessTakesEffectImmediately(t *testing.T) {
	f := newScopedFixture(t)
	mine := strconv.FormatInt(f.mine, 10)

	rec, _ := f.do(t, f.userCookie, "POST", "/api/v1/libraries/"+mine+"/refresh", nil)
	if rec.Code == http.StatusForbidden {
		t.Fatalf("baseline: the user should reach their own library")
	}

	rec, out := f.do(t, f.adminCooke, "GET", "/api/v1/users", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("users: %d", rec.Code)
	}
	var samID int64
	for _, raw := range list(out, "users") {
		u := raw.(map[string]any)
		if u["username"] == "sam" {
			samID = int64(u["id"].(float64))
		}
	}
	if samID == 0 {
		t.Fatal("sam not found")
	}

	rec, _ = f.do(t, f.adminCooke, "PUT", "/api/v1/users/"+strconv.FormatInt(samID, 10)+"/libraries",
		map[string]any{"libraryIds": []int64{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}

	// same session cookie, no re-login
	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/libraries", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("libraries after revoke: %d", rec.Code)
	}
	if libs := list(out, "libraries"); len(libs) != 0 {
		t.Fatalf("revoked user still sees %d libraries", len(libs))
	}
	rec, _ = f.do(t, f.userCookie, "POST", "/api/v1/libraries/"+mine+"/refresh", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("revoked user can still refresh: %d", rec.Code)
	}
}

// Authors and series follow the books: you see them because something of
// theirs sits in a library you hold. These are the cases that rule has to get
// right, including an admin filing a book into someone else's library.
func TestAuthorAndSeriesVisibility(t *testing.T) {
	f := newScopedFixture(t)

	add := func(c *http.Cookie, title string, libID int64) int64 {
		t.Helper()
		rec, out := f.do(t, c, "POST", "/api/v1/books", map[string]any{
			"meta": map[string]any{
				"provider": "stub", "title": title, "authors": []string{"Emmett Hale"},
				"seriesName": "Vault", "seriesIndex": 1,
			},
			"libraryId": libID,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("add %q: %d %s", title, rec.Code, rec.Body)
		}
		return int64(out["id"].(float64))
	}
	authorNames := func(c *http.Cookie) []string {
		t.Helper()
		rec, out := f.do(t, c, "GET", "/api/v1/authors", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("authors: %d %s", rec.Code, rec.Body)
		}
		var names []string
		for _, raw := range list(out, "authors") {
			names = append(names, raw.(map[string]any)["name"].(string))
		}
		return names
	}
	seriesNames := func(c *http.Cookie) []string {
		t.Helper()
		rec, out := f.do(t, c, "GET", "/api/v1/series", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("series: %d %s", rec.Code, rec.Body)
		}
		var names []string
		for _, raw := range list(out, "series") {
			names = append(names, raw.(map[string]any)["name"].(string))
		}
		return names
	}

	// the admin shelves a book in the library the user CANNOT reach
	add(f.adminCooke, "Burrow", f.off)
	if got := authorNames(f.userCookie); len(got) != 0 {
		t.Fatalf("an author reachable only through an off-limits library leaked: %v", got)
	}
	if got := seriesNames(f.userCookie); len(got) != 0 {
		t.Fatalf("a series reachable only through an off-limits library leaked: %v", got)
	}

	// ...and now the admin files one into the USER'S library. Same author,
	// same series — the user must pick both up.
	add(f.adminCooke, "Drift", f.mine)
	if got := authorNames(f.userCookie); len(got) != 1 || got[0] != "Emmett Hale" {
		t.Fatalf("a book added into the user's library must surface its author: %v", got)
	}
	if got := seriesNames(f.userCookie); len(got) != 1 || got[0] != "Vault" {
		t.Fatalf("a book added into the user's library must surface its series: %v", got)
	}

	// the counts narrow too — the user must not be told about the copy they
	// can't open
	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/authors", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("authors: %d", rec.Code)
	}
	author := list(out, "authors")[0].(map[string]any)
	if n := int(author["bookCount"].(float64)); n != 1 {
		t.Errorf("bookCount = %d, want 1 — only the book in the user's library counts", n)
	}
	// the admin still sees both
	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/authors", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin authors: %d", rec.Code)
	}
	author = list(out, "authors")[0].(map[string]any)
	if n := int(author["bookCount"].(float64)); n != 2 {
		t.Errorf("admin bookCount = %d, want 2", n)
	}
}

// "Add author" shelves nothing — the bibliography arrives catalog-only — so
// without remembering who asked, the author would vanish from the page of the
// user who just added them. They must also stay private to that user.
func TestAddedAuthorStaysVisibleToTheAdder(t *testing.T) {
	f := newScopedFixture(t)

	rec, _ := f.do(t, f.userCookie, "POST", "/api/v1/authors", map[string]any{"name": "Emmett Hale"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add author: %d %s", rec.Code, rec.Body)
	}
	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/authors", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("authors: %d", rec.Code)
	}
	authors := list(out, "authors")
	if len(authors) != 1 || authors[0].(map[string]any)["name"] != "Emmett Hale" {
		t.Fatalf("the adder must still see the author they just added: %v", authors)
	}

	// an author added by someone else, with nothing shelved anywhere, is not
	// this user's business
	rec, _ = f.do(t, f.adminCooke, "POST", "/api/v1/authors", map[string]any{"name": "Nora Vale"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin add author: %d %s", rec.Code, rec.Body)
	}
	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/authors", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("authors: %d", rec.Code)
	}
	for _, raw := range list(out, "authors") {
		if raw.(map[string]any)["name"] == "Nora Vale" {
			t.Fatal("an author the admin added, shelved nowhere, leaked to the user")
		}
	}
}

// Acting on an author or series you can't see is refused the same way reading
// one is — otherwise the list filter is decoration.
func TestAuthorAndSeriesActionsAreGuarded(t *testing.T) {
	f := newScopedFixture(t)

	rec, out := f.do(t, f.adminCooke, "POST", "/api/v1/books", map[string]any{
		"meta": map[string]any{
			"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"},
			"seriesName": "Vault", "seriesIndex": 1,
		},
		"libraryId": f.off,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	authorID := strconv.FormatInt(int64(out["authorId"].(float64)), 10)
	seriesID := strconv.FormatInt(int64(out["seriesId"].(float64)), 10)

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/authors/" + authorID + "/photo", nil},
		{"POST", "/api/v1/authors/" + authorID + "/expand", nil},
		{"POST", "/api/v1/authors/" + authorID + "/search", nil},
		{"DELETE", "/api/v1/authors/" + authorID, nil},
		{"GET", "/api/v1/books?authorId=" + authorID, nil},
		{"POST", "/api/v1/series/" + seriesID + "/library", map[string]any{"libraryId": f.mine}},
		{"GET", "/api/v1/books?seriesId=" + seriesID, nil},
	} {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

// Shelving a series names the library, so there is no target to guess and
// nothing to reach past. The old toggle flipped a global flag whose cascade
// hit every library holding any of those books, and whose fallback target was
// "the first library" — which for a scoped user was often one they couldn't
// even see.
func TestAddSeriesToLibraryStaysInTheChosenLibrary(t *testing.T) {
	f := newScopedFixture(t)

	rec, out := f.do(t, f.userCookie, "POST", "/api/v1/books", map[string]any{
		"meta": map[string]any{
			"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"},
			"seriesName": "Vault", "seriesIndex": 1,
		},
		"libraryId": f.mine,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	seriesID := strconv.FormatInt(int64(out["seriesId"].(float64)), 10)

	// a catalog-only sibling, in no library at all
	if _, err := f.srv.db.Exec(`INSERT INTO books (author_id, series_id, series_num, title, description, language, publisher, genres)
		VALUES ((SELECT author_id FROM series WHERE id = ?), ?, 2, 'Drift', '', '', '', '[]')`,
		seriesID, seriesID); err != nil {
		t.Fatal(err)
	}

	// the user can't shelve it into a library they don't hold
	rec, _ = f.do(t, f.userCookie, "POST", "/api/v1/series/"+seriesID+"/library",
		map[string]any{"libraryId": f.off})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("shelving into an off-limits library: got %d, want 403", rec.Code)
	}

	rec, out = f.do(t, f.userCookie, "POST", "/api/v1/series/"+seriesID+"/library",
		map[string]any{"libraryId": f.mine})
	if rec.Code != http.StatusOK {
		t.Fatalf("shelving into their own library: %d %s", rec.Code, rec.Body)
	}
	if added := int(out["added"].(float64)); added != 1 {
		t.Fatalf("added = %d, want 1 (the catalog-only sibling)", added)
	}

	var stray int
	if err := f.srv.db.QueryRow(`SELECT COUNT(*) FROM library_books WHERE library_id = ?`, f.off).Scan(&stray); err != nil {
		t.Fatal(err)
	}
	if stray != 0 {
		t.Fatalf("shelving a series put %d book(s) in a library the user can't reach", stray)
	}
	var landed int
	if err := f.srv.db.QueryRow(`SELECT COUNT(*) FROM library_books WHERE library_id = ? AND monitored = 1`,
		f.mine).Scan(&landed); err != nil {
		t.Fatal(err)
	}
	if landed != 2 {
		t.Fatalf("the series should be shelved monitored in the user's library, got %d book(s)", landed)
	}
}

// The JSON form of manual import names a path on the SERVER and copies it
// into a library, from where it can be downloaded again. Unfenced, that is a
// read-any-file primitive; the fence is not an admin/user distinction, so it
// is checked against the admin, who has the most reach of anyone.
func TestManualImportPathIsFencedEvenForAdmins(t *testing.T) {
	f := newScopedFixture(t)

	rec, out := f.do(t, f.adminCooke, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"}},
		"libraryId": f.mine,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("seed: %d %s", rec.Code, rec.Body)
	}
	bookID := strconv.FormatInt(int64(out["id"].(float64)), 10)

	// a real file, outside the media mount and outside every library root
	outside := filepath.Join(t.TempDir(), "secrets.epub")
	if err := os.WriteFile(outside, []byte("PK\x03\x04not really"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{outside, "/etc/passwd", "/etc/../etc/passwd"} {
		rec, body := f.do(t, f.adminCooke, "POST", "/api/v1/books/"+bookID+"/import",
			map[string]any{"libraryId": f.mine, "path": path})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("import %s: got %d, want 400 — the path is outside the fence", path, rec.Code)
		}
		if msg, _ := body["error"].(string); msg == "" {
			t.Errorf("import %s: refusal should say why", path)
		}
	}

	// a symlink planted inside an allowed root must not tunnel back out
	root := t.TempDir()
	if _, err := f.srv.db.Exec(`UPDATE libraries SET root_path = ? WHERE id = ?`, root, f.mine); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.epub")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rec, _ = f.do(t, f.adminCooke, "POST", "/api/v1/books/"+bookID+"/import",
		map[string]any{"libraryId": f.mine, "path": link})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("import via symlink out of the root: got %d, want 400", rec.Code)
	}

	// and a real file inside the root is still importable — the fence must not
	// break the feature it's protecting
	inside := filepath.Join(root, "burrow.epub")
	if err := os.WriteFile(inside, []byte("PK\x03\x04"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, body := f.do(t, f.adminCooke, "POST", "/api/v1/books/"+bookID+"/import",
		map[string]any{"libraryId": f.mine, "path": inside})
	if msg, _ := body["error"].(string); rec.Code == http.StatusBadRequest && strings.Contains(msg, "outside") {
		t.Errorf("a file inside a library root must be importable, got: %s", msg)
	}

	// non-book extensions are refused by name as well, so the database can't
	// arrive wearing a book's clothes
	db := filepath.Join(root, "booky.db")
	if err := os.WriteFile(db, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, _ = f.do(t, f.adminCooke, "POST", "/api/v1/books/"+bookID+"/import",
		map[string]any{"libraryId": f.mine, "path": db})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("importing a .db: got %d, want 400", rec.Code)
	}
}

// Saved sidebar scopes live in the global key/value settings table but are
// per-person UI state, so each account needs its own slot — otherwise a user
// adding a scope silently rewrites the admin's sidebar.
func TestSavedScopesDoNotClobberEachOther(t *testing.T) {
	f := newScopedFixture(t)

	rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/settings/user_scopes",
		map[string]any{"value": `[{"name":"admin scope"}]`})
	if rec.Code != http.StatusOK {
		t.Fatalf("admin save: %d %s", rec.Code, rec.Body)
	}
	rec, _ = f.do(t, f.userCookie, "PUT", "/api/v1/settings/user_scopes",
		map[string]any{"value": `[{"name":"sam scope"}]`})
	if rec.Code != http.StatusOK {
		t.Fatalf("user save: %d %s", rec.Code, rec.Body)
	}

	rec, out := f.do(t, f.adminCooke, "GET", "/api/v1/settings/user_scopes", nil)
	if rec.Code != http.StatusOK || !strings.Contains(out["value"].(string), "admin scope") {
		t.Fatalf("the user's save overwrote the admin's scopes: %v", out)
	}
	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/settings/user_scopes", nil)
	if rec.Code != http.StatusOK || !strings.Contains(out["value"].(string), "sam scope") {
		t.Fatalf("the user got back someone else's scopes: %v", out)
	}
}

// Health lists every library by name and root path — the one endpoint that
// enumerates them all regardless of the sidebar.
func TestHealthDoesNotEnumerateLibraries(t *testing.T) {
	f := newScopedFixture(t)
	rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/system/health", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("health: got %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "Private") || strings.Contains(rec.Body.String(), "/data/books/private") {
		t.Fatal("health leaked an off-limits library's name or root path")
	}
	rec, _ = f.do(t, f.adminCooke, "GET", "/api/v1/system/health", nil)
	if rec.Code == http.StatusForbidden {
		t.Fatal("admins still need the health panel")
	}
}

// A catalog-only book — a bibliography entry sitting in no library — follows
// its author. An author you can see brings their whole bibliography; one you
// can't must not leak titles through the unfiltered book list.
//
// The fixture is built in SQL rather than through "Add author": that endpoint
// kicks off a bibliography sync in the background, and a hand-written book row
// carrying no identifiers is exactly what the sync's dedupe logic adopts, so
// the test would race its own setup.
func TestCatalogOnlyBooksFollowTheirAuthor(t *testing.T) {
	f := newScopedFixture(t)

	if _, err := f.srv.db.Exec(`INSERT INTO authors (id, name, sort_name) VALUES (7, 'Ghost Writer', 'Writer, Ghost')`); err != nil {
		t.Fatal(err)
	}
	// two books by them, neither in any library yet
	if _, err := f.srv.db.Exec(`INSERT INTO books (id, author_id, title, description, language, publisher, genres)
		VALUES (70, 7, 'Gravel', '', '', '', '[]'), (71, 7, 'Ridges', '', '', '', '[]')`); err != nil {
		t.Fatal(err)
	}

	titles := func(c *http.Cookie) map[string]bool {
		t.Helper()
		rec, out := f.do(t, c, "GET", "/api/v1/books", nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("books: %d %s", rec.Code, rec.Body)
		}
		got := map[string]bool{}
		for _, raw := range list(out, "books") {
			got[raw.(map[string]any)["title"].(string)] = true
		}
		return got
	}

	if titles(f.userCookie)["Gravel"] {
		t.Fatal("a catalog-only book from an author the user can't see leaked into the book list")
	}
	if !titles(f.adminCooke)["Gravel"] {
		t.Fatal("the admin must see every catalog-only book")
	}

	// shelving ONE of their books in the user's library makes the author
	// visible — and the rest of the bibliography with them, which is what the
	// author page is built from
	if _, err := f.srv.db.Exec(`INSERT INTO library_books (library_id, book_id, monitored) VALUES (?, 71, 0)`,
		f.mine); err != nil {
		t.Fatal(err)
	}
	if !titles(f.userCookie)["Gravel"] {
		t.Fatal("a visible author's bibliography must stay whole — the author page depends on it")
	}

	// the same book by an author reachable only through the off-limits
	// library stays hidden
	if _, err := f.srv.db.Exec(`INSERT INTO authors (id, name, sort_name) VALUES (8, 'Private Writer', 'Writer, Private')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.db.Exec(`INSERT INTO books (id, author_id, title, description, language, publisher, genres)
		VALUES (80, 8, 'Hidden Backlist', '', '', '', '[]'), (81, 8, 'Shelved Elsewhere', '', '', '', '[]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.srv.db.Exec(`INSERT INTO library_books (library_id, book_id, monitored) VALUES (?, 81, 0)`,
		f.off); err != nil {
		t.Fatal(err)
	}
	got := titles(f.userCookie)
	if got["Hidden Backlist"] || got["Shelved Elsewhere"] {
		t.Fatalf("an off-limits author's books leaked: %v", got)
	}

	// and the single-book check agrees with the list
	rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/books/80", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET a catalog-only book of an invisible author: got %d, want 403", rec.Code)
	}
	rec, _ = f.do(t, f.userCookie, "GET", "/api/v1/books/70", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("GET a catalog-only book of a visible author: got %d, want 200", rec.Code)
	}
}

// Activity has to narrow in the query, not after it. Both views read a fixed
// number of recent rows, so filtering afterwards would hand a scoped user
// whatever fraction of a busy neighbour's day happened to be theirs — and
// could show them an empty page while their own imports scrolled past just
// out of reach.
func TestActivityIsScopedBeforeTheLimit(t *testing.T) {
	f := newScopedFixture(t)

	bookID, err := f.srv.Catalog.UpsertBook(metadata.BookMeta{
		Provider: "stub", Title: "Burrow", Authors: []string{"Emmett Hale"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// one event of the user's own, then a flood in the library they can't see
	if err := f.srv.Catalog.AddHistory(bookID, f.mine, "added", "the user's own import"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		if err := f.srv.Catalog.AddHistory(bookID, f.off, "added", "someone else's busy day"); err != nil {
			t.Fatal(err)
		}
	}
	// ...and the same shape in the queue
	if _, err := f.srv.db.Exec(`INSERT INTO queue (book_id, library_id, release_title, source, protocol, status)
		VALUES (?, ?, 'Burrow.epub', 'stub', 'directdl', 'done')`, bookID, f.mine); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		if _, err := f.srv.db.Exec(`INSERT INTO queue (book_id, library_id, release_title, source, protocol, status)
			VALUES (?, ?, 'Elsewhere.epub', 'stub', 'directdl', 'done')`, bookID, f.off); err != nil {
			t.Fatal(err)
		}
	}

	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history: %d %s", rec.Code, rec.Body)
	}
	hist := list(out, "history")
	if len(hist) != 1 {
		t.Fatalf("history should hold the user's single event, got %d", len(hist))
	}
	if detail := hist[0].(map[string]any)["detail"].(string); detail != "the user's own import" {
		t.Fatalf("history showed someone else's event: %q", detail)
	}

	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/queue", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", rec.Code, rec.Body)
	}
	queue := list(out, "queue")
	if len(queue) != 1 {
		t.Fatalf("queue should hold the user's single item, got %d", len(queue))
	}
	if title := queue[0].(map[string]any)["releaseTitle"].(string); title != "Burrow.epub" {
		t.Fatalf("queue showed someone else's download: %q", title)
	}

	// the admin still sees the lot (capped by the query's own limits)
	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/history", nil)
	if rec.Code != http.StatusOK || len(list(out, "history")) < 100 {
		t.Fatalf("admin history: %d, %d rows", rec.Code, len(list(out, "history")))
	}
}

// A bibliography sync writes a history row with no library — "3 new book(s) in
// <author>'s bibliography". Once syncs stopped shelving anything, that became
// EVERY such row, so a passthrough for library-less events would have named
// authors a scoped user can't otherwise see.
func TestLibrarylessHistoryStaysWithAdmins(t *testing.T) {
	f := newScopedFixture(t)
	if err := f.srv.Catalog.AddHistory(0, 0, "backlist", "3 new book(s) in Nora Vale's bibliography"); err != nil {
		t.Fatal(err)
	}

	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history: %d", rec.Code)
	}
	for _, raw := range list(out, "history") {
		if strings.Contains(raw.(map[string]any)["detail"].(string), "Nora Vale") {
			t.Fatal("an install-wide sync event named an author the user can't see")
		}
	}

	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin history: %d", rec.Code)
	}
	found := false
	for _, raw := range list(out, "history") {
		if strings.Contains(raw.(map[string]any)["detail"].(string), "Nora Vale") {
			found = true
		}
	}
	if !found {
		t.Fatal("the admin should still see install-wide sync events")
	}
}

// Emptying a library is per-account. If Tommy removes the last book he holds
// by an author, that author and their series leave HIS pages — while anyone
// else still holding one keeps seeing them. The rule is the same one that
// puts them there: it counts only books in libraries you can reach.
func TestEmptyingOneLibraryOnlyClearsThatAccountsPages(t *testing.T) {
	f := newScopedFixture(t)

	// the same series shelved in both libraries
	var bookIDs []int64
	for _, lib := range []int64{f.mine, f.off} {
		rec, out := f.do(t, f.adminCooke, "POST", "/api/v1/books", map[string]any{
			"meta": map[string]any{
				"provider": "stub", "title": "Burrow", "authors": []string{"Emmett Hale"},
				"seriesName": "Vault", "seriesIndex": 1,
			},
			"libraryId": lib,
		})
		if rec.Code != http.StatusCreated {
			t.Fatalf("seed %d: %d %s", lib, rec.Code, rec.Body)
		}
		bookIDs = append(bookIDs, int64(out["id"].(float64)))
	}

	names := func(c *http.Cookie, path, key string) []string {
		t.Helper()
		rec, out := f.do(t, c, "GET", path, nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", path, rec.Code, rec.Body)
		}
		var got []string
		for _, raw := range list(out, key) {
			got = append(got, raw.(map[string]any)["name"].(string))
		}
		return got
	}

	if got := names(f.userCookie, "/api/v1/authors", "authors"); len(got) != 1 {
		t.Fatalf("baseline authors for the user: %v", got)
	}
	if got := names(f.userCookie, "/api/v1/series", "series"); len(got) != 1 {
		t.Fatalf("baseline series for the user: %v", got)
	}

	// the user clears their own copy — files untouched, other library untouched
	rec, _ := f.do(t, f.userCookie, "DELETE",
		"/api/v1/libraries/"+strconv.FormatInt(f.mine, 10)+
			"/books/"+strconv.FormatInt(bookIDs[0], 10)+"?mode=library", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("remove from own library: %d %s", rec.Code, rec.Body)
	}

	if got := names(f.userCookie, "/api/v1/authors", "authors"); len(got) != 0 {
		t.Errorf("author still on the user's page with nothing of theirs shelved: %v", got)
	}
	if got := names(f.userCookie, "/api/v1/series", "series"); len(got) != 0 {
		t.Errorf("series still on the user's page with nothing of theirs shelved: %v", got)
	}

	// the other library still holds a copy, so the admin's pages are unchanged
	if got := names(f.adminCooke, "/api/v1/authors", "authors"); len(got) != 1 {
		t.Errorf("one account clearing its library changed another's authors: %v", got)
	}
	if got := names(f.adminCooke, "/api/v1/series", "series"); len(got) != 1 {
		t.Errorf("one account clearing its library changed another's series: %v", got)
	}
}
