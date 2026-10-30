// Package koreader builds per-device KOReader plugins and serves their
// check-in API. Each device gets its own bearer token baked into the zip;
// the plugin syncs over wifi, pulls new arrivals from auto-download
// libraries, and can be revoked from the UI at any time.
package koreader

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"

	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/secrets"
)

type Store struct {
	db *sql.DB
	// keeper seals the raw token so plugin zips can be rebuilt later; the
	// token column itself holds only a SHA-256 lookup hash.
	keeper *secrets.Keeper
}

func New(database *sql.DB, keeper *secrets.Keeper) *Store {
	return &Store{db: database, keeper: keeper}
}

type Device struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
	// TagIDs are the tags the device carries (browse/download) and AutoIDs
	// the ones it auto-downloads. Both are capped by the OWNER's current
	// tags at every request. The columns held library ids before migration
	// 0024; libraries became tags of the same id, so they carry over as-is.
	TagIDs    []int64 `json:"tagIds"`
	AutoIDs   []int64 `json:"autoTagIds"`
	CreatedAt string  `json:"createdAt"`
	LastSync  string  `json:"lastSync,omitempty"`
	// OwnerID is the account that paired the device. Everyone manages their
	// own devices and nobody else's; 0 means it was paired before accounts
	// existed, which only an admin can still see.
	OwnerID int64 `json:"ownerId"`
	// Token is only exposed internally (zip builder) — never in JSON. Rows
	// scan it as the SHA-256 lookup hash; the zip handler swaps in the raw
	// token via RawToken before building.
	Token string `json:"-"`
}

const deviceColumns = `id, name, token, library_ids, auto_ids, created_at, COALESCE(last_sync, ''), owner_user_id`

