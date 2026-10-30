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

// An account's tags come back on every read path — the session lookup
// especially, since that's what every API request goes through. Every
// account holds its personal tag; shared tags are granted and revoked
// without ever touching it, and land on the next request, not the next
// login.
func TestUserTags(t *testing.T) {
	s := testStore(t)
	if _, err := s.db.Exec(`INSERT INTO tags (id, name) VALUES (101, 'Kids'), (102, 'Household')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateUser("root", "correct horse battery", "admin", nil); err != nil {
		t.Fatal(err)
	}
	userID, err := s.CreateUser("sam", "another passphrase", "user", []int64{101})
	if err != nil {
		t.Fatal(err)
	}

	token, user, err := s.Login("sam", "another passphrase", "1.2.3.4")
	if err != nil {
		t.Fatal(err)
	}
	if user.PersonalTag == 0 || !user.HasTag(user.PersonalTag) {
		t.Fatalf("login user = %+v, want a personal tag it holds", user)
	}
	if !user.HasTag(101) || user.HasTag(102) {
		t.Fatalf("login tags = %v, want Kids only (plus personal)", user.Tags)
	}
	personal := user.PersonalTag

	// re-scoping lands on the next request, not the next login — and never
	// strips the personal tag
	if err := s.SetUserTags(userID, []int64{102}); err != nil {
		t.Fatal(err)
	}
	session := s.SessionUser(token)
	if session == nil || session.HasTag(101) || !session.HasTag(102) || !session.HasTag(personal) {
		t.Fatalf("tags must be re-read per request and keep the personal tag, got %+v", session)
	}

	// another account's personal tag can never be granted
	rootID := s.SessionUser(mustLogin(t, s, "root", "correct horse battery")).PersonalTag
	if err := s.SetUserTags(userID, []int64{rootID}); err == nil {
		t.Fatal("granting someone else's personal tag must fail")
	}
	if session := s.SessionUser(token); !session.HasTag(102) {
		t.Fatalf("a refused grant must change nothing, got %v", session.Tags)
	}

	// an admin holds just their personal tag; seeing everything is decided by
	// the role, not by tags
	admin := s.SessionUser(mustLogin(t, s, "root", "correct horse battery"))
	if !admin.IsAdmin() || len(admin.Tags) != 1 {
		t.Fatalf("admin = %+v, want the admin role and only a personal tag", admin)
	}
}

// OPDS signs in with the account's own login, and locks out after repeated
// failures like the web login does — on a separate counter.
func TestOPDSLogin(t *testing.T) {
	s := testStore(t)
	id, err := s.CreateUser("sam", "web passphrase!", "user", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.OPDSLogin("sam", "web passphrase!", "1.2.3.4")
	if err != nil || u.ID != id {
		t.Fatalf("OPDS login = %v, %v", u, err)
	}
	if _, err := s.OPDSLogin("sam", "wrong passphrase", "1.2.3.4"); err == nil {
		t.Fatal("a wrong password opened OPDS")
	}
	if _, err := s.OPDSLogin("nobody", "web passphrase!", "1.2.3.4"); err == nil {
		t.Fatal("an unknown account opened OPDS")
	}

	// a changed password stops working at once, even while the old one sits
	// in the sign-in cache
	hash, err := HashPassword("new passphrase!")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, hash, id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.OPDSLogin("sam", "web passphrase!", "1.2.3.4"); err == nil {
		t.Fatal("old password still opens OPDS after a change")
	}
	if _, err := s.OPDSLogin("sam", "new passphrase!", "1.2.3.4"); err != nil {
		t.Fatalf("new password refused: %v", err)
	}

	// lockout: after maxFailures the right password is refused too — even
	// while it sits in the sign-in cache from a moment ago
	if _, err := s.OPDSLogin("sam", "new passphrase!", "5.6.7.8"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailures; i++ {
		_, _ = s.OPDSLogin("sam", "wrong", "5.6.7.8")
	}
	if _, err := s.OPDSLogin("sam", "new passphrase!", "5.6.7.8"); err == nil {
		t.Fatal("OPDS must lock out after repeated failures")
	}
	// ...but only for that IP, and the web login is a separate counter
	if _, err := s.OPDSLogin("sam", "new passphrase!", "1.2.3.4"); err != nil {
		t.Fatalf("other IP locked out too: %v", err)
	}
	if _, _, err := s.Login("sam", "new passphrase!", "5.6.7.8"); err != nil {
		t.Fatalf("OPDS failures locked the web login: %v", err)
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

// Sessions are stored hashed: the cookie's raw token must never appear in
// the database, while login/lookup/logout all keep working through it.
func TestSessionTokensStoredHashed(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := New(conn)
	if _, err := s.CreateUser("root", "correct horse battery", "admin", nil); err != nil {
		t.Fatal(err)
	}
	token, _, lerr := s.Login("root", "correct horse battery", "127.0.0.1")
	if lerr != nil {
		t.Fatal(lerr)
	}
	var stored string
	if err := conn.QueryRow(`SELECT token FROM sessions`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token {
		t.Fatal("raw session token stored in the database")
	}
	if stored != HashToken(token) {
		t.Fatalf("stored token is not the hash: %q", stored)
	}
	if s.SessionUser(token) == nil {
		t.Fatal("raw token must still resolve the session")
	}
	s.Logout(token)
	if s.SessionUser(token) != nil {
		t.Fatal("logout by raw token must kill the session")
	}
}
