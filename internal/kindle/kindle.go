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
//
// What it may receive is its owner's business: a manual send takes anything
// the owner can see right now. AutoTagIDs are the tags it auto-sends — a
// book is emailed once, the moment it is both on disk and carrying one of
// them (send on import, send on tag), and only while the owner still holds
// that tag.
type Device struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Email      string  `json:"email"`
	AutoTagIDs []int64 `json:"autoTagIds"`
	CreatedAt  string  `json:"createdAt"`
	LastSent   string  `json:"lastSent,omitempty"`
	// OwnerID is the account that paired the device — sends always go
	// through this account's SMTP configuration.
	OwnerID int64 `json:"ownerId"`
}

// auto_ids held library ids before migration 0024; libraries became tags of
// the same id, so the column carries over as the auto-send tag list.
const deviceColumns = `id, owner_user_id, name, email, auto_ids, created_at, COALESCE(last_sent, '')`

func validDevice(name, email string) (string, string, error) {
	name, email = strings.TrimSpace(name), strings.TrimSpace(email)
	if name == "" || email == "" {
		return "", "", fmt.Errorf("name and kindle email required")
	}
	if !strings.Contains(email, "@") {
		return "", "", fmt.Errorf("%q doesn't look like an email address", email)
	}
	return name, email, nil
}

// CreateDevice registers a device. The caller has already verified the owner
// holds every auto-send tag listed.
func (s *Store) CreateDevice(ownerID int64, name, email string, autoTagIDs []int64) (*Device, error) {
	name, email, err := validDevice(name, email)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(`INSERT INTO kindle_devices (owner_user_id, name, email, auto_ids)
		VALUES (?, ?, ?, ?)`, ownerID, name, email, idsJSON(autoTagIDs))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return s.GetDevice(id)
}

// UpdateDevice rewrites a device's name, email and auto-send tags in place —
// owner, creation time and send history stay put. The caller has already
// verified the OWNER holds every tag listed.
func (s *Store) UpdateDevice(id int64, name, email string, autoTagIDs []int64) (*Device, error) {
	name, email, err := validDevice(name, email)
	if err != nil {
		return nil, err
	}
	res, err := s.db.Exec(`UPDATE kindle_devices SET name = ?, email = ?, auto_ids = ? WHERE id = ?`,
		name, email, idsJSON(autoTagIDs), id)
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
	var autos string
	if err := scan(&d.ID, &d.OwnerID, &d.Name, &d.Email, &autos, &d.CreatedAt, &d.LastSent); err != nil {
		return nil, err
	}
	d.AutoTagIDs = parseIDs(autos)
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

// AutoFor reports whether the device auto-sends any of tagIDs.
func (d *Device) AutoFor(tagIDs []int64) bool {
	for _, want := range d.AutoTagIDs {
		for _, id := range tagIDs {
			if id == want {
				return true
			}
		}
	}
	return false
}

// ---- send log ----
//
// kindle_sent holds every book a device has been emailed. A send CLAIMS its
// row before the mail goes out and gives it back if the mail fails, so two
// triggers racing (an import landing while someone tags the book) can never
// both send.

// ClaimSend records that bookID is going to deviceID. False means it was
// already sent (or is being sent right now) — the caller must not send.
func (s *Store) ClaimSend(deviceID, bookID int64) bool {
	res, err := s.db.Exec(`INSERT OR IGNORE INTO kindle_sent (device_id, book_id) VALUES (?, ?)`, deviceID, bookID)
	if err != nil {
		return false
	}
	n, _ := res.RowsAffected()
	return n > 0
}

// ReleaseSend gives back a claim whose mail failed, so the book can go out on
// a later trigger.
func (s *Store) ReleaseSend(deviceID, bookID int64) {
	_, _ = s.db.Exec(`DELETE FROM kindle_sent WHERE device_id = ? AND book_id = ?`, deviceID, bookID)
}

// WasSent reports whether bookID has gone to deviceID.
func (s *Store) WasSent(deviceID, bookID int64) bool {
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM kindle_sent WHERE device_id = ? AND book_id = ?`, deviceID, bookID).Scan(&n)
	return n > 0
}

// Sent is one entry of a book's send record.
type Sent struct {
	DeviceID int64  `json:"deviceId"`
	Device   string `json:"device"`
	OwnerID  int64  `json:"ownerId"`
	SentAt   string `json:"sentAt"`
}

// SentFor lists the devices a book has been emailed to.
func (s *Store) SentFor(bookID int64) ([]Sent, error) {
	rows, err := s.db.Query(`SELECT d.id, d.name, d.owner_user_id, ks.sent_at FROM kindle_sent ks
		JOIN kindle_devices d ON d.id = ks.device_id WHERE ks.book_id = ? ORDER BY ks.sent_at`, bookID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Sent{}
	for rows.Next() {
		var x Sent
		if err := rows.Scan(&x.DeviceID, &x.Device, &x.OwnerID, &x.SentAt); err != nil {
			return nil, err
		}
		x.SentAt = db.SQLTime(x.SentAt)
		out = append(out, x)
	}
	return out, rows.Err()
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
