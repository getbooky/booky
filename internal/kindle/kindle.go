// Package kindle stores Send-to-Kindle devices and per-account outgoing
// email. Devices mirror KoReader's ownership model — each account pairs its
// own, an admin sees all — and a device always sends through its OWNER's
// SMTP account: there is no server default, and an owner without one has the
// feature off.
package kindle

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/mailer"
	"github.com/getbooky/booky/internal/secrets"
)

type Store struct {
	db     *sql.DB
	keeper *secrets.Keeper
}

func New(database *sql.DB, keeper *secrets.Keeper) *Store {
	return &Store{db: database, keeper: keeper}
}

// Device is one Kindle destination, addressed by its @kindle.com email.
type Device struct {
	ID        int64   `json:"id"`
	Name      string  `json:"name"`
	Email     string  `json:"email"`
	Libraries []int64 `json:"libraryIds"`     // libraries the device draws from
	AutoIDs   []int64 `json:"autoLibraryIds"` // auto-send new arrivals
	CreatedAt string  `json:"createdAt"`
	LastSent  string  `json:"lastSent,omitempty"`
	// OwnerID is the account that paired the device — sends always go
	// through this account's SMTP configuration.
	OwnerID int64 `json:"ownerId"`
}

const deviceColumns = `id, owner_user_id, name, email, library_ids, auto_ids, created_at, COALESCE(last_sent, '')`

// CreateDevice registers a device. Auto libraries must be a subset of the
// device's libraries; the caller has already verified the owner may reach
// every library listed.
func (s *Store) CreateDevice(ownerID int64, name, email string, libraries, autoIDs []int64) (*Device, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" || email == "" || len(libraries) == 0 {
		return nil, fmt.Errorf("name, kindle email, and at least one library required")
	}
	if !strings.Contains(email, "@") {
		return nil, fmt.Errorf("%q doesn't look like an email address", email)
	}
	allowed := map[int64]bool{}
	for _, id := range libraries {
		allowed[id] = true
	}
	for _, id := range autoIDs {
		if !allowed[id] {
			return nil, fmt.Errorf("auto-send library %d is not in the device's library list", id)
		}
	}
	res, err := s.db.Exec(`INSERT INTO kindle_devices (owner_user_id, name, email, library_ids, auto_ids)
		VALUES (?, ?, ?, ?, ?)`, ownerID, name, email, idsJSON(libraries), idsJSON(autoIDs))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetDevice(id)
}

// UpdateDevice rewrites a device's name, email, and library lists in place —
// owner, creation time, and send history stay put. Same validation as
// CreateDevice; the caller has already verified the OWNER may reach every
// library listed.
func (s *Store) UpdateDevice(id int64, name, email string, libraries, autoIDs []int64) (*Device, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" || email == "" || len(libraries) == 0 {
		return nil, fmt.Errorf("name, kindle email, and at least one library required")
	}
	if !strings.Contains(email, "@") {
		return nil, fmt.Errorf("%q doesn't look like an email address", email)
	}
	allowed := map[int64]bool{}
	for _, lid := range libraries {
		allowed[lid] = true
	}
	for _, lid := range autoIDs {
		if !allowed[lid] {
			return nil, fmt.Errorf("auto-send library %d is not in the device's library list", lid)
		}
	}
	res, err := s.db.Exec(`UPDATE kindle_devices SET name = ?, email = ?, library_ids = ?, auto_ids = ? WHERE id = ?`,
		name, email, idsJSON(libraries), idsJSON(autoIDs), id)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, fmt.Errorf("device %d not found", id)
	}
	return s.GetDevice(id)
}

func (s *Store) GetDevice(id int64) (*Device, error) {
	return s.scanDevice(s.db.QueryRow(`SELECT `+deviceColumns+` FROM kindle_devices WHERE id = ?`, id))
}

func (s *Store) ListDevices() ([]Device, error) {
	rows, err := s.db.Query(`SELECT ` + deviceColumns + ` FROM kindle_devices ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Device
	for rows.Next() {
		d, err := scanDeviceRow(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, *d)
	}
	return out, rows.Err()
}

func (s *Store) scanDevice(row *sql.Row) (*Device, error) {
	return scanDeviceRow(row.Scan)
}

func scanDeviceRow(scan func(...any) error) (*Device, error) {
	var d Device
	var libs, autos string
	if err := scan(&d.ID, &d.OwnerID, &d.Name, &d.Email, &libs, &autos, &d.CreatedAt, &d.LastSent); err != nil {
		return nil, err
	}
	d.Libraries, d.AutoIDs = parseIDs(libs), parseIDs(autos)
	d.CreatedAt, d.LastSent = db.SQLTime(d.CreatedAt), db.SQLTime(d.LastSent)
	return &d, nil
}

// RemoveDevice deletes the device.
func (s *Store) RemoveDevice(id int64) error {
	res, err := s.db.Exec(`DELETE FROM kindle_devices WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("device %d not found", id)
	}
	return nil
}

