package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbooky/booky/internal/release"
)

// Every response tells the browser what the page may do; the line that
// matters is script-src 'self', which keeps a script inside an EPUB (shown
// in a same-origin frame by the web reader) from running.
func TestSecurityHeaders(t *testing.T) {
	h := testServer(t).Handler()
	for _, path := range []string{"/", "/api/v1/auth/me", "/assets/nope.js"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "script-src 'self';") || strings.Contains(csp, "unsafe-eval") ||
			!strings.Contains(csp, "object-src 'none'") || !strings.Contains(csp, "frame-ancestors 'self'") {
			t.Errorf("%s: CSP = %q", path, csp)
		}
		// no inline or blob scripts, ever
		for _, part := range strings.Split(csp, ";") {
			if strings.HasPrefix(strings.TrimSpace(part), "script-src") && strings.TrimSpace(part) != "script-src 'self'" {
				t.Errorf("%s: %q", path, part)
			}
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: no nosniff", path)
		}
		if rec.Header().Get("X-Frame-Options") == "" || rec.Header().Get("Referrer-Policy") == "" {
			t.Errorf("%s: frame/referrer headers missing", path)
		}
	}
	// API answers aren't cached
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/api/v1/auth/me", nil))
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Errorf("API Cache-Control = %q", rec.Header().Get("Cache-Control"))
	}
	// HSTS only over HTTPS
	if rec.Header().Get("Strict-Transport-Security") != "" {
		t.Error("HSTS sent over plain HTTP")
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "172.17.0.1:5000" // a reverse proxy on the Docker network
	req.Header.Set("X-Forwarded-Proto", "https")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Header().Get("Strict-Transport-Security") == "" {
		t.Error("no HSTS behind an HTTPS proxy")
	}
}

