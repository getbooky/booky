// Package settings is the single-row-per-key app configuration store.
package settings

import (
	"database/sql"
	"encoding/base64"
	"log"
	"strings"

	"github.com/getbooky/booky/internal/secrets"
)

// SecretKeys are settings whose values are credentials. With a keeper wired
// (production), they're sealed AES-256-GCM before touching the database — the
// key lives outside booky.db, so a downloaded backup can't yield them. The
// API additionally never echoes them (see the handlers' masking).
var SecretKeys = map[string]bool{
	"hardcover_token": true, "prowlarr_api_key": true, "sab_api_key": true,
	"annas_key": true, "zlib_password": true,
}

// encPrefix marks a sealed value; anything without it is legacy plaintext,
// re-sealed by the startup sweep.
const encPrefix = "enc1:"

// Defaults applied when a key has never been written.
var defaults = map[string]string{
	// Hardcover is the only metadata provider; the key survives for old
	// installs but nothing reads other values anymore.
	"provider_order":     "hardcover",
	"naming_scheme":      "{Author}/{Title}",
	"list_poll_seconds":  "60",
	"hardcover_token":    "",
	"metadata_write":     "title,author,series,description,language,publisher,pubdate,cover,identifiers",
	"write_on_import":    "true",
	"rewrite_on_refresh": "true",
	"backlog_enabled":    "false",
	// Goodreads series overlay: announced books and unlinked novellas merged
	// into watched series during the bibliography sync. Off by default —
	// rows it added are pruned again on each author's next sync once
	// disabled (books the user shelved or edited always stay).
	"series_overlay": "false",
	// Seeded with the compilation keywords (box set, omnibus, …) so users
	// can remove any of them; migration 0013 appends these to installs that
	// had already written the key.
	"exclude_patterns": "box set\nboxed set\nomnibus\ncollection\nbundle\nanthology\ncomplete series\ntrilogy",
	"server_url":       "",
	"backup_enabled":   "true",
	"backup_keep":      "4",
	// Per-source kill switches: a disabled source never joins a search even
	// when its credentials are set (Anna's works keyless, so "configured"
	// alone can't mean "wanted").
	"prowlarr_enabled": "true",
	"annas_enabled":    "true",
	"zlib_enabled":     "true",
	// Mirror lists seeded here so the UI can show the stock entries as
	// removable pills; an emptied list genuinely means "no mirrors" (the
	// source drops out) rather than silently restoring defaults.
	"annas_mirrors": "https://annas-archive.gl\nhttps://annas-archive.pk\nhttps://annas-archive.gd",
	"zlib_domains":  "https://z-lib.sk\nhttps://z-library.sk\nhttps://1lib.sk",
}

type Store struct {
	db *sql.DB
	// keeper seals SecretKeys values at rest; nil (tests, tools) stores
	// them as-is.
	keeper *secrets.Keeper
}

func New(db *sql.DB) *Store { return &Store{db: db} }

// UseKeeper turns on at-rest encryption for secret settings and re-seals any
// legacy plaintext values already in the table (idempotent — sealed values
// carry a marker prefix).
func (s *Store) UseKeeper(k *secrets.Keeper) {
	s.keeper = k
	for key := range SecretKeys {
		var v string
		if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
			continue
		}
		if v == "" || strings.HasPrefix(v, encPrefix) {
			continue
		}
		if err := s.Set(key, v); err != nil {
			log.Printf("settings: sealing %s: %v", key, err)
		}
	}
}

func (s *Store) Get(key string) string {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if err != nil {
		return defaults[key]
	}
	if strings.HasPrefix(v, encPrefix) {
		if s.keeper == nil {
			log.Printf("settings: %s is sealed but no secret key is loaded", key)
			return ""
		}
		ct, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(v, encPrefix))
		if err != nil {
			log.Printf("settings: %s: corrupt sealed value", key)
			return ""
		}
		plain, err := s.keeper.Open(ct)
		if err != nil {
			log.Printf("settings: %s: %v", key, err)
			return ""
		}
		return plain
	}
	return v
}

func (s *Store) Set(key, value string) error {
	if SecretKeys[key] && s.keeper != nil && value != "" {
		ct, err := s.keeper.Seal(value)
		if err != nil {
			return err
		}
		value = encPrefix + base64.StdEncoding.EncodeToString(ct)
	}
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// ProviderOrder returns the user's metadata provider ranking, best first.
func (s *Store) ProviderOrder() []string {
	parts := strings.Split(s.Get("provider_order"), ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ExcludePatterns returns the user's extra title-exclusion terms, one per
// line (commas work too), applied on top of the built-in box-set filter.
func (s *Store) ExcludePatterns() []string {
	var out []string
	for _, line := range strings.Split(s.Get("exclude_patterns"), "\n") {
		for _, term := range strings.Split(line, ",") {
			if term = strings.TrimSpace(term); term != "" {
				out = append(out, term)
			}
		}
	}
	return out
}
