// Package auth owns accounts: local username/password users (admin/user
// roles, bcrypt hashes), cookie sessions, the tags each account holds, and
// login rate limiting. OPDS signs in with the same username and password as
// the web UI; KoReader devices use a per-device token.
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
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

const (
	sessionTTL = 30 * 24 * time.Hour
	// after this many consecutive failures a username/IP pair must wait;
	// each further lockout doubles, up to maxLockout, until a sign-in works
	maxFailures = 5
	lockout     = 30 * time.Second
	maxLockout  = 15 * time.Minute
	// one address failing against any mix of usernames (password spraying)
	// is stopped as a whole
	maxIPFailures = 20
	ipLockout     = 5 * time.Minute
	maxIPLockout  = time.Hour
	// the failure table is pruned past this many entries, so a flood of
	// made-up usernames can't grow it without bound
	maxTracked = 10000
	// bcrypt reads at most 72 bytes of a password
	minPassword = 8
	maxPassword = 72
)

type Store struct {
	db *sql.DB

	// opdsOK remembers recent successful OPDS sign-ins, keyed by a hash of
	// username+password. A reader app sends Basic credentials on every cover
	// request, and a bcrypt per cover would make a 50-book feed take seconds.
	// Entries hold the user id and the password hash they were checked
	// against, so a changed password stops matching at once; the account and
	// its tags are re-read on every request either way.
	opdsMu sync.Mutex
	opdsOK map[string]opdsHit

	mu       sync.Mutex
	failures map[string]failureState // key: username + "\n" + ip
}

type failureState struct {
	count int
	level int // lockouts so far without a success: each one doubles the wait
	until time.Time
	last  time.Time
}

// userKey and ipKey name the two counters a sign-in attempt is held to.
// scope keeps the web login and OPDS apart, so a misconfigured reader app
// can't lock its owner out of the web UI.
func userKey(scope, username, ip string) string {
	return scope + "\nu\n" + strings.ToLower(strings.TrimSpace(username)) + "\n" + ip
}

func ipKey(scope, ip string) string { return scope + "\nip\n" + ip }

// locked reports whether any of the counters is serving a lockout.
func (s *Store) locked(keys ...string) bool {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, k := range keys {
		if now.Before(s.failures[k].until) {
			return true
		}
	}
	return false
}

// failed counts a wrong sign-in against the username+address pair and the
// address as a whole.
func (s *Store) failed(user, ip string) {
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	bump := func(key string, limit int, base, ceiling time.Duration) {
		st := s.failures[key]
		st.count++
		st.last = now
		if st.count >= limit {
			wait := base << min(st.level, 10)
			if wait > ceiling {
				wait = ceiling
			}
			st.until = now.Add(wait)
			st.count = 0
			st.level++
		}
		s.failures[key] = st
	}
	bump(user, maxFailures, lockout, maxLockout)
	bump(ip, maxIPFailures, ipLockout, maxIPLockout)
	if len(s.failures) > maxTracked {
		for k, st := range s.failures {
			if now.After(st.until) && now.Sub(st.last) > maxIPLockout {
				delete(s.failures, k)
			}
		}
		// still full of live entries: drop arbitrary ones rather than grow
		for k := range s.failures {
			if len(s.failures) <= maxTracked {
				break
			}
			delete(s.failures, k)
		}
	}
}

// succeeded clears the pair's counter. The address-wide one is left to run
// out on its own: one good sign-in doesn't excuse a spray of bad ones.
func (s *Store) succeeded(user string) {
	s.mu.Lock()
	delete(s.failures, user)
	s.mu.Unlock()
}

// checkPassword is the policy for a new password.
func checkPassword(password string) error {
	if len(password) < minPassword {
		return fmt.Errorf("password must be at least %d characters", minPassword)
	}
	if len(password) > maxPassword {
		return fmt.Errorf("password must be at most %d characters", maxPassword)
	}
	return nil
}

// checkUsername keeps usernames to something that reads the same everywhere
// it appears: the UI, OPDS Basic auth (no colon), tag names, logs.
func checkUsername(username string) error {
	if username == "" {
		return fmt.Errorf("username required")
	}
	if len(username) > 64 {
		return fmt.Errorf("username must be at most 64 characters")
	}
	if strings.ContainsRune(username, ':') || !printable(username) {
		return fmt.Errorf("username can't contain colons, control characters or invisible characters")
	}
	return nil
}

