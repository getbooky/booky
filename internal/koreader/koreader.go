// Package koreader builds per-device KOReader plugins and serves their
// check-in API. Each device gets its own bearer token baked into the zip;
// the plugin syncs over wifi, pulls new arrivals from auto-download
// libraries, and can be revoked from the UI at any time.
package koreader

import (
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/db"
)

type Store struct {
	db *sql.DB
}

func New(db *sql.DB) *Store { return &Store{db: db} }

type Device struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Libraries []int64 `json:"libraryIds"`     // browsable/downloadable
	AutoIDs   []int64 `json:"autoLibraryIds"` // auto-download new arrivals
	CreatedAt string  `json:"createdAt"`
	LastSync  string  `json:"lastSync,omitempty"`
	// OwnerID is the account that paired the device. Everyone manages their
	// own devices and nobody else's; 0 means it was paired before accounts
	// existed, which only an admin can still see.
	OwnerID int64 `json:"ownerId"`
	// Token is only exposed internally (zip builder, auth) — never in JSON.
	Token string `json:"-"`
}

const deviceColumns = `id, name, token, library_ids, auto_ids, created_at, COALESCE(last_sync, ''), owner_user_id`

// Create registers a device with a fresh token, owned by ownerID. Auto
// libraries must be a subset of browsable ones. The caller is responsible for
// having already checked that the owner may reach every library listed.
func (s *Store) Create(name string, libraries, autoIDs []int64, ownerID int64) (*Device, error) {
	if name == "" || len(libraries) == 0 {
		return nil, fmt.Errorf("name and at least one library required")
	}
	allowed := map[int64]bool{}
	for _, id := range libraries {
		allowed[id] = true
	}
	for _, id := range autoIDs {
		if !allowed[id] {
			return nil, fmt.Errorf("auto-download library %d is not in the device's library list", id)
		}
	}
	token := auth.RandomToken()
	res, err := s.db.Exec(`INSERT INTO devices (name, token, library_ids, auto_ids, owner_user_id) VALUES (?, ?, ?, ?, ?)`,
		name, token, idsJSON(libraries), idsJSON(autoIDs), ownerID)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.Get(id)
}

func (s *Store) Get(id int64) (*Device, error) {
	return s.scanOne(s.db.QueryRow(`SELECT `+deviceColumns+` FROM devices WHERE id = ?`, id))
}

// ByToken resolves a device from its bearer token (plugin check-ins).
func (s *Store) ByToken(token string) (*Device, error) {
	if token == "" {
		return nil, fmt.Errorf("no token")
	}
	return s.scanOne(s.db.QueryRow(`SELECT `+deviceColumns+` FROM devices WHERE token = ?`, token))
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

// finish fills in the parsed library lists and zones the timestamps. "Last
// sync 21:14" is only meaningful in the reader's own timezone — the column
// is UTC and says so nowhere, so the zone goes on here (db.SQLTime).
func (d *Device) finish(libs, autos string) {
	d.Libraries = parseIDs(libs)
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

func (d *Device) MayAccess(libraryID int64) bool {
	for _, id := range d.Libraries {
		if id == libraryID {
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