// A browser on another site — another app on a sibling subdomain, say —
// can't make changes with the session cookie.
func TestCrossSiteWritesAreRefused(t *testing.T) {
	f := newScopedFixture(t)
	// refused reports whether the same-origin rule turned the request away
	// (rather than anything further in answering it)
	refused := func(headers map[string]string) bool {
		req := httptest.NewRequest("PUT", "/api/v1/settings/user_scopes", strings.NewReader(`{"value":"[]"}`))
		req.Host = "booky.example.com"
		req.AddCookie(f.userCookie)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		rec := httptest.NewRecorder()
		f.h.ServeHTTP(rec, req)
		return rec.Code == http.StatusForbidden && strings.Contains(rec.Body.String(), "another site")
	}
	for name, tc := range map[string]struct {
		headers map[string]string
		want    bool
	}{
		"same origin":            {map[string]string{"Origin": "https://booky.example.com"}, false},
		"no origin (curl, apps)": {nil, false},
		"sibling subdomain":      {map[string]string{"Origin": "https://sab.example.com"}, true},
		"another site":           {map[string]string{"Origin": "https://evil.example"}, true},
		"opaque origin":          {map[string]string{"Origin": "null"}, true},
		"fetch metadata only":    {map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		"fetch metadata same":    {map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
	} {
		if got := refused(tc.headers); got != tc.want {
			t.Errorf("%s: refused = %v, want %v", name, got, tc.want)
		}
	}
	// reads are never affected
	req := httptest.NewRequest("GET", "/api/v1/auth/me", nil)
	req.Header.Set("Origin", "https://evil.example")
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != 200 {
		t.Errorf("cross-site GET: %d", rec.Code)
	}
	// the e-reader plugin has its own credentials and sends no cookie
	req = httptest.NewRequest("PUT", "/api/koreader/v1/readalong/1/1/position", strings.NewReader("{}"))
	req.Header.Set("Origin", "https://evil.example")
	rec = httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code == 403 && strings.Contains(rec.Body.String(), "another site") {
		t.Error("device API held to the browser rule")
	}
}

// The address a request comes from: the peer, unless the peer is a trusted
// proxy — then the last address in X-Forwarded-For that isn't one.
func TestClientIP(t *testing.T) {
	t.Setenv("BOOKY_TRUSTED_PROXIES", "")
	proxies := loadTrustedProxies()
	for name, tc := range map[string]struct{ peer, xff, want string }{
		"direct from the internet":            {"203.0.113.9:4000", "", "203.0.113.9"},
		"direct, claiming to be someone else": {"203.0.113.9:4000", "1.2.3.4", "203.0.113.9"},
		"through a proxy":                     {"172.17.0.1:4000", "198.51.100.7", "198.51.100.7"},
		"client forged a prefix":              {"172.17.0.1:4000", "1.2.3.4, 198.51.100.7", "198.51.100.7"},
		"two proxies":                         {"127.0.0.1:4000", "198.51.100.7, 10.0.0.2", "198.51.100.7"},
		"proxy, no header":                    {"172.17.0.1:4000", "", "172.17.0.1"},
		"proxy, garbage header":               {"172.17.0.1:4000", "not-an-ip", "172.17.0.1"},
		"ipv6 client":                         {"[::1]:4000", "2001:db8::1", "2001:db8::1"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		if tc.xff != "" {
			r.Header.Set("X-Forwarded-For", tc.xff)
		}
		if got := proxies.clientIP(r); got != tc.want {
			t.Errorf("%s: %q, want %q", name, got, tc.want)
		}
	}
	t.Setenv("BOOKY_TRUSTED_PROXIES", "none")
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.17.0.1:4000"
	r.Header.Set("X-Forwarded-For", "198.51.100.7")
	if got := loadTrustedProxies().clientIP(r); got != "172.17.0.1" {
		t.Errorf("none: %q", got)
	}
	t.Setenv("BOOKY_TRUSTED_PROXIES", "192.0.2.10, 198.18.0.0/15, junk")
	only := loadTrustedProxies()
	r.RemoteAddr = "192.0.2.10:1"
	if got := only.clientIP(r); got != "198.51.100.7" {
		t.Errorf("listed proxy: %q", got)
	}
	r.RemoteAddr = "172.17.0.1:1"
	if got := only.clientIP(r); got != "172.17.0.1" {
		t.Errorf("unlisted proxy believed: %q", got)
	}
}

// A grab only takes a release the server itself offered for that book: the
// download URL is sealed on the way out and has to open on the way back.
func TestGrabOnlyTakesWhatTheServerOffered(t *testing.T) {
	f := newScopedFixture(t)
	book := f.addBook(t, f.userCookie, map[string]any{"title": "Drift"}, nil)
	id := int64(book["id"].(float64))

	rels := []release.Release{{Title: "Drift EPUB", Protocol: "usenet", Source: "prowlarr:ndx",
		DownloadURL: "http://prowlarr:9696/1/download?apikey=SECRET&link=abc"}}
	f.srv.signReleases(id, "ebook", rels)
	if strings.Contains(rels[0].DownloadURL, "SECRET") || strings.Contains(rels[0].DownloadURL, "prowlarr") {
		t.Fatalf("the indexer's URL went out in the clear: %q", rels[0].DownloadURL)
	}
	// it opens for the book and kind it was sealed for, and nothing else
	if u, ok := openURL(id, "ebook", rels[0]); !ok || !strings.Contains(u, "apikey=SECRET") {
		t.Fatalf("sealed release didn't open: %q %v", u, ok)
	}
	if _, ok := openURL(id+1, "ebook", rels[0]); ok {
		t.Error("opened for another book")
	}
	if _, ok := openURL(id, "audiobook", rels[0]); ok {
		t.Error("opened for another kind")
	}
	retitled := rels[0]
	retitled.Title = "Something Else"
	if _, ok := openURL(id, "ebook", retitled); ok {
		t.Error("opened with a changed title")
	}
	direct := rels[0]
	direct.Protocol = "direct"
	if _, ok := openURL(id, "ebook", direct); ok {
		t.Error("opened with a changed protocol")
	}

	// a release made up by the caller — any URL they like — is refused, for
	// users and admins alike, on both grab endpoints
	forged := map[string]any{"libraryId": f.lib, "release": map[string]any{
		"title": "x", "protocol": "usenet", "source": "prowlarr:ndx", "downloadUrl": "http://169.254.169.254/latest/meta-data"}}
	for _, c := range []*http.Cookie{f.userCookie, f.adminCooke} {
		for _, path := range []string{"/grab", "/audiobook/grab"} {
			rec, out := f.do(t, c, "POST", "/api/v1/books/"+idStr(book["id"])+path, forged)
			if rec.Code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "search again") {
				t.Errorf("%s: forged release: %d %s", path, rec.Code, rec.Body)
			}
		}
	}
	var queued int
	if err := f.srv.db.QueryRow(`SELECT COUNT(*) FROM queue`).Scan(&queued); err != nil || queued != 0 {
		t.Errorf("a forged grab reached the queue (%d rows, %v)", queued, err)
	}
}

func uploadBook(t *testing.T, f *scopedFixture, c *http.Cookie, bookID string, name string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("libraryId", "1")
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/books/"+bookID+"/import", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	return rec
}

// What a plain user may upload: a file for a book they can see that has
// none. Nothing is written for anything else.
func TestUserUploadsStayInTheirLane(t *testing.T) {
	f := newScopedFixture(t)
	downloads := t.TempDir()
	if err := f.srv.Settings.Set("downloads_dir", downloads); err != nil {
		t.Fatal(err)
	}
	leftovers := func() []string {
		entries, _ := os.ReadDir(filepath.Join(downloads, "uploads"))
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names
	}
	pdf := []byte("%PDF-1.4\n%fake\n")

	// a book the user can't see, and one that doesn't exist
	hidden := f.shelfBook(t, "Hidden", "epub", []int64{f.off})
	for _, id := range []string{idStr(float64(hidden)), "999999"} {
		if rec := uploadBook(t, f, f.userCookie, id, "x.pdf", pdf); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
			t.Errorf("upload to book %s: %d %s", id, rec.Code, rec.Body)
		}
	}
	if got := leftovers(); len(got) != 0 {
		t.Errorf("refused uploads were left on disk: %v", got)
	}

	// a shared book that already has its file: replacing it is the admin's
	shared := f.shelfBook(t, "Shared", "epub", []int64{f.mine})
	before, _ := f.srv.Catalog.GetBook(shared)
	rec := uploadBook(t, f, f.userCookie, idStr(float64(shared)), "x.pdf", pdf)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "only an admin") {
		t.Errorf("user replaced a shared book's file: %d %s", rec.Code, rec.Body)
	}
	after, _ := f.srv.Catalog.GetBook(shared)
	if after.FilePath != before.FilePath || after.FileFormat != "epub" {
		t.Errorf("the shared book's file changed: %q → %q", before.FilePath, after.FilePath)
	}
	if got := leftovers(); len(got) != 0 {
		t.Errorf("a refused upload was left on disk: %v", got)
	}

	// content that isn't the format it claims is refused and removed
	mine := f.addBook(t, f.userCookie, map[string]any{"title": "Mine"}, nil)
	if rec := uploadBook(t, f, f.userCookie, idStr(mine["id"]), "x.pdf", []byte("<html><script>alert(1)</script>")); rec.Code != http.StatusBadRequest {
		t.Errorf("a web page dressed as a PDF: %d %s", rec.Code, rec.Body)
	}
	if got := leftovers(); len(got) != 0 {
		t.Errorf("a rejected upload was left on disk: %v", got)
	}
}