func (s *Store) TouchSent(id int64) {
	_, _ = s.db.Exec(`UPDATE kindle_devices SET last_sent = datetime('now') WHERE id = ?`, id)
}

func (d *Device) MayAccess(libraryID int64) bool {
	for _, id := range d.Libraries {
		if id == libraryID {
			return true
		}
	}
	return false
}

func (d *Device) AutoFor(libraryID int64) bool {
	for _, id := range d.AutoIDs {
		if id == libraryID {
			return true
		}
	}
	return false
}

// ---- per-account outgoing email ----

// SMTPConfig is an account's outgoing email as the API exposes it: the
// password never leaves the server, only whether one is stored.
type SMTPConfig struct {
	FromAddr    string `json:"fromAddr"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Security    string `json:"security"` // starttls | tls | none
	Username    string `json:"username"`
	PasswordSet bool   `json:"passwordSet"`
}

var validSecurity = map[string]bool{"starttls": true, "tls": true, "none": true}

// GetSMTP returns the owner's outgoing email config, or nil when none is set.
func (s *Store) GetSMTP(ownerID int64) (*SMTPConfig, error) {
	var c SMTPConfig
	var ct []byte
	err := s.db.QueryRow(`SELECT from_addr, host, port, security, username, password_ct
		FROM kindle_smtp WHERE owner_user_id = ?`, ownerID).
		Scan(&c.FromAddr, &c.Host, &c.Port, &c.Security, &c.Username, &ct)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.PasswordSet = len(ct) > 0
	return &c, nil
}

// SetSMTP writes the owner's outgoing email. An empty password KEEPS any
// stored one (the API never echoes it back, so edits resend everything but
// the password); use ClearSMTP to drop the whole configuration.
func (s *Store) SetSMTP(ownerID int64, cfg SMTPConfig, password string) error {
	cfg.FromAddr, cfg.Host = strings.TrimSpace(cfg.FromAddr), strings.TrimSpace(cfg.Host)
	if cfg.FromAddr == "" || cfg.Host == "" {
		return fmt.Errorf("from address and SMTP server required")
	}
	if !strings.Contains(cfg.FromAddr, "@") {
		return fmt.Errorf("%q doesn't look like an email address", cfg.FromAddr)
	}
	if cfg.Port <= 0 || cfg.Port > 65535 {
		cfg.Port = 587
	}
	if !validSecurity[cfg.Security] {
		return fmt.Errorf("security must be starttls, tls, or none")
	}
	var ct []byte
	if password != "" {
		sealed, err := s.keeper.Seal(password)
		if err != nil {
			return err
		}
		ct = sealed
	}
	_, err := s.db.Exec(`INSERT INTO kindle_smtp (owner_user_id, from_addr, host, port, security, username, password_ct, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, datetime('now'))
		ON CONFLICT(owner_user_id) DO UPDATE SET
			from_addr = excluded.from_addr, host = excluded.host, port = excluded.port,
			security = excluded.security, username = excluded.username,
			password_ct = CASE WHEN excluded.password_ct IS NOT NULL THEN excluded.password_ct ELSE kindle_smtp.password_ct END,
			updated_at = excluded.updated_at`,
		ownerID, cfg.FromAddr, cfg.Host, cfg.Port, cfg.Security, cfg.Username, ct)
	return err
}

// ClearSMTP removes the owner's outgoing email entirely — their devices stop
// sending until it's set up again.
func (s *Store) ClearSMTP(ownerID int64) error {
	_, err := s.db.Exec(`DELETE FROM kindle_smtp WHERE owner_user_id = ?`, ownerID)
	return err
}

// ResolveAccount decrypts the owner's outgoing email into a sendable mailer
// account. Returns an error naming the gap when the owner has none.
func (s *Store) ResolveAccount(ownerID int64) (mailer.Account, error) {
	var acc mailer.Account
	var ct []byte
	err := s.db.QueryRow(`SELECT from_addr, host, port, security, username, password_ct
		FROM kindle_smtp WHERE owner_user_id = ?`, ownerID).
		Scan(&acc.From, &acc.Host, &acc.Port, &acc.Security, &acc.Username, &ct)
	if err == sql.ErrNoRows {
		return acc, fmt.Errorf("the device owner hasn't set up their outgoing email (Settings → Send to Kindle)")
	}
	if err != nil {
		return acc, err
	}
	if len(ct) > 0 {
		pw, err := s.keeper.Open(ct)
		if err != nil {
			return acc, err
		}
		acc.Password = pw
	}
	return acc, nil
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
