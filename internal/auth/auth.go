// Package auth owns web-UI accounts: local username/password users
// (admin/user roles, bcrypt hashes), cookie sessions, and login rate
// limiting. OPDS and KoReader deliberately use per-library / per-device
// credentials instead — a stolen e-reader never exposes a user account.
package auth

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionTTL = 30 * 24 * time.Hour
	// after this many consecutive failures a username/IP pair must wait
	maxFailures = 5
	lockout     = 30 * time.Second
)

type Store struct {
	db *sql.DB

	mu       sync.Mutex
	failures map[string]failureState // key: username + "\n" + ip
}

type failureState struct {
	count int
	until time.Time
}

func New(db *sql.DB) *Store {
	return &Store{db: db, failures: map[string]failureState{}}
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	// Libraries is what a 'user'-role account may reach. It is always empty
	// for admins, who reach everything — ask MayAccess rather than reading
	// this directly, or an admin looks like an account with no access at all.
	Libraries []int64 `json:"libraryIds"`
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

// MayAccess reports whether the account may work in a library. Admins always
// may; a plain user only in the libraries assigned to them.
func (u *User) MayAccess(libraryID int64) bool {
	if u == nil {
		return false
	}
	if u.IsAdmin() {
		return true
	}
	for _, id := range u.Libraries {
		if id == libraryID {
			return true
		}
	}
	return false
}

// Enabled reports whether any account exists — before the first user is
// created (first run / wizard) the UI and API are open.
func (s *Store) Enabled() bool {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
		return false
	}
	return n > 0
}