// Adding a book the catalog already has shares it — it doesn't let a plain
// user rewrite what the book says.
func TestUserAddDoesNotRewriteAnExistingBook(t *testing.T) {
	f := newScopedFixture(t)
	orig := f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow", "description": "The real description."}, []int64{f.mine})
	again := f.addBook(t, f.userCookie, map[string]any{
		"title": "Burrow", "description": "Defaced.", "publisher": "Nobody", "releaseDate": "../x"}, nil)
	if idStr(again["id"]) != idStr(orig["id"]) {
		t.Fatalf("a second row was made: %v vs %v", again["id"], orig["id"])
	}
	b, err := f.srv.Catalog.GetBook(int64(orig["id"].(float64)))
	if err != nil {
		t.Fatal(err)
	}
	if b.Description != "The real description." || b.Publisher == "Nobody" || b.ReleaseDate == "../x" {
		t.Errorf("a plain user's add rewrote the book: %+v", b)
	}
	// an admin's add still updates it
	f.addBook(t, f.adminCooke, map[string]any{"title": "Burrow", "description": "Revised by the admin."}, nil)
	if b, _ := f.srv.Catalog.GetBook(int64(orig["id"].(float64))); b.Description != "Revised by the admin." {
		t.Errorf("an admin's add didn't update: %q", b.Description)
	}
}

