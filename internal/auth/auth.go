// Package auth owns accounts: local username/password users (admin/user
// roles, bcrypt hashes), cookie sessions, the tags each account holds, and
// login rate limiting. OPDS signs in per account but with its own password,
// and KoReader with a per-device token — a stolen e-reader never exposes the
// account's web login.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
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

	// opdsOK remembers recent successful OPDS sign-ins, keyed by a hash of
	// username+password. A reader app sends Basic credentials on every cover
	// request, and a bcrypt per cover would make a 50-book feed take seconds.
	// Entries hold only the user id; the account and its tags are re-read
	// on every request either way.
	opdsMu sync.Mutex
	opdsOK map[string]opdsHit

	mu       sync.Mutex
	failures map[string]failureState // key: username + "\n" + ip
}

type failureState struct {
	count int
	until time.Time
}

type opdsHit struct {
	userID int64
	until  time.Time
}

const opdsCacheTTL = 5 * time.Minute

func New(db *sql.DB) *Store {
	return &Store{db: db, failures: map[string]failureState{}, opdsOK: map[string]opdsHit{}}
}

type User struct {
	ID        int64  `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"createdAt"`
	// Tags is every tag the account holds, its personal tag included. A book
	// is theirs when it carries one of these. Admins hold tags too (their
	// personal one at least) but see everything regardless — ask the access
	// checks rather than reading this as "what an admin can reach".
	Tags []int64 `json:"tagIds"`
	// PersonalTag is the account's own tag: what they add for themselves.
	PersonalTag int64 `json:"personalTagId"`
}

func (u *User) IsAdmin() bool { return u != nil && u.Role == "admin" }

