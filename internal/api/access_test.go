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
)

// scopedFixture builds the shape these tests all need: the one library, an
// admin, two shared tags, and a plain user who holds only the first.
type scopedFixture struct {
	srv *Server
	h   http.Handler
	lib int64 // the library every book lives in
	// mine is a tag the user holds; off is one they must never see through
	mine, off  int64
	adminCooke *http.Cookie
	userCookie *http.Cookie
	samID      int64
	samTag     int64 // sam's personal tag
}

func newScopedFixture(t *testing.T) *scopedFixture {
	t.Helper()
	srv := testServer(t)
	h := srv.Handler()
	f := &scopedFixture{srv: srv, h: h}

	// the library first — while the API is still open, before any account
	rec, out := doJSON(t, h, "POST", "/api/v1/libraries",
		map[string]string{"name": "Library", "rootPath": "/data/books"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create library: %d %s", rec.Code, rec.Body)
	}
	f.lib = int64(out["id"].(float64))

	// first account is forced to admin and turns login on
	rec, _ = doJSON(t, h, "POST", "/api/v1/users", map[string]any{
		"username": "root", "password": "correct horse battery"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create admin: %d %s", rec.Code, rec.Body)
	}
	f.adminCooke = login(t, h, "root", "correct horse battery")

	f.mine = f.createTag(t, "Alex")
	f.off = f.createTag(t, "Private")

	rec, out = f.do(t, f.adminCooke, "POST", "/api/v1/users", map[string]any{
		"username": "sam", "password": "another passphrase", "role": "user",
		"tagIds": []int64{f.mine},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create scoped user: %d %s", rec.Code, rec.Body)
	}
	f.samID = int64(out["id"].(float64))
	f.userCookie = login(t, h, "sam", "another passphrase")
	f.samTag = srv.Auth.UserByID(f.samID).PersonalTag
	return f
}

func (f *scopedFixture) createTag(t *testing.T, name string) int64 {
	t.Helper()
	rec, out := f.do(t, f.adminCooke, "POST", "/api/v1/tags", map[string]any{"name": name})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create tag %s: %d %s", name, rec.Code, rec.Body)
	}
	return int64(out["id"].(float64))
}

// addBook adds a book through the API as c, shared with tags (nil lets the
// server pick: admin-only for an admin, the personal tag for a user).
func (f *scopedFixture) addBook(t *testing.T, c *http.Cookie, meta map[string]any, tags []int64) map[string]any {
	t.Helper()
	if _, ok := meta["provider"]; !ok {
		meta["provider"] = "stub"
	}
	if _, ok := meta["authors"]; !ok {
		meta["authors"] = []string{"Emmett Hale"}
	}
	body := map[string]any{"meta": meta, "libraryId": f.lib}
	if tags != nil {
		body["tagIds"] = tags
	}
	rec, out := f.do(t, c, "POST", "/api/v1/books", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add %v: %d %s", meta["title"], rec.Code, rec.Body)
	}
	return out
}

func idStr(v any) string { return strconv.FormatInt(int64(v.(float64)), 10) }

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

// A plain user must not reach anything that configures the install, edits
// or deletes what's in it, or decides who sees what.
func TestUserIsLockedOutOfAdminEndpoints(t *testing.T) {
	f := newScopedFixture(t)
	lib := strconv.FormatInt(f.lib, 10)
	book := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow"}, []int64{f.mine})["id"])
	off := strconv.FormatInt(f.off, 10)
	sam := strconv.FormatInt(f.samID, 10)

	forbidden := []struct {
		method, path string
		// adminOnly endpoints that DO something irreversible are only checked
		// from the user's side — calling restart as the admin would take the
		// test binary down with it.
		skipAdminCheck bool
	}{
		{method: "GET", path: "/api/v1/users"},
		{method: "POST", path: "/api/v1/users"},
		{method: "PUT", path: "/api/v1/users/" + sam + "/tags"},
		{method: "GET", path: "/api/v1/settings/prowlarr_url"},
		{method: "PUT", path: "/api/v1/settings/naming_scheme"},
		{method: "POST", path: "/api/v1/libraries"},
		{method: "DELETE", path: "/api/v1/libraries/" + lib},
		{method: "POST", path: "/api/v1/libraries/" + lib + "/scan"},
		{method: "POST", path: "/api/v1/libraries/" + lib + "/refresh", skipAdminCheck: true},
		{method: "GET", path: "/api/v1/libraries/" + lib + "/review"},
		{method: "PUT", path: "/api/v1/libraries/" + lib + "/profile"},
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
		// sharing is the admin's call, end to end
		{method: "POST", path: "/api/v1/tags"},
		{method: "PATCH", path: "/api/v1/tags/" + off},
		{method: "PUT", path: "/api/v1/tags/" + off + "/members"},
		{method: "DELETE", path: "/api/v1/tags/" + off, skipAdminCheck: true},
		{method: "PUT", path: "/api/v1/books/" + book + "/tags"},
		{method: "POST", path: "/api/v1/tags/bulk"},
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
func TestUserCanWorkOnTheirShelf(t *testing.T) {
	f := newScopedFixture(t)
	lib := strconv.FormatInt(f.lib, 10)

	out := f.addBook(t, f.userCookie, map[string]any{"title": "Burrow"}, nil)
	bookID := idStr(out["id"])
	// what a user adds for themselves lands on their personal tag
	if tags := out["tagIds"].([]any); len(tags) != 1 || int64(tags[0].(float64)) != f.samTag {
		t.Fatalf("a user's own add must carry their personal tag, got %v", tags)
	}

	allowed := []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/books/" + bookID, nil},
		{"POST", "/api/v1/books/" + bookID + "/refresh", nil},
		{"PUT", "/api/v1/libraries/" + lib + "/books/" + bookID + "/monitored", map[string]any{"monitored": true}},
		{"POST", "/api/v1/libraries/" + lib + "/search", nil},
		{"GET", "/api/v1/search?q=burrow", nil},
		{"GET", "/api/v1/wanted", nil},
		{"GET", "/api/v1/calendar", nil},
		{"GET", "/api/v1/devices", nil},
		{"GET", "/api/v1/tags", nil},
		{"GET", "/api/v1/auth/opds", nil},
		{"GET", "/api/v1/settings/user_scopes", nil},
		{"PUT", "/api/v1/settings/user_scopes", map[string]any{"value": "[]"}},
		{"GET", "/api/v1/settings/server_url", nil},
		// a person who can add a book has to be able to unadd it
		{"DELETE", "/api/v1/libraries/" + lib + "/books/" + bookID + "?mode=library", nil},
	}
	for _, tc := range allowed {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, tc.body)
		if rec.Code == http.StatusForbidden || rec.Code == http.StatusUnauthorized {
			t.Errorf("%s %s: got %d, must be allowed for a scoped user", tc.method, tc.path, rec.Code)
		}
	}

	// ...but not what the role explicitly withholds
	bookID = idStr(f.addBook(t, f.userCookie, map[string]any{"title": "Drift"}, nil)["id"])
	denied := []struct {
		method, path string
		body         any
	}{
		{"PATCH", "/api/v1/books/" + bookID, map[string]any{"fields": map[string]string{"title": "Nope"}}},
		{"PUT", "/api/v1/books/" + bookID + "/lock", map[string]any{"field": "title", "locked": true}},
		{"PUT", "/api/v1/books/" + bookID + "/cover/custom", map[string]any{"url": "http://example.invalid/x.jpg"}},
		{"DELETE", "/api/v1/libraries/" + lib + "/books/" + bookID + "?mode=file", nil},
		{"DELETE", "/api/v1/libraries/" + lib + "/books/" + bookID + "?mode=block", nil},
	}
	for _, tc := range denied {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403 (metadata edits and deletes are admin-only)", tc.method, tc.path, rec.Code)
		}
	}
}

// A book carrying none of the user's tags must be invisible, not merely
// read-only — whether it's shared through a tag they don't hold or carries no
// tag at all (admin-only).
func TestBooksOutsideYourTagsAreInvisible(t *testing.T) {
	f := newScopedFixture(t)
	lib := strconv.FormatInt(f.lib, 10)

	private := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow"}, []int64{f.off})["id"])
	untagged := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Haze"}, nil)["id"])

	for _, book := range []string{private, untagged} {
		// the book itself, its cover, its file and its progress are all closed
		for _, path := range []string{
			"/api/v1/books/" + book,
			"/api/v1/covers/" + book,
			"/api/v1/books/" + book + "/file",
			"/api/v1/books/" + book + "/progress",
			"/api/v1/books/" + book + "/releases?libraryId=" + lib,
		} {
			rec, _ := f.do(t, f.userCookie, "GET", path, nil)
			if rec.Code != http.StatusForbidden {
				t.Errorf("GET %s: got %d, want 403", path, rec.Code)
			}
		}
		rec, _ := f.do(t, f.userCookie, "PUT", "/api/v1/libraries/"+lib+"/books/"+book+"/monitored",
			map[string]any{"monitored": true})
		if rec.Code != http.StatusForbidden {
			t.Errorf("monitor %s: got %d, want 403", book, rec.Code)
		}
		rec, _ = f.do(t, f.userCookie, "POST", "/api/v1/books/"+book+"/autograb?libraryId="+lib, nil)
		if rec.Code != http.StatusForbidden {
			t.Errorf("autograb %s: got %d, want 403", book, rec.Code)
		}
	}

	// and they never show up in a list
	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/books", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("books: %d", rec.Code)
	}
	for _, raw := range list(out, "books") {
		if title := raw.(map[string]any)["title"]; title == "Burrow" || title == "Haze" {
			t.Fatalf("a book outside the user's tags leaked into the list: %v", raw)
		}
	}

	// a user can't hand a book a tag they don't hold
	rec, _ = f.do(t, f.userCookie, "POST", "/api/v1/books", map[string]any{
		"meta":   map[string]any{"provider": "stub", "title": "Drift", "authors": []string{"Emmett Hale"}},
		"tagIds": []int64{f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Errorf("adding a book with a tag the user doesn't hold: got %d, want 403", rec.Code)
	}

	// search doesn't advertise what's on other people's shelves
	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/search?q=burrow", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("search: %d", rec.Code)
	}
	for _, raw := range list(out, "results") {
		if raw.(map[string]any)["inLibrary"] == true {
			t.Fatal("search told the user a book outside their tags is in the library")
		}
	}
}

// Which other audiences a book reaches — another person's personal tag above
// all — is the admin's business. A user sees only the tags they hold.
func TestOtherPeoplesTagsAreHidden(t *testing.T) {
	f := newScopedFixture(t)
	book := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow"}, []int64{f.mine, f.off})["id"])

	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/books/"+book, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("book: %d %s", rec.Code, rec.Body)
	}
	tags := out["tagIds"].([]any)
	if len(tags) != 1 || int64(tags[0].(float64)) != f.mine {
		t.Fatalf("user sees tags %v, want only the one they hold", tags)
	}
	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/tags", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tags: %d", rec.Code)
	}
	for _, raw := range list(out, "tags") {
		tg := raw.(map[string]any)
		if int64(tg["id"].(float64)) == f.off {
			t.Fatal("the tag list showed a tag the user doesn't hold")
		}
		if members, _ := tg["memberIds"].([]any); len(members) > 0 {
			t.Fatal("a user must not see who else holds a tag")
		}
	}
	// the admin sees both
	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/books/"+book, nil)
	if rec.Code != http.StatusOK || len(out["tagIds"].([]any)) != 2 {
		t.Fatalf("admin tags: %d %v", rec.Code, out["tagIds"])
	}
}

