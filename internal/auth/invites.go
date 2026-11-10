package auth

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Invite links. An admin creates a link and sends it to someone; opening it
// lets that person choose their own username and password. A link makes one
// plain account, once, and stops working after InviteTTL.

// InviteTTL is how long an unused invite link works.
const InviteTTL = 7 * 24 * time.Hour

// maxPendingInvites bounds how many unused links can be outstanding.
const maxPendingInvites = 50

// ErrInviteInvalid covers every way a link can be no good — unknown, used,
// expired, revoked. The caller is told nothing more specific.
var ErrInviteInvalid = errors.New("this invite link is no longer valid — ask for a new one")

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
	if !s.Enabled() {
		return "", nil, fmt.Errorf("create your own account first")
	}
	note = strings.TrimSpace(note)
	if len(note) > 100 {
		return "", nil, fmt.Errorf("note must be at most 100 characters")
	}
	tagIDs = dedupe(tagIDs)
	for _, id := range tagIDs {
		var owner sql.NullInt64
		if err := s.db.QueryRow(`SELECT owner_user_id FROM tags WHERE id = ?`, id).Scan(&owner); err != nil {
			return "", nil, fmt.Errorf("tag %d not found", id)
		}
		if owner.Valid {
			return "", nil, fmt.Errorf("tag %d is an account's personal tag", id)
		}
	}
	s.pruneInvites()
	var pending int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM invites WHERE used_at IS NULL`).Scan(&pending); err != nil {
		return "", nil, err
	}
	if pending >= maxPendingInvites {
		return "", nil, fmt.Errorf("there are already %d unused invite links — revoke some first", pending)
	}
	raw, err := json.Marshal(tagIDs)
	if err != nil {
		return "", nil, err
	}
	token = randomToken()
	now := time.Now().UTC()
	res, err := s.db.Exec(`INSERT INTO invites (token_hash, note, tag_ids, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?)`,
		HashToken(token), note, string(raw), createdBy, now.Format(time.RFC3339), now.Add(InviteTTL).Format(time.RFC3339))
	if err != nil {
		return "", nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return "", nil, err
	}
	inv, err = s.invite(`id = ?`, id)
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
	defer rows.Close()
	out := []Invite{}
	for rows.Next() {
		inv, err := scanInvite(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *inv)
	}
	return out, rows.Err()
}

// RevokeInvite deletes a link, used or not.
func (s *Store) RevokeInvite(id int64) error {
	res, err := s.db.Exec(`DELETE FROM invites WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("invite not found")
	}
	return nil
}

// CheckInvite reports whether a token is a link that still works. Wrong
// tokens count against the caller's address like wrong passwords do.
func (s *Store) CheckInvite(token, ip string) error {
	_, err := s.guardedInvite(token, ip)
	return err
}

func (s *Store) guardedInvite(token, ip string) (*Invite, error) {
	key, addr := userKey("invite", "", ip), ipKey("invite", ip)
	if s.locked(key, addr) {
		return nil, fmt.Errorf("too many attempts — try again shortly")
	}
	inv, err := s.liveInvite(token)
	if err != nil {
		s.failed(key, addr)
	}
	return inv, err
}

// AcceptInvite spends a link on a new plain account and signs it in. The
// link is claimed before the account is made, so two people racing on one
// link get one account; a failed sign-up (name taken, weak password) hands
// the link back.
func (s *Store) AcceptInvite(token, username, password, ip string) (session string, user *User, err error) {
	inv, err := s.guardedInvite(token, ip)
	if err != nil {
		return "", nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := s.db.Exec(`UPDATE invites SET used_at = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?`,
		now, inv.ID, now)
	if err != nil {
		return "", nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return "", nil, ErrInviteInvalid
	}
	release := func() {
		_, _ = s.db.Exec(`UPDATE invites SET used_at = NULL WHERE id = ?`, inv.ID)
	}
	id, err := s.CreateUser(username, password, "user", s.sharedTags(inv.TagIDs))
	if err != nil {
		release()
		return "", nil, err
	}
	user = s.UserByID(id)
	if user == nil {
		return "", nil, fmt.Errorf("account created, but it couldn't be loaded — sign in")
	}
	_, _ = s.db.Exec(`UPDATE invites SET used_by = ? WHERE id = ?`, user.Username, inv.ID)
	session, err = s.newSession(id)
	if err != nil {
		return "", nil, fmt.Errorf("account created — sign in to continue")
	}
	return session, user, nil
}

// sharedTags keeps the ids that are still shared tags: one deleted since the
// link was made shouldn't sink the sign-up.
func (s *Store) sharedTags(ids []int64) []int64 {
	out := []int64{}
	for _, id := range ids {
		var n int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM tags WHERE id = ? AND owner_user_id IS NULL`, id).Scan(&n); err == nil && n > 0 {
			out = append(out, id)
		}
	}
	return out
}

func (s *Store) liveInvite(token string) (*Invite, error) {
	if token == "" || len(token) > 200 {
		return nil, ErrInviteInvalid
	}
	inv, err := s.invite(`token_hash = ? AND used_at IS NULL AND expires_at > ?`,
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

const inviteColumns = `id, note, tag_ids, created_by, created_at, expires_at, COALESCE(used_at, ''), used_by`

func (s *Store) invite(where string, args ...any) (*Invite, error) {
	return scanInvite(s.db.QueryRow(`SELECT `+inviteColumns+` FROM invites WHERE `+where, args...).Scan)
}

func scanInvite(scan func(...any) error) (*Invite, error) {
	var inv Invite
	var raw string
	if err := scan(&inv.ID, &inv.Note, &raw, &inv.CreatedBy, &inv.CreatedAt, &inv.ExpiresAt, &inv.UsedAt, &inv.UsedBy); err != nil {
		return nil, err
	}
	inv.TagIDs = []int64{}
	_ = json.Unmarshal([]byte(raw), &inv.TagIDs)
	return &inv, nil
}

func dedupe(ids []int64) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}