// HasTag reports whether the account holds a tag.
func (u *User) HasTag(tagID int64) bool {
	if u == nil {
		return false
	}
	for _, id := range u.Tags {
		if id == tagID {
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

// CreateUser adds an account with its personal tag, plus membership in the
// shared tags given. An account with no shared tags is ordinary: it sees
// what it adds for itself and nothing else until an admin shares more.
func (s *Store) CreateUser(username, password, role string, tagIDs []int64) (int64, error) {
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
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	res, err := tx.Exec(`INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)`,
		username, string(hash), role)
	if err != nil {
		return 0, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	res, err = tx.Exec(`INSERT INTO tags (name, owner_user_id) VALUES (?, ?)`, username, id)
	if err != nil {
		return 0, fmt.Errorf("personal tag: %w", err)
	}
	personal, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec(`INSERT INTO user_tags (user_id, tag_id) VALUES (?, ?)`, id, personal); err != nil {
		return 0, err
	}
	if err := setSharedTags(tx, id, tagIDs); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// SetUserTags replaces an account's shared tags. The personal tag is never
// touched here — it is the account's own and only goes with the account.
// Another account's personal tag can't be granted either: personal means
// personal. A nonexistent tag id fails the whole call.
func (s *Store) SetUserTags(userID int64, tagIDs []int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	if err := setSharedTags(tx, userID, tagIDs); err != nil {
		return err
	}
	return tx.Commit()
}

func setSharedTags(tx *sql.Tx, userID int64, tagIDs []int64) error {
	if _, err := tx.Exec(`DELETE FROM user_tags WHERE user_id = ?
		AND tag_id IN (SELECT id FROM tags WHERE owner_user_id IS NULL)`, userID); err != nil {
		return err
	}
	seen := map[int64]bool{}
	for _, tagID := range tagIDs {
		if seen[tagID] {
			continue
		}
		seen[tagID] = true
		var owner sql.NullInt64
		if err := tx.QueryRow(`SELECT owner_user_id FROM tags WHERE id = ?`, tagID).Scan(&owner); err != nil {
			return fmt.Errorf("tag %d not found", tagID)
		}
		if owner.Valid {
			if owner.Int64 == userID {
				continue // already held
			}
			return fmt.Errorf("tag %d is another account's personal tag", tagID)
		}
		if _, err := tx.Exec(`INSERT INTO user_tags (user_id, tag_id) VALUES (?, ?)`, userID, tagID); err != nil {
			return fmt.Errorf("tag %d: %w", tagID, err)
		}
	}
	return nil
}

// fillTags reads one account's tags. Read per request rather than cached in
// the session, so an admin sharing or unsharing a tag takes effect on the
// account's next click — and on their devices' next sync.
func (s *Store) fillTags(u *User) error {
	rows, err := s.db.Query(`SELECT ut.tag_id, COALESCE(t.owner_user_id, 0) FROM user_tags ut
		JOIN tags t ON t.id = ut.tag_id WHERE ut.user_id = ? ORDER BY ut.tag_id`, u.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	u.Tags = []int64{}
	for rows.Next() {
		var id, owner int64
		if err := rows.Scan(&id, &owner); err != nil {
			return err
		}
		u.Tags = append(u.Tags, id)
		if owner == u.ID {
			u.PersonalTag = id
		}
	}
	return rows.Err()
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
	// tags are read after the cursor closes — SQLite allows one statement
	// per connection at a time under the pooled driver
	for i := range out {
		if err := s.fillTags(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// UserByID resolves an account without a session — used to re-check a
// device (KoReader, Kindle) against its owner's current tags. Returns nil
// when the account is gone.
func (s *Store) UserByID(id int64) *User {
	var u User
	if err := s.db.QueryRow(`SELECT id, username, role, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt); err != nil {
		return nil
	}
	if err := s.fillTags(&u); err != nil {
		return nil
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

// SetOPDSPassword sets (or, with "", clears) an account's OPDS password. It
// is deliberately separate from the web password: reader apps keep it in
// plain text on the device.
func (s *Store) SetOPDSPassword(userID int64, password string) error {
	hash := ""
	if password != "" {
		if len(password) < 8 {
			return fmt.Errorf("OPDS password must be at least 8 characters")
		}
		h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
		if err != nil {
			return err
		}
		hash = string(h)
	}
	res, err := s.db.Exec(`UPDATE users SET opds_password_hash = ? WHERE id = ?`, hash, userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("user not found")
	}
	// a changed password must stop working everywhere at once
	s.opdsMu.Lock()
	for k, hit := range s.opdsOK {
		if hit.userID == userID {
			delete(s.opdsOK, k)
		}
	}
	s.opdsMu.Unlock()
	return nil
}

// OPDSConfigured reports whether an account has an OPDS password set.
func (s *Store) OPDSConfigured(userID int64) bool {
	var hash string
	if err := s.db.QueryRow(`SELECT opds_password_hash FROM users WHERE id = ?`, userID).Scan(&hash); err != nil {
		return false
	}
	return hash != ""
}

// OPDSLogin checks Basic credentials from a reader app against the account's
// OPDS password, with the same per-username+IP lockout as the web login (a
// separate counter, so a misconfigured reader can't lock its owner out of the
// web UI). It returns the account with its CURRENT tags.
func (s *Store) OPDSLogin(username, password, ip string) (*User, error) {
	username = strings.TrimSpace(username)
	sum := sha256.Sum256([]byte(username + "\x00" + password))
	cacheKey := hex.EncodeToString(sum[:])
	s.opdsMu.Lock()
	hit, ok := s.opdsOK[cacheKey]
	s.opdsMu.Unlock()
	if ok && time.Now().Before(hit.until) {
		if u := s.UserByID(hit.userID); u != nil && u.Username == username {
			return u, nil
		}
	}

	key := "opds\n" + username + "\n" + ip
	s.mu.Lock()
	st := s.failures[key]
	locked := time.Now().Before(st.until)
	s.mu.Unlock()
	if locked {
		return nil, fmt.Errorf("too many attempts — try again shortly")
	}
	fail := func() (*User, error) {
		s.mu.Lock()
		st := s.failures[key]
		st.count++
		if st.count >= maxFailures {
			st.until = time.Now().Add(lockout)
			st.count = 0
		}
		s.failures[key] = st
		s.mu.Unlock()
		return nil, fmt.Errorf("wrong username or password")
	}

	var id int64
	var hash string
	if err := s.db.QueryRow(`SELECT id, opds_password_hash FROM users WHERE username = ?`, username).
		Scan(&id, &hash); err != nil || hash == "" {
		// burn comparable time so a missing account (or one with OPDS off)
		// isn't distinguishable from a wrong password
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B1Nl0jGVSyKyOaUmYQx1lWFZK1WS"), []byte(password))
		return fail()
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return fail()
	}
	s.mu.Lock()
	delete(s.failures, key)
	s.mu.Unlock()
	u := s.UserByID(id)
	if u == nil {
		return fail()
	}
	s.opdsMu.Lock()
	s.opdsOK[cacheKey] = opdsHit{userID: id, until: time.Now().Add(opdsCacheTTL)}
	s.opdsMu.Unlock()
	return u, nil
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

	if err := s.fillTags(&u); err != nil {
		return "", nil, err
	}

	token = randomToken()
	expires := time.Now().UTC().Add(sessionTTL).Format(time.RFC3339)
	// only the hash touches the database — the cookie carries the raw token,
	// so a stolen booky.db can't ride anyone's session
	if _, err := s.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		HashToken(token), u.ID, expires); err != nil {
		return "", nil, err
	}
	return token, &u, nil
}

func (s *Store) Logout(token string) {
	_, _ = s.db.Exec(`DELETE FROM sessions WHERE token = ?`, HashToken(token))
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
		WHERE se.token = ?`, HashToken(token)).Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &expires)
	if err != nil {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, expires); err != nil || time.Now().After(t) {
		s.Logout(token)
		return nil
	}
	if err := s.fillTags(&u); err != nil {
		return nil
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

// HashToken is the storage form of every server-generated bearer token
// (sessions, KoReader devices): a plain SHA-256. Deliberately NOT bcrypt —
// these are 256-bit crypto/rand values, so there is no guessable space for a
// slow hash to defend, and session validation runs on every request where
// bcrypt's ~100ms cost would be a self-inflicted denial of service. Slow
// hashes are for human-chosen passwords; those stay bcrypt below.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
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
