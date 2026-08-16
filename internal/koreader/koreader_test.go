package koreader

import (
	"path/filepath"
	"testing"

	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/db"
	"github.com/getbooky/booky/internal/secrets"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	conn, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	keeper, err := secrets.Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(conn, keeper)
}

// New devices store only a token hash plus a sealed copy; the raw token
// resolves check-ins and rebuilds plugin zips, and never sits in a row.
func TestDeviceTokensHashedAndSealed(t *testing.T) {
	s := testStore(t)
	d, err := s.Create("Kobo", []int64{1}, []int64{1}, 7)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := s.RawToken(d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw == d.Token {
		t.Fatal("stored token must be the hash, not the raw value")
	}
	if d.Token != auth.HashToken(raw) {
		t.Fatal("stored token is not the raw token's hash")
	}
	got, err := s.ByToken(raw)
	if err != nil || got.ID != d.ID {
		t.Fatalf("raw token must resolve the device: %v", err)
	}
	if _, err := s.ByToken(d.Token); err == nil {
		t.Fatal("the stored hash itself must NOT act as a valid bearer token")
	}
}

// Rows from before token hardening (raw token, no sealed copy) convert in
// place: the device keeps syncing with its old token, no re-pair needed.
func TestSealLegacyTokens(t *testing.T) {
	s := testStore(t)
	rawLegacy := auth.RandomToken()
	if _, err := s.db.Exec(`INSERT INTO devices (name, token, library_ids, auto_ids, owner_user_id)
		VALUES ('Old Kobo', ?, '[1]', '[]', 3)`, rawLegacy); err != nil {
		t.Fatal(err)
	}

	n, err := s.SealLegacyTokens()
	if err != nil || n != 1 {
		t.Fatalf("sweep = %d, %v", n, err)
	}
	d, err := s.ByToken(rawLegacy)
	if err != nil {
		t.Fatalf("legacy raw token must keep working after the sweep: %v", err)
	}
	if got, err := s.RawToken(d.ID); err != nil || got != rawLegacy {
		t.Fatalf("sealed copy must recover the legacy token: %q, %v", got, err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT token FROM devices WHERE id = ?`, d.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == rawLegacy {
		t.Fatal("legacy raw token still in the database after the sweep")
	}

	// idempotent: a second sweep finds nothing to do
	if n, err := s.SealLegacyTokens(); err != nil || n != 0 {
		t.Fatalf("re-sweep = %d, %v", n, err)
	}
}
