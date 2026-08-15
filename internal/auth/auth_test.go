package auth

import (
	"path/filepath"
	"testing"

	"github.com/getbooky/booky/internal/db"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return New(conn)
}

func TestUsersAndLogin(t *testing.T) {
	s := testStore(t)
	if s.Enabled() {
		t.Fatal("auth must start disabled with no users")
	}
	if _, err := s.CreateUser("alex", "short", "admin", nil); err == nil {
		t.Fatal("short passwords must be rejected")
	}
	if _, err := s.CreateUser("alex", "correct horse battery", "admin", nil); err != nil {
		t.Fatal(err)
	}
	if !s.Enabled() {
		t.Fatal("auth must be enabled once a user exists")
	}

	if _, _, err := s.Login("alex", "wrong password!", "1.2.3.4"); err == nil {
		t.Fatal("wrong password must fail")
	}
	token, user, err := s.Login("alex", "correct horse battery", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if user.Username != "alex" || user.Role != "admin" || token == "" {
		t.Fatalf("bad login result: %+v token=%q", user, token)
	}

	if got := s.SessionUser(token); got == nil || got.ID != user.ID {
		t.Fatalf("session did not resolve: %+v", got)
	}
	s.Logout(token)
	if s.SessionUser(token) != nil {
		t.Fatal("session must die on logout")
	}
}

func TestLoginRateLimit(t *testing.T) {
	s := testStore(t)
	if _, err := s.CreateUser("alex", "correct horse battery", "admin", nil); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailures; i++ {
		_, _, _ = s.Login("alex", "nope nope nope", "9.9.9.9")
	}
	// even the right password is refused while locked out
	if _, _, err := s.Login("alex", "correct horse battery", "9.9.9.9"); err == nil {
		t.Fatal("lockout must refuse logins")
	}
	// a different IP is unaffected
	if _, _, err := s.Login("alex", "correct horse battery", "8.8.8.8"); err != nil {
		t.Fatalf("other IP should still log in: %v", err)
	}
}

func TestLastAdminUndeletable(t *testing.T) {
	s := testStore(t)
	adminID, err := s.CreateUser("alex", "correct horse battery", "admin", nil)
	if err != nil {
		t.Fatal(err)
	}
	userID, err := s.CreateUser("sam", "another passphrase", "user", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteUser(adminID); err == nil {
		t.Fatal("must refuse to delete the last admin")
	}
	if err := s.DeleteUser(userID); err != nil {
		t.Fatalf("plain users must be deletable: %v", err)
	}
}

// A user's library grants come back on every read path — the session lookup
// especially, since that's what every API request goes through. Admins carry
// no list at all and MayAccess answers yes regardless, so an admin is never
// mistaken for an account with no access.
func TestUserLibraryGrants(t *testing.T) {
	s := testStore(t)
	if _, err := s.db.Exec(`INSERT INTO quality_profiles (id, name, formats, cutoff_format)
		VALUES (1, 'EPUB preferred', '["epub"]', 'epub')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO libraries (id, name, root_path, quality_profile_id, opds_username, opds_password_hash)
		VALUES (1, 'Alex', '/a', 1, 'alex-shelf', 'unset'), (2, 'Private', '/p', 1, 'p-shelf', 'unset')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("root", "correct horse battery", "admin", nil); err != nil {
		t.Fatal(err)
	}
	userID, err := s.CreateUser("sam", "another passphrase", "user", []int64{1})
	if err != nil {
		t.Fatal(err)
	}

	token, user, err := s.Login("sam", "another passphrase", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if !user.MayAccess(1) || user.MayAccess(2) {
		t.Fatalf("login grants = %v, want library 1 only", user.Libraries)
	}

	session := s.SessionUser(token)
	if session == nil || !session.MayAccess(1) || session.MayAccess(2) {
		t.Fatalf("session grants = %v, want library 1 only", session)
	}

	// re-scoping lands on the next request, not the next login
	if err := s.SetUserLibraries(userID, []int64{2}); err != nil {
		t.Fatal(err)
	}
	session = s.SessionUser(token)
	if session == nil || session.MayAccess(1) || !session.MayAccess(2) {
		t.Fatalf("grants must be re-read per request, got %v", session)
	}

	// an admin reaches everything without holding a single grant row
	admin := s.SessionUser(mustLogin(t, s, "root", "correct horse battery"))
	if len(admin.Libraries) != 0 || !admin.MayAccess(1) || !admin.MayAccess(2) {
		t.Fatalf("admin = %v, want no stored grants but access to all", admin)
	}

	// a nonexistent library is refused outright rather than half-applied
	if err := s.SetUserLibraries(userID, []int64{2, 999}); err == nil {
		t.Fatal("granting a library that doesn't exist must fail")
	}
	if session = s.SessionUser(token); !session.MayAccess(2) {
		t.Fatal("a failed re-scope must leave the previous grants intact")
	}
}

func mustLogin(t *testing.T, s *Store, username, password string) string {
	t.Helper()
	token, _, err := s.Login(username, password, "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	return token
}
