package settings

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/secrets"
)

func testStoreWithKeeper(t *testing.T) *Store {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := New(conn)
	k, err := secrets.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.keeper = k
	return s
}

// Secret settings are sealed at rest: the database row never carries the
// plaintext, while Get still returns it.
func TestSecretSettingsSealedAtRest(t *testing.T) {
	s := testStoreWithKeeper(t)
	if err := s.Set("hardcover_token", "hc-token-plain"); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = 'hardcover_token'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "hc-token-plain") || !strings.HasPrefix(raw, encPrefix) {
		t.Fatalf("stored value is not sealed: %q", raw)
	}
	if got := s.Get("hardcover_token"); got != "hc-token-plain" {
		t.Fatalf("Get = %q", got)
	}

	// non-secret keys stay plain
	if err := s.Set("naming_scheme", "{Author}/{Title}"); err != nil {
		t.Fatal(err)
	}
	s.db.QueryRow(`SELECT value FROM settings WHERE key = 'naming_scheme'`).Scan(&raw) //nolint:errcheck
	if raw != "{Author}/{Title}" {
		t.Fatalf("non-secret sealed: %q", raw)
	}
}

// UseKeeper re-seals legacy plaintext rows from installs that predate
// encryption — idempotently.
func TestUseKeeperMigratesLegacyPlaintext(t *testing.T) {
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	s := New(conn)
	// legacy plaintext row, written without a keeper
	if err := s.Set("zlib_password", "old-plain-pass"); err != nil {
		t.Fatal(err)
	}

	k, err := secrets.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	s.UseKeeper(k)

	var raw string
	if err := s.db.QueryRow(`SELECT value FROM settings WHERE key = 'zlib_password'`).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "old-plain-pass") {
		t.Fatalf("legacy plaintext not sealed: %q", raw)
	}
	if got := s.Get("zlib_password"); got != "old-plain-pass" {
		t.Fatalf("Get after migration = %q", got)
	}

	// running the sweep again changes nothing
	s.UseKeeper(k)
	var raw2 string
	s.db.QueryRow(`SELECT value FROM settings WHERE key = 'zlib_password'`).Scan(&raw2) //nolint:errcheck
	if raw != raw2 {
		t.Fatal("re-sweep must be a no-op on sealed values")
	}
}