// A device's token bypasses session auth, so a user must not be able to mint
// one that reaches a tag they don't hold.
func TestDevicesAreScopedToTheirOwner(t *testing.T) {
	f := newScopedFixture(t)
	if err := f.srv.Settings.Set("server_url", "http://booky.local:8787"); err != nil {
		t.Fatal(err)
	}

	rec, _ := f.do(t, f.userCookie, "POST", "/api/v1/devices", map[string]any{
		"name": "Sneaky Kobo", "tagIds": []int64{f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("pairing a device to a tag the user doesn't hold: got %d, want 403", rec.Code)
	}

	rec, out := f.do(t, f.userCookie, "POST", "/api/v1/devices", map[string]any{
		"name": "Sam's Kobo", "tagIds": []int64{f.mine, f.samTag},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("pairing a device to their own tags: %d %s", rec.Code, rec.Body)
	}
	samDevice := idStr(out["id"])

	rec, out = f.do(t, f.adminCooke, "POST", "/api/v1/devices", map[string]any{
		"name": "Admin Kindle", "tagIds": []int64{f.off},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin device: %d %s", rec.Code, rec.Body)
	}
	adminDevice := idStr(out["id"])

	// an admin editing sam's device can't widen it past sam
	rec, _ = f.do(t, f.adminCooke, "PUT", "/api/v1/devices/"+samDevice, map[string]any{
		"name": "Sam's Kobo", "tagIds": []int64{f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin widening a user's device past the user: got %d, want 403", rec.Code)
	}

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

// Tags are read per request, so an admin taking one away lands on the user's
// next click — same session, no re-login.
func TestRevokingATagTakesEffectImmediately(t *testing.T) {
	f := newScopedFixture(t)
	book := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow"}, []int64{f.mine})["id"])

	rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/books/"+book, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("baseline: the user should see a book shared with their tag, got %d", rec.Code)
	}

	rec, _ = f.do(t, f.adminCooke, "PUT", "/api/v1/users/"+strconv.FormatInt(f.samID, 10)+"/tags",
		map[string]any{"tagIds": []int64{}})
	if rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}

	rec, _ = f.do(t, f.userCookie, "GET", "/api/v1/books/"+book, nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("revoked user still opens the book: %d", rec.Code)
	}
	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/books", nil)
	if rec.Code != http.StatusOK || len(list(out, "books")) != 0 {
		t.Fatalf("revoked user still lists books: %v", out)
	}
	// the personal tag survives a revoke — it's the account's own
	if u := f.srv.Auth.UserByID(f.samID); !u.HasTag(f.samTag) {
		t.Fatal("revoking shared tags took the personal tag with it")
	}
}

// Authors and series follow the books: you see them because something of
// theirs carries one of your tags.
func TestAuthorAndSeriesVisibility(t *testing.T) {
	f := newScopedFixture(t)
	vault := map[string]any{"seriesName": "Vault", "seriesIndex": 1}
	meta := func(title string) map[string]any {
		m := map[string]any{"title": title}
		for k, v := range vault {
			m[k] = v
		}
		return m
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

	f.addBook(t, f.adminCooke, meta("Burrow"), []int64{f.off})
	if got := names(f.userCookie, "/api/v1/authors", "authors"); len(got) != 0 {
		t.Fatalf("an author reachable only through someone else's tag leaked: %v", got)
	}
	if got := names(f.userCookie, "/api/v1/series", "series"); len(got) != 0 {
		t.Fatalf("a series reachable only through someone else's tag leaked: %v", got)
	}

	f.addBook(t, f.adminCooke, meta("Drift"), []int64{f.mine})
	if got := names(f.userCookie, "/api/v1/authors", "authors"); len(got) != 1 || got[0] != "Emmett Hale" {
		t.Fatalf("a book shared with the user must surface its author: %v", got)
	}
	if got := names(f.userCookie, "/api/v1/series", "series"); len(got) != 1 || got[0] != "Vault" {
		t.Fatalf("a book shared with the user must surface its series: %v", got)
	}

	// the counts narrow too
	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/authors", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("authors: %d", rec.Code)
	}
	if n := int(list(out, "authors")[0].(map[string]any)["bookCount"].(float64)); n != 1 {
		t.Errorf("bookCount = %d, want 1 — only the book shared with the user counts", n)
	}
	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/authors", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin authors: %d", rec.Code)
	}
	if n := int(list(out, "authors")[0].(map[string]any)["bookCount"].(float64)); n != 2 {
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
	out := f.addBook(t, f.adminCooke, map[string]any{
		"title": "Burrow", "seriesName": "Vault", "seriesIndex": 1,
	}, []int64{f.off})
	authorID := idStr(out["authorId"])
	seriesID := idStr(out["seriesId"])

	for _, tc := range []struct {
		method, path string
		body         any
	}{
		{"GET", "/api/v1/authors/" + authorID + "/photo", nil},
		{"POST", "/api/v1/authors/" + authorID + "/expand", nil},
		{"POST", "/api/v1/authors/" + authorID + "/search", nil},
		{"DELETE", "/api/v1/authors/" + authorID, nil},
		{"GET", "/api/v1/books?authorId=" + authorID, nil},
		{"POST", "/api/v1/series/" + seriesID + "/library", map[string]any{"libraryId": f.lib}},
		{"GET", "/api/v1/books?seriesId=" + seriesID, nil},
	} {
		rec, _ := f.do(t, f.userCookie, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: got %d, want 403", tc.method, tc.path, rec.Code)
		}
	}
}

// Shelving a series shares its new entries with whoever the series already
// reaches, plus the person shelving it — and never with anyone else.
func TestShelvingASeriesKeepsItsAudience(t *testing.T) {
	f := newScopedFixture(t)
	out := f.addBook(t, f.adminCooke, map[string]any{
		"title": "Burrow", "seriesName": "Vault", "seriesIndex": 1,
	}, []int64{f.mine})
	seriesID := idStr(out["seriesId"])

	// a catalog-only sibling, in no library at all
	if _, err := f.srv.db.Exec(`INSERT INTO books (id, author_id, series_id, series_num, title, description, language, publisher, genres)
		VALUES (500, (SELECT author_id FROM series WHERE id = ?), ?, 2, 'Drift', '', '', '', '[]')`,
		seriesID, seriesID); err != nil {
		t.Fatal(err)
	}

	rec, out := f.do(t, f.userCookie, "POST", "/api/v1/series/"+seriesID+"/library",
		map[string]any{"libraryId": f.lib})
	if rec.Code != http.StatusOK {
		t.Fatalf("shelving the series: %d %s", rec.Code, rec.Body)
	}
	if added := int(out["added"].(float64)); added != 1 {
		t.Fatalf("added = %d, want 1 (the catalog-only sibling)", added)
	}
	tags, err := f.srv.Catalog.BookTags(500)
	if err != nil {
		t.Fatal(err)
	}
	if len(tags) != 1 || tags[0] != f.mine {
		t.Fatalf("the new entry must inherit the series' audience and nothing else, got %v", tags)
	}
	rec, _ = f.do(t, f.userCookie, "GET", "/api/v1/books/500", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("the user can't open the entry they just shelved: %d", rec.Code)
	}
}

// The JSON form of manual import names a path on the SERVER and copies it
// into the library, from where it can be downloaded again. Unfenced, that is a
// read-any-file primitive; the fence is not an admin/user distinction, so it
// is checked against the admin, who has the most reach of anyone.
func TestManualImportPathIsFencedEvenForAdmins(t *testing.T) {
	f := newScopedFixture(t)
	bookID := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow"}, []int64{f.mine})["id"])

	outside := filepath.Join(t.TempDir(), "secrets.epub")
	if err := os.WriteFile(outside, []byte("PK\x03\x04not really"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, "/etc/passwd", "/etc/../etc/passwd"} {
		rec, body := f.do(t, f.adminCooke, "POST", "/api/v1/books/"+bookID+"/import",
			map[string]any{"libraryId": f.lib, "path": path})
		if rec.Code != http.StatusBadRequest {
			t.Errorf("import %s: got %d, want 400 — the path is outside the fence", path, rec.Code)
		}
		if msg, _ := body["error"].(string); msg == "" {
			t.Errorf("import %s: refusal should say why", path)
		}
	}

	// a symlink planted inside an allowed root must not tunnel back out
	root := t.TempDir()
	f.srv.mediaRoot = root
	if _, err := f.srv.db.Exec(`UPDATE libraries SET root_path = ? WHERE id = ?`, root, f.lib); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "innocent.epub")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	rec, _ := f.do(t, f.adminCooke, "POST", "/api/v1/books/"+bookID+"/import",
		map[string]any{"libraryId": f.lib, "path": link})
	if rec.Code != http.StatusBadRequest {
		t.Errorf("import via symlink out of the root: got %d, want 400", rec.Code)
	}

	inside := filepath.Join(root, "burrow.epub")
	if err := os.WriteFile(inside, []byte("PK\x03\x04"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, body := f.do(t, f.adminCooke, "POST", "/api/v1/books/"+bookID+"/import",
		map[string]any{"libraryId": f.lib, "path": inside})
	if msg, _ := body["error"].(string); rec.Code == http.StatusBadRequest && strings.Contains(msg, "outside") {
		t.Errorf("a file inside the library root must be importable, got: %s", msg)
	}

	db := filepath.Join(root, "booky.db")
	if err := os.WriteFile(db, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	rec, _ = f.do(t, f.adminCooke, "POST", "/api/v1/books/"+bookID+"/import",
		map[string]any{"libraryId": f.lib, "path": db})
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

// Health names the library and its root path — admin-only.
func TestHealthIsAdminOnly(t *testing.T) {
	f := newScopedFixture(t)
	rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/system/health", nil)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("health: got %d, want 403", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "/data/books") {
		t.Fatal("health leaked the library root")
	}
	rec, _ = f.do(t, f.adminCooke, "GET", "/api/v1/system/health", nil)
	if rec.Code == http.StatusForbidden {
		t.Fatal("admins still need the health panel")
	}
}

// A catalog-only book — a bibliography entry nobody has shelved — follows its
// author. An author you can see brings their whole bibliography; one you can't
// must not leak titles. Following an author is for browsing only: there is no
// file to fetch.
func TestCatalogOnlyBooksFollowTheirAuthor(t *testing.T) {
	f := newScopedFixture(t)
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := f.srv.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO authors (id, name, sort_name) VALUES (7, 'Ghost Writer', 'Writer, Ghost')`)
	exec(`INSERT INTO books (id, author_id, title, description, language, publisher, genres)
		VALUES (70, 7, 'Gravel', '', '', '', '[]'), (71, 7, 'Ridges', '', '', '', '[]')`)

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

	// shelving ONE of their books for the user makes the author visible —
	// and the rest of the bibliography with them
	exec(`INSERT INTO library_books (library_id, book_id, monitored) VALUES (?, 71, 0)`, f.lib)
	exec(`INSERT INTO book_tags (book_id, tag_id) VALUES (71, ?)`, f.mine)
	if !titles(f.userCookie)["Gravel"] {
		t.Fatal("a visible author's bibliography must stay whole — the author page depends on it")
	}

	exec(`INSERT INTO authors (id, name, sort_name) VALUES (8, 'Private Writer', 'Writer, Private')`)
	exec(`INSERT INTO books (id, author_id, title, description, language, publisher, genres)
		VALUES (80, 8, 'Hidden Backlist', '', '', '', '[]'), (81, 8, 'Shelved Elsewhere', '', '', '', '[]')`)
	exec(`INSERT INTO library_books (library_id, book_id, monitored) VALUES (?, 81, 0)`, f.lib)
	exec(`INSERT INTO book_tags (book_id, tag_id) VALUES (81, ?)`, f.off)
	got := titles(f.userCookie)
	if got["Hidden Backlist"] || got["Shelved Elsewhere"] {
		t.Fatalf("an off-limits author's books leaked: %v", got)
	}

	rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/books/80", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET a catalog-only book of an invisible author: got %d, want 403", rec.Code)
	}
	rec, _ = f.do(t, f.userCookie, "GET", "/api/v1/books/70", nil)
	if rec.Code != http.StatusOK {
		t.Errorf("GET a catalog-only book of a visible author: got %d, want 200", rec.Code)
	}
	// browsing it is fine; there's no file to give
	rec, _ = f.do(t, f.userCookie, "GET", "/api/v1/books/70/file", nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("GET the file of a catalog-only book: got %d, want 403", rec.Code)
	}
}

// Activity has to narrow in the query, not after it. Both views read a fixed
// number of recent rows, so filtering afterwards would hand a scoped user
// whatever fraction of a busy neighbour's day happened to be theirs.
func TestActivityIsScopedBeforeTheLimit(t *testing.T) {
	f := newScopedFixture(t)
	mine := int64(f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow"}, []int64{f.mine})["id"].(float64))
	theirs := int64(f.addBook(t, f.adminCooke, map[string]any{"title": "Haze"}, []int64{f.off})["id"].(float64))

	if err := f.srv.Catalog.AddHistory(mine, f.lib, "imported", "the user's own import"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		if err := f.srv.Catalog.AddHistory(theirs, f.lib, "imported", "someone else's busy day"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.srv.db.Exec(`INSERT INTO queue (book_id, library_id, release_title, source, protocol, status)
		VALUES (?, ?, 'Burrow.epub', 'stub', 'directdl', 'done')`, mine, f.lib); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 250; i++ {
		if _, err := f.srv.db.Exec(`INSERT INTO queue (book_id, library_id, release_title, source, protocol, status)
			VALUES (?, ?, 'Elsewhere.epub', 'stub', 'directdl', 'done')`, theirs, f.lib); err != nil {
			t.Fatal(err)
		}
	}

	rec, out := f.do(t, f.userCookie, "GET", "/api/v1/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("history: %d %s", rec.Code, rec.Body)
	}
	for _, raw := range list(out, "history") {
		if detail := raw.(map[string]any)["detail"].(string); strings.Contains(detail, "busy day") || strings.Contains(detail, "Haze") {
			t.Fatalf("history showed someone else's event: %q", detail)
		}
	}
	found := false
	for _, raw := range list(out, "history") {
		if raw.(map[string]any)["detail"] == "the user's own import" {
			found = true
		}
	}
	if !found {
		t.Fatal("the user's own event scrolled out behind someone else's")
	}

	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/queue", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("queue: %d %s", rec.Code, rec.Body)
	}
	queue := list(out, "queue")
	if len(queue) != 1 || queue[0].(map[string]any)["releaseTitle"] != "Burrow.epub" {
		t.Fatalf("queue should hold the user's single item, got %v", queue)
	}

	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/history", nil)
	if rec.Code != http.StatusOK || len(list(out, "history")) < 100 {
		t.Fatalf("admin history: %d, %d rows", rec.Code, len(list(out, "history")))
	}
}

// A bibliography sync writes a history row with no book — "3 new book(s) in
// <author>'s bibliography". It must stay with admins.
func TestBooklessHistoryStaysWithAdmins(t *testing.T) {
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

// "Remove" for a plain user means off MY shelf: their personal tag comes off,
// and nothing else changes — not the book, not its file, not anyone else's
// access. A book that still reaches them through a shared tag stays, and the
// answer says why.
func TestRemovingOnlyTakesItOffYourShelf(t *testing.T) {
	f := newScopedFixture(t)
	lib := strconv.FormatInt(f.lib, 10)

	own := idStr(f.addBook(t, f.userCookie, map[string]any{"title": "Burrow", "seriesName": "Vault", "seriesIndex": 1}, nil)["id"])
	shared := idStr(f.addBook(t, f.adminCooke, map[string]any{"title": "Haze", "authors": []string{"Nora Vale"}}, []int64{f.mine, f.off})["id"])

	rec, out := f.do(t, f.userCookie, "DELETE", "/api/v1/libraries/"+lib+"/books/"+own+"?mode=library", nil)
	if rec.Code != http.StatusOK || out["status"] != "removed" {
		t.Fatalf("remove own: %d %v", rec.Code, out)
	}
	rec, _ = f.do(t, f.userCookie, "GET", "/api/v1/books/"+own, nil)
	if rec.Code != http.StatusForbidden {
		t.Errorf("a book taken off the user's shelf is still open to them: %d", rec.Code)
	}
	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/series", nil)
	if rec.Code != http.StatusOK || len(list(out, "series")) != 0 {
		t.Errorf("series still on the user's page with nothing of theirs shelved: %v", out)
	}
	// still in the library for the admin — a user's remove deletes nothing
	var n int
	if err := f.srv.db.QueryRow(`SELECT COUNT(*) FROM library_books WHERE book_id = ?`, own).Scan(&n); err != nil || n != 1 {
		t.Errorf("a user's remove took the book out of the library: %d %v", n, err)
	}

	// a shared book can't be unshared by a user
	rec, out = f.do(t, f.userCookie, "DELETE", "/api/v1/libraries/"+lib+"/books/"+shared+"?mode=library", nil)
	if rec.Code != http.StatusOK || out["status"] != "shared" {
		t.Fatalf("remove shared: %d %v", rec.Code, out)
	}
	tags, _ := f.srv.Catalog.BookTags(int64(mustAtoi(t, shared)))
	if len(tags) != 2 {
		t.Errorf("a user's remove changed a shared book's tags: %v", tags)
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}
