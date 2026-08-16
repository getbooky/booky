package api

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"
)

// The whole API surface must refuse unauthenticated callers once any account
// exists — the only exceptions are the three deliberately open paths (login,
// the auth probe, the health check) and the surfaces that carry their own
// credentials (KoReader device tokens, OPDS basic auth), which must still
// refuse a caller presenting none.
//
// The route list is parsed from server.go itself, so a route added without a
// thought for auth turns this test red instead of shipping open.
func TestEveryRouteRefusesUnauthenticated(t *testing.T) {
	src, err := os.ReadFile("server.go")
	if err != nil {
		t.Fatal(err)
	}
	routeRe := regexp.MustCompile(`mux\.HandleFunc\("([A-Z]+) ([^"]+)"`)
	matches := routeRe.FindAllStringSubmatch(string(src), -1)
	if len(matches) < 80 {
		t.Fatalf("route parse looks broken: only %d routes found", len(matches))
	}

	// open by design: the SPA needs these to show a login screen and probes
	// need a pulse. Everything else answers 401.
	openPaths := map[string]bool{
		"/api/v1/auth/login":    true,
		"/api/v1/auth/me":       true,
		"/api/v1/system/status": true,
	}

	srv := testServer(t)
	h := srv.Handler()
	// turning auth on: the first account
	rec, _ := doJSON(t, h, "POST", "/api/v1/users", map[string]any{
		"username": "root", "password": "correct horse battery"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create admin: %d %s", rec.Code, rec.Body)
	}

	fill := regexp.MustCompile(`\{[^}]+\}`)
	for _, m := range matches {
		method, pattern := m[1], m[2]
		path := fill.ReplaceAllString(pattern, "1")
		if openPaths[path] {
			continue
		}
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte("{}")))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s answered %d to an unauthenticated caller, want 401", method, pattern, w.Code)
		}
	}

	// OPDS carries its own per-library credentials — but none presented is
	// still a refusal, never a feed
	for _, p := range []string{"/opds/1", "/opds/1/download/1", "/opds/1/cover/1"} {
		req := httptest.NewRequest("GET", p, nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusNotFound {
			t.Errorf("GET %s answered %d without credentials", p, w.Code)
		}
		if strings.Contains(w.Body.String(), "<feed") {
			t.Errorf("GET %s leaked a feed without credentials", p)
		}
	}

	// the open trio behaves — and status keeps the version for signed-in eyes
	rec, out := doJSON(t, h, "GET", "/api/v1/system/status", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	if _, leaked := out["version"]; leaked {
		t.Error("unauthenticated status must not reveal the version")
	}
	c := login(t, h, "root", "correct horse battery")
	req := httptest.NewRequest("GET", "/api/v1/system/status", nil)
	req.AddCookie(c)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), "version") {
		t.Error("signed-in status should include the version")
	}

	rec, out = doJSON(t, h, "GET", "/api/v1/auth/me", nil)
	if rec.Code != http.StatusOK || out["authRequired"] != true {
		t.Fatalf("auth/me: %d %v", rec.Code, out)
	}
	if _, leaked := out["user"]; leaked {
		t.Error("auth/me must not name a user without a session")
	}
}