// Create registers a device with a fresh token, owned by ownerID. Auto tags
// must be a subset of the device's tags. The caller is responsible for having
// already checked that the owner holds every tag listed.
func (s *Store) Create(name string, tags, autoIDs []int64, ownerID int64) (*Device, error) {
	if err := checkTags(name, tags, autoIDs); err != nil {
		return nil, err
	}
	// the database gets the hash (lookup) and a sealed copy (zip rebuilds) —
	// never the raw token, so a stolen booky.db can't sync anyone's shelf
	raw := auth.RandomToken()
	ct, err := s.keeper.Seal(raw)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(`INSERT INTO devices (name, token, token_ct, library_ids, auto_ids, owner_user_id) VALUES (?, ?, ?, ?, ?, ?)`,
		name, auth.HashToken(raw), ct, idsJSON(tags), idsJSON(autoIDs), ownerID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

// Update rewrites a device's name and library lists in place. The token (and
// its sealed copy) survive untouched, so the plugin already on the device
// keeps syncing — the server computes sync responses from the STORED lists,
// while the zip's own baked lists go stale until it's re-downloaded.
func (s *Store) Update(id int64, name string, tags, autoIDs []int64) (*Device, error) {
	if err := checkTags(name, tags, autoIDs); err != nil {
		return nil, err
	}
	res, err := s.db.Exec(`UPDATE devices SET name = ?, library_ids = ?, auto_ids = ? WHERE id = ?`,
		name, idsJSON(tags), idsJSON(autoIDs), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("device %d not found", id)
	}
	return s.Get(id)
}

// RawToken recovers a device's raw bearer token for the plugin-zip builder.
func (s *Store) RawToken(id int64) (string, error) {
	var ct []byte
	if err := s.db.QueryRow(`SELECT token_ct FROM devices WHERE id = ?`, id).Scan(&ct); err != nil {
		return "", err
	}
	if len(ct) == 0 {
		return "", fmt.Errorf("device %d has no sealed token — re-pair it", id)
	}
	return s.keeper.Open(ct)
}

// SealLegacyTokens converts rows from before token hardening: their token
// column still holds the raw value and token_ct is NULL. Each gets a sealed
// copy and its column rewritten to the lookup hash — in place, so paired
// devices keep syncing without a re-pair. Idempotent: converted rows carry a
// ct and are never touched again.
func (s *Store) SealLegacyTokens() (int, error) {
	rows, err := s.db.Query(`SELECT id, token FROM devices WHERE token_ct IS NULL`)
	if err != nil {
		return 0, err
	}
	type legacy struct {
		id  int64
		raw string
	}
	var pending []legacy
	for rows.Next() {
		var l legacy
		if err := rows.Scan(&l.id, &l.raw); err == nil {
			pending = append(pending, l)
		}
	}
	rows.Close()
	converted := 0
	for _, l := range pending {
		ct, err := s.keeper.Seal(l.raw)
		if err != nil {
			log.Printf("koreader: sealing device %d token: %v", l.id, err)
			continue
		}
		if _, err := s.db.Exec(`UPDATE devices SET token = ?, token_ct = ? WHERE id = ?`,
			auth.HashToken(l.raw), ct, l.id); err != nil {
			log.Printf("koreader: rewriting device %d token: %v", l.id, err)
			continue
		}
		converted++
	}
	return converted, rows.Err()
}

func (s *Store) Get(id int64) (*Device, error) {
	return s.scanOne(s.db.QueryRow(`SELECT `+deviceColumns+` FROM devices WHERE id = ?`, id))
}

// ByToken resolves a device from its raw bearer token (plugin check-ins) —
// the lookup hashes it, so only hashes are ever compared or stored.
func (s *Store) ByToken(token string) (*Device, error) {
	if token == "" {
		return nil, fmt.Errorf("no token")
	}
	return s.scanOne(s.db.QueryRow(`SELECT `+deviceColumns+` FROM devices WHERE token = ?`, auth.HashToken(token)))
}

func (s *Store) scanOne(row *sql.Row) (*Device, error) {
	var d Device
	var libs, autos string
	if err := row.Scan(&d.ID, &d.Name, &d.Token, &libs, &autos, &d.CreatedAt, &d.LastSync, &d.OwnerID); err != nil {
		return nil, err
	}
	d.finish(libs, autos)
	return &d, nil
}

// finish fills in the parsed tag lists and zones the timestamps. "Last
// sync 21:14" is only meaningful in the reader's own timezone — the column
// is UTC and says so nowhere, so the zone goes on here (db.SQLTime).
func (d *Device) finish(libs, autos string) {
	d.TagIDs = parseIDs(libs)
	d.AutoIDs = parseIDs(autos)
	d.CreatedAt, d.LastSync = db.SQLTime(d.CreatedAt), db.SQLTime(d.LastSync)
}

func (s *Store) List() ([]Device, error) {
	rows, err := s.db.Query(`SELECT ` + deviceColumns + ` FROM devices ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		var d Device
		var libs, autos string
		if err := rows.Scan(&d.ID, &d.Name, &d.Token, &libs, &autos, &d.CreatedAt, &d.LastSync, &d.OwnerID); err != nil {
			return nil, err
		}
		d.finish(libs, autos)
		out = append(out, d)
	}
	return out, rows.Err()
}

// Revoke deletes the device — its token stops working immediately.
func (s *Store) Revoke(id int64) error {
	res, err := s.db.Exec(`DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("device %d not found", id)
	}
	return nil
}

func (s *Store) TouchSync(id int64) {
	_, _ = s.db.Exec(`UPDATE devices SET last_sync = datetime('now') WHERE id = ?`, id)
}

func checkTags(name string, tags, autoIDs []int64) error {
	if name == "" || len(tags) == 0 {
		return fmt.Errorf("name and at least one tag required")
	}
	allowed := map[int64]bool{}
	for _, id := range tags {
		allowed[id] = true
	}
	for _, id := range autoIDs {
		if !allowed[id] {
			return fmt.Errorf("auto-download tag %d is not one of the device's tags", id)
		}
	}
	return nil
}

// HasTag reports whether the device carries a tag.
func (d *Device) HasTag(tagID int64) bool {
	for _, id := range d.TagIDs {
		if id == tagID {
			return true
		}
	}
	return false
}

// AutoFor reports whether the device auto-downloads a tag.
func (d *Device) AutoFor(tagID int64) bool {
	for _, id := range d.AutoIDs {
		if id == tagID {
			return true
		}
	}
	return false
}

func idsJSON(ids []int64) string {
	if len(ids) == 0 {
		return "[]"
	}
	raw, err := json.Marshal(ids)
	if err != nil {
		return "[]"
	}
	return string(raw)
}

func parseIDs(raw string) []int64 {
	var out []int64
	_ = json.Unmarshal([]byte(raw), &out) //nolint:errcheck // bad rows read as empty
	return out
}
