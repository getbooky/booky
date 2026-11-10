package auth

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Invite links. An admin creates a link and sends it to someone; opening it
// lets that person choose their own username and password. A link makes one
// plain account, once, and stops working after InviteTTL.
//
// The token is 32 random bytes, so there is nothing to guess and no lockout:
// a counter here would only let a stranger lock a real invitee out.

// InviteTTL is how long an unused invite link works.
const InviteTTL = 7 * 24 * time.Hour

// maxPendingInvites bounds how many unused links can be outstanding.
const maxPendingInvites = 50

var (
	// ErrInviteInvalid covers every way a link can be no good — unknown,
	// used, expired, revoked. The caller is told nothing more specific.
	ErrInviteInvalid = errors.New("this invite link is no longer valid — ask for a new one")
	// ErrInviteUsed is a revoke that came too late.
	ErrInviteUsed = errors.New("invite already used")
)

type Invite struct {
	ID        int64   `json:"id"`
	Note      string  `json:"note"`
	TagIDs    []int64 `json:"tagIds"`
	CreatedBy string  `json:"createdBy"`
	CreatedAt string  `json:"createdAt"`
	ExpiresAt string  `json:"expiresAt"`
	UsedAt    string  `json:"usedAt,omitempty"`
	UsedBy    string  `json:"usedBy,omitempty"`
}