// CreateUser adds an account. libraryIDs is the set of libraries a 'user'
// may reach and is ignored for admins, who reach everything. Creating a user
// with no libraries is allowed — they can sign in and read nothing until an
// admin grants one.
func (s *Store) CreateUser(username, password, role string, libraryIDs []int64) (int64, error) {
	username = strings.TrimSpace(username)
	if username == "" || len(password) < 8 {
		return 0, fmt.Errorf("username required and password must be at least 8 characters")
	}
	if role != "admin" && role != "user" {
		return 0, fmt.Errorf("role must be admin or user")
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(`INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)`,
		username, string(hash), role)
	if err != nil {
		return 0, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if role == "user" {
		if err := s.SetUserLibraries(id, libraryIDs); err != nil {
			// the account exists but reaches nothing — undo it rather than
			// leave a half-configured user behind
			_, _ = s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
			return 0, fmt.Errorf("assign libraries: %w", err)
		}
	}
	return id, nil
}

// SetUserLibraries replaces an account's library grants. A nonexistent
// library id fails the whole call (foreign key), so a typo can't half-apply.
func (s *Store) SetUserLibraries(userID int64, libraryIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	if _, err := tx.Exec(`DELETE FROM user_libraries WHERE user_id = ?`, userID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, libID := range libraryIDs {
		if seen[libID] {
			continue
		}
		seen[libID] = true
		if _, err := tx.Exec(`INSERT INTO user_libraries (user_id, library_id) VALUES (?, ?)`,
			userID, libID); err != nil {
			return fmt.Errorf("library %d: %w", libID, err)
		}
	}
	return tx.Commit()
}

// libraries reads one account's grants. Admins are never stored, so this
// correctly returns nothing for them — MayAccess short-circuits instead.
func (s *Store) libraries(userID int64) ([]int64, error) {
	rows, err := s.db.Query(`SELECT library_id FROM user_libraries WHERE user_id = ? ORDER BY library_id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s *Store) ListUsers() ([]User, error) {
	rows, err := s.db.Query(`SELECT id, username, role, created_at FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// grants are read after the cursor closes — SQLite allows one statement
	// per connection at a time under the pooled driver
	for i := range out {
		if out[i].Role == "admin" {
			continue
		}
		libs, err := s.libraries(out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Libraries = libs
	}
	return out, nil
}

// UserByID resolves an account without a session — used to re-check a
// KoReader device against its owner's current library grants. Returns nil
// when the account is gone.
func (s *Store) UserByID(id int64) *User {
	var u User
	if err := s.db.QueryRow(`SELECT id, username, role, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt); err != nil {
		return nil
	}
	if u.Role != "admin" {
		libs, err := s.libraries(u.ID)
		if err != nil {
			return nil
		}
		u.Libraries = libs
	}
	return &u
}

// DeleteUser removes an account, refusing to remove the last admin — that
// would lock everyone out permanently.
func (s *Store) DeleteUser(id int64) error {
	var role string
	if err := s.db.QueryRow(`SELECT role FROM users WHERE id = ?`, id).Scan(&role); err != nil {
		return fmt.Errorf("user not found")
	}
	if role == "admin" {
		var admins int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE role = 'admin'`).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return fmt.Errorf("cannot delete the last admin")
		}
	}
	_, err := s.db.Exec(`DELETE FROM users WHERE id = ?`, id)
	return err
}

// Login verifies credentials (rate-limited per username+IP) and mints a
// session token.
func (s *Store) Login(username, password, ip string) (token string, user *User, err error) {
	key := username + "\n" + ip
	s.mu.Lock()
	st := s.failures[key]
	if time.Now().Before(st.until) {
		s.mu.Unlock()
		return "", nil, fmt.Errorf("too many attempts — try again shortly")
	}
	s.mu.Unlock()

	fail := func() (string, *User, error) {
		s.mu.Lock()
		st := s.failures[key]
		st.count++
		if st.count >= maxFailures {
			st.until = time.Now().Add(lockout)
			st.count = 0
		}
		s.failures[key] = st
		s.mu.Unlock()
		return "", nil, fmt.Errorf("wrong username or password")
	}

	var u User
	var hash string
	err = s.db.QueryRow(`SELECT id, username, role, created_at, password_hash FROM users WHERE username = ?`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &hash)
	if err != nil {
		// burn comparable time so a missing user isn't distinguishable
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B1Nl0jGVSyKyOaUmYQx1lWFZK1WS"), []byte(password))
		return fail()
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return fail()
	}

	s.mu.Lock()
	delete(s.failures, key)
	s.mu.Unlock()

	if u.Role != "admin" {
		if u.Libraries, err = s.libraries(u.ID); err != nil {
			return "", nil, err
		}
	}

	token = randomToken()
	expires := time.Now().UTC().Add(sessionTTL).Format(time.RFC3339)
	if _, err := s.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		token, u.ID, expires); err != nil {
		return "", nil, err
	}
	return token, &u, nil
}

func (s *Store) Logout(token string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE token = ?`, token)
}

// SessionUser resolves a session token to its user; expired sessions are
// pruned as they're seen.
func (s *Store) SessionUser(token string) *User {
	if token == "" {
		return nil
	}
	var u User
	var expires string
	err := s.db.QueryRow(`
		SELECT u.id, u.username, u.role, u.created_at, se.expires_at
		FROM sessions se JOIN users u ON u.id = se.user_id
		WHERE se.token = ?`, token).Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &expires)
	if err != nil {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, expires); err != nil || time.Now().After(t) {
		s.Logout(token)
		return nil
	}
	// Grants are read per request rather than cached in the session, so an
	// admin adding or removing a library takes effect on the user's next
	// click instead of at their next login.
	if u.Role != "admin" {
		libs, err := s.libraries(u.ID)
		if err != nil {
			return nil
		}
		u.Libraries = libs
	}
	return &u
}

func randomToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err)) // never in practice
	}
	return hex.EncodeToString(b)
}

// HashPassword is bcrypt for other per-credential stores (library OPDS).
func HashPassword(password string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	return string(h), err
}

// CheckPassword reports whether password matches a stored bcrypt hash.
func CheckPassword(hash, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// RandomToken exposes session-grade random tokens for device credentials.
func RandomToken() string { return randomToken() }