// printable reports whether every character shows on screen: no control
// characters, and none of the invisible or direction-flipping ones that let
// one name pass for another.
func printable(text string) bool {
	for _, r := range text {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

type opdsHit struct {
	userID int64
	hash   string // the stored password hash this sign-in was checked against
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
	if err := checkUsername(username); err != nil {
		return 0, err
	}
	if err := checkPassword(password); err != nil {
		return 0, err
	}
	// usernames are one name however they're capitalised: "Alex" and "alex"
	// as two accounts is a mix-up waiting to happen
	var taken int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ? COLLATE NOCASE`, username).Scan(&taken); err == nil && taken > 0 {
		return 0, fmt.Errorf("that username is taken")
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
	id, err := insertUser(tx, username, string(hash), role, tagIDs)
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// insertUser writes an already-vetted account, its personal tag and its
// shared tags inside the caller's transaction.
func insertUser(tx *sql.Tx, username, hash, role string, tagIDs []int64) (int64, error) {
	var taken int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM users WHERE username = ? COLLATE NOCASE`, username).Scan(&taken); err != nil {
		return 0, err
	}
	if taken > 0 {
		return 0, fmt.Errorf("that username is taken")
	}
	res, err := tx.Exec(`INSERT INTO users (username, password_hash, role) VALUES (?, ?, ?)`,
		username, hash, role)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return 0, fmt.Errorf("that username is taken")
		}
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
	return id, nil
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
	// Everything the account owned goes with it. Sessions, tags and added
	// authors follow by foreign key; the rest is keyed by a bare id, and
	// account ids are reused — left behind, a removed person's e-reader
	// token, Kindle addresses and mail password would come back to life
	// under whoever is created next.
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	// unused invite links this account handed out stop working with it
	if _, err := tx.Exec(`DELETE FROM invites WHERE used_at IS NULL AND created_by != ''
		AND created_by = (SELECT username FROM users WHERE id = ?)`, id); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM devices WHERE owner_user_id = ?`,
		`DELETE FROM kindle_devices WHERE owner_user_id = ?`,
		`DELETE FROM kindle_smtp WHERE owner_user_id = ?`,
		`DELETE FROM reading_progress WHERE user_id = ?`,
		`DELETE FROM listen_progress WHERE user_id = ?`,
		`DELETE FROM settings WHERE key = 'user_scopes_' || ?`,
		`DELETE FROM users WHERE id = ?`,
	} {
		if _, err := tx.Exec(q, id); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	// a cached OPDS sign-in mustn't outlive the account (or greet a new
	// one that gets its id)
	s.opdsMu.Lock()
	for k, h := range s.opdsOK {
		if h.userID == id {
			delete(s.opdsOK, k)
		}
	}
	s.opdsMu.Unlock()
	return nil
}

// OPDSLogin checks Basic credentials from a reader app against the account's
// own login — the same username and password as the web UI — with the same
// per-username+IP lockout (a separate counter, so a misconfigured reader
// can't lock its owner out of the web UI). It returns the account with its
// CURRENT tags.
func (s *Store) OPDSLogin(username, password, ip string) (*User, error) {
	username = strings.TrimSpace(username)
	// the lockout comes first, before even the cache: otherwise, while the
	// owner's reader keeps a cached sign-in warm, guesses from a locked-out
	// IP would still be checked against it — at hash speed, unthrottled
	key, addr := userKey("opds", username, ip), ipKey("opds", ip)
	if s.locked(key, addr) {
		return nil, fmt.Errorf("too many attempts — try again shortly")
	}

	sum := sha256.Sum256([]byte(username + "\x00" + password))
	cacheKey := hex.EncodeToString(sum[:])
	s.opdsMu.Lock()
	hit, ok := s.opdsOK[cacheKey]
	s.opdsMu.Unlock()
	if ok && time.Now().Before(hit.until) {
		var current string
		err := s.db.QueryRow(`SELECT password_hash FROM users WHERE id = ?`, hit.userID).Scan(&current)
		if u := s.UserByID(hit.userID); err == nil && current == hit.hash && u != nil && strings.EqualFold(u.Username, username) {
			return u, nil
		}
	}
	fail := func() (*User, error) {
		s.failed(key, addr)
		return nil, fmt.Errorf("wrong username or password")
	}

	var id int64
	var hash string
	if err := s.db.QueryRow(`SELECT id, password_hash FROM users WHERE username = ? COLLATE NOCASE`, username).
		Scan(&id, &hash); err != nil {
		// burn comparable time so a missing account isn't distinguishable
		// from a wrong password
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B1Nl0jGVSyKyOaUmYQx1lWFZK1WS"), []byte(password))
		return fail()
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return fail()
	}
	s.succeeded(key)
	u := s.UserByID(id)
	if u == nil {
		return fail()
	}
	s.opdsMu.Lock()
	now := time.Now()
	for k, h := range s.opdsOK { // expired sign-ins go as new ones arrive
		if now.After(h.until) {
			delete(s.opdsOK, k)
		}
	}
	s.opdsOK[cacheKey] = opdsHit{userID: id, hash: hash, until: now.Add(opdsCacheTTL)}
	s.opdsMu.Unlock()
	return u, nil
}

// Login verifies credentials (rate-limited per username+IP) and mints a
// session token.
func (s *Store) Login(username, password, ip string) (token string, user *User, err error) {
	key, addr := userKey("web", username, ip), ipKey("web", ip)
	if s.locked(key, addr) {
		return "", nil, fmt.Errorf("too many attempts — try again shortly")
	}

	fail := func() (string, *User, error) {
		s.failed(key, addr)
		return "", nil, fmt.Errorf("wrong username or password")
	}

	var u User
	var hash string
	err = s.db.QueryRow(`SELECT id, username, role, created_at, password_hash FROM users WHERE username = ? COLLATE NOCASE`,
		strings.TrimSpace(username)).Scan(&u.ID, &u.Username, &u.Role, &u.CreatedAt, &hash)
	if err != nil {
		// burn comparable time so a missing user isn't distinguishable
		_ = bcrypt.CompareHashAndPassword([]byte("$2a$10$7EqJtq98hPqEX7fNZaFWoOhi5B1Nl0jGVSyKyOaUmYQx1lWFZK1WS"), []byte(password))
		return fail()
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return fail()
	}

	s.succeeded(key)

	if err := s.fillTags(&u); err != nil {
		return "", nil, err
	}

	token, err = s.newSession(u.ID)
	if err != nil {
		return "", nil, err
	}
	return token, &u, nil
}

// newSession signs an account in and returns the cookie's token.
func (s *Store) newSession(userID int64) (string, error) {
	token := randomToken()
	expires := time.Now().UTC().Add(sessionTTL).Format(time.RFC3339)
	// only the hash touches the database — the cookie carries the raw token,
	// so a stolen booky.db can't ride anyone's session
	if _, err := s.db.Exec(`INSERT INTO sessions (token, user_id, expires_at) VALUES (?, ?, ?)`,
		HashToken(token), userID, expires); err != nil {
		return "", err
	}
	return token, nil
}

// ChangePassword lets an account set a new password by proving the current
// one. Every other session of the account is signed out — if the old
// password leaked, whoever used it is out too — and the session doing the
// change (keepToken) stays. Wrong guesses of the current password count
// against the same lockout as sign-ins.
func (s *Store) ChangePassword(userID int64, current, next, keepToken, ip string) error {
	var username, hash string
	if err := s.db.QueryRow(`SELECT username, password_hash FROM users WHERE id = ?`, userID).Scan(&username, &hash); err != nil {
		return fmt.Errorf("account not found")
	}
	key, addr := userKey("web", username, ip), ipKey("web", ip)
	if s.locked(key, addr) {
		return fmt.Errorf("too many attempts — try again shortly")
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
		s.failed(key, addr)
		return fmt.Errorf("the current password is wrong")
	}
	if err := checkPassword(next); err != nil {
		return err
	}
	if next == current {
		return fmt.Errorf("the new password is the same as the current one")
	}
	return s.setPassword(userID, next, keepToken)
}

// SetPassword is an admin's reset: no current password needed, and every
// session of the account is signed out.
func (s *Store) SetPassword(userID int64, next string) error {
	if err := checkPassword(next); err != nil {
		return err
	}
	return s.setPassword(userID, next, "")
}

func (s *Store) setPassword(userID int64, next, keepToken string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(next), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	res, err := tx.Exec(`UPDATE users SET password_hash = ? WHERE id = ?`, string(hash), userID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("account not found")
	}
	keep := ""
	if keepToken != "" {
		keep = HashToken(keepToken)
	}
	if _, err := tx.Exec(`DELETE FROM sessions WHERE user_id = ? AND token != ?`, userID, keep); err != nil {
		return err
	}
	return tx.Commit()
}

// PruneSessions removes expired sessions; run at startup and now and then.
func (s *Store) PruneSessions() (int64, error) {
	res, err := s.db.Exec(`DELETE FROM sessions WHERE expires_at < ?`, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
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
