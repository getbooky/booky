package api

import (
	"net/http"
	"strings"
	"testing"
)

func TestInviteLinks(t *testing.T) {
	f := newScopedFixture(t)

	// only admins make, list or revoke links
	for _, c := range []*http.Cookie{nil, f.userCookie} {
		if rec, _ := f.do(t, c, "POST", "/api/v1/invites", map[string]any{}); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("create as non-admin: %d", rec.Code)
		}
		if rec, _ := f.do(t, c, "GET", "/api/v1/invites", nil); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("list as non-admin: %d", rec.Code)
		}
		if rec, _ := f.do(t, c, "DELETE", "/api/v1/invites/1", nil); rec.Code != http.StatusForbidden && rec.Code != http.StatusUnauthorized {
			t.Fatalf("revoke as non-admin: %d", rec.Code)
		}
	}

	rec, out := f.do(t, f.adminCooke, "POST", "/api/v1/invites", map[string]any{"note": "Kim", "tagIds": []int64{f.mine}})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	token, _ := out["token"].(string)
	if len(token) < 32 {
		t.Fatalf("token = %q", token)
	}

	// the list never carries the token
	rec, _ = f.do(t, f.adminCooke, "GET", "/api/v1/invites", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), token) {
		t.Fatalf("list: %d, leaks token: %v", rec.Code, strings.Contains(rec.Body.String(), token))
	}

	// the link works without a session
	if rec, _ := f.do(t, nil, "GET", "/api/v1/invite/"+token, nil); rec.Code != http.StatusOK {
		t.Fatalf("check: %d %s", rec.Code, rec.Body)
	}
	if rec, _ := f.do(t, nil, "GET", "/api/v1/invite/nope", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("bad token: %d", rec.Code)
	}
	// a link only makes plain accounts: asking for a role is refused outright
	if rec, _ := f.do(t, nil, "POST", "/api/v1/invite/"+token,
		map[string]any{"username": "kim", "password": "long enough password", "role": "admin"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("accept with a role: %d", rec.Code)
	}
	rec, out = f.do(t, nil, "POST", "/api/v1/invite/"+token,
		map[string]any{"username": "kim", "password": "long enough password"})
	if rec.Code != http.StatusCreated || out["role"] != "user" {
		t.Fatalf("accept: %d %s", rec.Code, rec.Body)
	}
	cookies := rec.Result().Cookies()
	if len(cookies) == 0 || !cookies[0].HttpOnly {
		t.Fatal("accepting must set the session cookie")
	}
	if rec, me := f.do(t, cookies[0], "GET", "/api/v1/auth/me", nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "kim") {
		t.Fatalf("me: %d %v", rec.Code, me)
	}
	// the new account is plain
	if rec, _ := f.do(t, cookies[0], "GET", "/api/v1/users", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("invited account reached the user list: %d", rec.Code)
	}
	// spent
	if rec, _ := f.do(t, nil, "POST", "/api/v1/invite/"+token,
		map[string]any{"username": "kim2", "password": "long enough password"}); rec.Code != http.StatusNotFound {
		t.Fatalf("second use: %d", rec.Code)
	}

	// revoke
	rec, out = f.do(t, f.adminCooke, "POST", "/api/v1/invites", map[string]any{})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body)
	}
	token = out["token"].(string)
	id := idStr(out["invite"].(map[string]any)["id"])
	if rec, _ := f.do(t, f.adminCooke, "DELETE", "/api/v1/invites/"+id, nil); rec.Code != http.StatusOK {
		t.Fatalf("revoke: %d", rec.Code)
	}
	if rec, _ := f.do(t, nil, "GET", "/api/v1/invite/"+token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("revoked link: %d", rec.Code)
	}
}