// CreateInvite makes a link for one new plain account that will join the
// shared tags given (none is fine: the account then sees only what it adds).
// The raw token is returned once and never stored.
func (s *Store) CreateInvite(createdBy, note string, tagIDs []int64) (token string, inv *Invite, err error) {
	// before the first account exists everyone is an admin, and the first
	// account must be one: a link would make it a plain user
	if !s.Enabled() {
		return "", nil, fmt.Errorf("create your own account first")
	}
	note = strings.TrimSpace(note)
	if len(note) > 100 {
		return "", nil, fmt.Errorf("note must be at most 100 characters")
	}
	if !printable(note) {
		return "", nil, fmt.Errorf("note can't contain control characters")
	}
	s.pruneInvites()

	tx, err := s.db.Begin()
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	var pending int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM invites WHERE used_at IS NULL`).Scan(&pending); err != nil {
		return "", nil, err
	}
	if pending >= maxPendingInvites {
		return "", nil, fmt.Errorf("there are already %d unused invite links — revoke some first", pending)
	}
	token = randomToken()
	now := time.Now().UTC()
	res, err := tx.Exec(`INSERT INTO invites (token_hash, note, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		HashToken(token), note, createdBy, now.Format(time.RFC3339), now.Add(InviteTTL).Format(time.RFC3339))
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	seen := map[int64]bool{}
	for _, tagID := range tagIDs {
		if seen[tagID] {
			continue
		}
		seen[tagID] = true
		var owner sql.NullInt64
		if err := tx.QueryRow(`SELECT owner_user_id FROM tags WHERE id = ?`, tagID).Scan(&owner); err != nil {
			return "", nil, fmt.Errorf("tag %d not found", tagID)
		}
		if owner.Valid {
			return "", nil, fmt.Errorf("tag %d is an account's personal tag", tagID)
		}
		if _, err := tx.Exec(`INSERT INTO invite_tags (invite_id, tag_id) VALUES (?, ?)`, id, tagID); err != nil {
			return "", nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	inv, err = s.invite(s.db, `id = ?`, id)
	return token, inv, err
}

// ListInvites returns unused links first (soonest to expire first), then the
// used ones, newest first.
func (s *Store) ListInvites() ([]Invite, error) {
	s.pruneInvites()
	rows, err := s.db.Query(`SELECT ` + inviteColumns + ` FROM invites
		ORDER BY used_at IS NOT NULL, CASE WHEN used_at IS NULL THEN expires_at END, used_at DESC`)
	if err != nil {
		return nil, err
	}
	out := []Invite{}
	for rows.Next() {
		inv, err := scanInvite(rows.Scan)
		if err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, *inv)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].TagIDs, err = inviteTags(s.db, out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// RevokeInvite cancels an unused link. One that has been used — or is being
// used this instant — is left alone and reported with ErrInviteUsed and the
// account it made: the admin asked to keep someone out, and must not be told
// that worked when the account already exists.
func (s *Store) RevokeInvite(id int64) (usedBy string, err error) {
	res, err := s.db.Exec(`DELETE FROM invites WHERE id = ? AND used_at IS NULL`, id)
	if err != nil {
		return "", err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return "", nil
	}
	if err := s.db.QueryRow(`SELECT used_by FROM invites WHERE id = ?`, id).Scan(&usedBy); err != nil {
		return "", fmt.Errorf("invite not found")
	}
	return usedBy, ErrInviteUsed
}

// ForgetInvite drops a used link from the list. It changes nothing about the
// account the link made.
func (s *Store) ForgetInvite(id int64) error {
	res, err := s.db.Exec(`DELETE FROM invites WHERE id = ? AND used_at IS NOT NULL`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("invite not found")
	}
	return nil
}

// CheckInvite reports whether a token is a link that still works.
func (s *Store) CheckInvite(token string) error {
	_, err := s.liveInvite(s.db, token)
	return err
}

// AcceptInvite spends a link on a new plain account and signs it in. The
// claim and the account are one transaction: two people racing on one link
// get one account, and a refused sign-up (name taken, weak password) or a
// crash halfway leaves the link unspent.
func (s *Store) AcceptInvite(token, username, password string) (session string, user *User, err error) {
	if err := s.CheckInvite(token); err != nil {
		return "", nil, err
	}
	username = strings.TrimSpace(username)
	if err := checkInviteUsername(username); err != nil {
		return "", nil, err
	}
	if err := checkPassword(password); err != nil {
		return "", nil, err
	}
	// hashing is the slow part: it happens only for a link that looked
	// valid a moment ago, and outside the transaction
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", nil, err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", nil, err
	}
	defer tx.Rollback() //nolint:errcheck // no-op once committed
	inv, err := s.liveInvite(tx, token)
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := tx.Exec(`UPDATE invites SET used_at = ?, used_by = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?`,
		now, username, inv.ID, now)
	if err != nil {
		return "", nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", nil, ErrInviteInvalid
	}
	tags, err := inviteTags(tx, inv.ID)
	if err != nil {
		return "", nil, err
	}
	id, err := insertUser(tx, username, string(hash), "user", tags)
	if err != nil {
		return "", nil, err
	}
	if err := tx.Commit(); err != nil {
		return "", nil, err
	}
	if user = s.UserByID(id); user == nil {
		return "", nil, fmt.Errorf("your account was created — sign in to continue")
	}
	if session, err = s.newSession(id); err != nil {
		return "", nil, fmt.Errorf("your account was created — sign in to continue")
	}
	return session, user, nil
}

// checkInviteUsername is the policy for a name someone outside picks for
// themselves. It is narrower than what an admin may type: no spaces, no
// look-alike or invisible characters to pass one account off as another.
func checkInviteUsername(username string) error {
	if len(username) < 2 || len(username) > 32 {
		return fmt.Errorf("username must be 2 to 32 characters")
	}
	for _, r := range username {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '_', r == '-':
		default:
			return fmt.Errorf("username can only use letters, numbers, dots, dashes and underscores")
		}
	}
	return nil
}

// querier is the part of *sql.DB and *sql.Tx the lookups need.
type querier interface {
	QueryRow(query string, args ...any) *sql.Row
	Query(query string, args ...any) (*sql.Rows, error)
}

func (s *Store) liveInvite(q querier, token string) (*Invite, error) {
	if token == "" || len(token) > 200 {
		return nil, ErrInviteInvalid
	}
	inv, err := s.invite(q, `token_hash = ? AND used_at IS NULL AND expires_at > ?`,
		HashToken(token), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, ErrInviteInvalid
	}
	return inv, nil
}

// pruneInvites drops links that expired unused, and used ones after 90 days.
func (s *Store) pruneInvites() {
	now := time.Now().UTC()
	_, _ = s.db.Exec(`DELETE FROM invites WHERE (used_at IS NULL AND expires_at <= ?) OR (used_at IS NOT NULL AND used_at <= ?)`,
		now.Format(time.RFC3339), now.Add(-90*24*time.Hour).Format(time.RFC3339))
}

const inviteColumns = `id, note, created_by, created_at, expires_at, COALESCE(used_at, ''), used_by`

func (s *Store) invite(q querier, where string, args ...any) (*Invite, error) {
	inv, err := scanInvite(q.QueryRow(`SELECT `+inviteColumns+` FROM invites WHERE `+where, args...).Scan)
	if err != nil {
		return nil, err
	}
	if inv.TagIDs, err = inviteTags(q, inv.ID); err != nil {
		return nil, err
	}
	return inv, nil
}

func scanInvite(scan func(...any) error) (*Invite, error) {
	inv := Invite{TagIDs: []int64{}}
	if err := scan(&inv.ID, &inv.Note, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.UsedAt, &inv.UsedBy); err != nil {
		return nil, err
	}
	return &inv, nil
}

func inviteTags(q querier, inviteID int64) ([]int64, error) {
	rows, err := q.Query(`SELECT tag_id FROM invite_tags WHERE invite_id = ? ORDER BY tag_id`, inviteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