// A shared book's history doesn't tell everyone who else has it.
func TestHistoryHidesOtherAudiences(t *testing.T) {
	f := newScopedFixture(t)
	id := f.shelfBook(t, "Shared", "epub", []int64{f.mine})
	lib := f.srv.Catalog.TheLibrary()
	_ = f.srv.Catalog.AddHistory(id, lib, "tagged", "+Private, +jolene")
	_ = f.srv.Catalog.AddHistory(id, lib, "sent", `"Shared" → Jolene's Kindle`)
	_ = f.srv.Catalog.AddHistory(id, lib, "warning", `send to kindle failed: "Shared" → Jolene's Kindle: dial tcp 10.0.0.5:25`)
	_ = f.srv.Catalog.AddHistory(id, lib, "imported", "the file arrived")

	rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/history", nil)
	body := rec.Body.String()
	for _, secret := range []string{"jolene", "Jolene", "Private", "10.0.0.5"} {
		if strings.Contains(body, secret) {
			t.Errorf("a plain user's history shows %q: %s", secret, body)
		}
	}
	if !strings.Contains(body, "the file arrived") {
		t.Errorf("ordinary events went missing: %s", body)
	}
	rec, _ = f.do(t, f.adminCooke, "GET", "/api/v1/history", nil)
	if !strings.Contains(rec.Body.String(), "jolene") {
		t.Error("the admin's history lost the audience events")
	}
}

// Passwords: an account changes its own with the current one; an admin can
// reset anyone's; nobody else can.
func TestPasswordEndpoints(t *testing.T) {
	f := newScopedFixture(t)
	if rec, _ := f.do(t, f.userCookie, "PUT", "/api/v1/auth/password",
		map[string]string{"current": "wrong", "new": "a whole new passphrase"}); rec.Code != http.StatusBadRequest {
		t.Errorf("wrong current password: %d", rec.Code)
	}
	if rec, _ := f.do(t, f.userCookie, "PUT", "/api/v1/auth/password",
		map[string]string{"current": "another passphrase", "new": "a whole new passphrase"}); rec.Code != http.StatusOK {
		t.Fatalf("change: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/books", nil); rec.Code != http.StatusOK {
		t.Errorf("the session that changed the password was signed out: %d", rec.Code)
	}
	login(t, f.h, "sam", "a whole new passphrase")

	// a user can't reset anyone, themselves included, by the admin route
	if rec, _ := f.do(t, f.userCookie, "PUT", "/api/v1/users/1/password",
		map[string]string{"password": "taken over by sam"}); rec.Code != http.StatusForbidden {
		t.Errorf("user reset the admin's password: %d", rec.Code)
	}
	if rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/users/"+idStr(float64(f.samID))+"/password",
		map[string]string{"password": "reset by the admin"}); rec.Code != http.StatusOK {
		t.Fatalf("admin reset: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.do(t, f.userCookie, "GET", "/api/v1/books", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("sam is still signed in after the reset: %d", rec.Code)
	}
	login(t, f.h, "sam", "reset by the admin")
	// signed out, there's nothing to change
	if rec, _ := f.do(t, nil, "PUT", "/api/v1/auth/password",
		map[string]string{"current": "x", "new": "y"}); rec.Code != http.StatusUnauthorized {
		t.Errorf("signed-out password change: %d", rec.Code)
	}
}

// Book files are served as what they claim to be and sandboxed, so a file
// that is really a web page can't run as one on Booky's origin.
func TestBookFilesAreServedInert(t *testing.T) {
	f := newScopedFixture(t)
	id := f.shelfBook(t, "Trap", "html", []int64{f.mine})
	req := httptest.NewRequest("GET", "/api/v1/books/"+idStr(float64(id))+"/file", nil)
	req.AddCookie(f.userCookie)
	rec := httptest.NewRecorder()
	f.h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("file: %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/octet-stream" {
		t.Errorf("an unknown format is served as %q", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.HasPrefix(csp, "sandbox") {
		t.Errorf("book file CSP = %q", csp)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("no nosniff on a book file")
	}
}

func TestSameURL(t *testing.T) {
	if !sameURL("http://Prowlarr:9696/", " http://prowlarr:9696") || sameURL("http://prowlarr:9696", "http://evil.example") {
		t.Error("sameURL")
	}
	// the saved API key isn't sent to an address that isn't the saved one
	f := newScopedFixture(t)
	_ = f.srv.Settings.Set("prowlarr_url", "http://prowlarr:9696")
	_ = f.srv.Settings.Set("prowlarr_api_key", "SECRET")
	hits := 0
	catcher := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer catcher.Close()
	rec, _ := f.do(t, f.adminCooke, "POST", "/api/v1/system/test/prowlarr", map[string]string{"url": catcher.URL, "apiKey": "********"})
	if rec.Code != http.StatusBadRequest || hits != 0 {
		t.Errorf("the saved key was sent to a new address: %d, %d request(s)", rec.Code, hits)
	}
	var out map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if strings.Contains(out["error"], "SECRET") {
		t.Error("the key is in the error")
	}
}
