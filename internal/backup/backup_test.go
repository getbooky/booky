package backup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/getbooky/booky/internal/db"
)

func TestBackupRoundTrip(t *testing.T) {
	configDir := t.TempDir()
	conn, err := db.Open(filepath.Join(configDir, "booky.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Exec(`INSERT INTO settings (key, value) VALUES ('marker', 'before-backup')`); err != nil {
		t.Fatal(err)
	}

	m := New(conn, configDir)
	name, err := m.Create()
	if err != nil {
		t.Fatal(err)
	}

	backups, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0].Name != name || backups[0].SizeBytes == 0 {
		t.Fatalf("bad listing: %+v", backups)
	}

	// change state after the backup, then restore — the old value must win
	if _, err := conn.Exec(`UPDATE settings SET value = 'after-backup' WHERE key = 'marker'`); err != nil {
		t.Fatal(err)
	}
	if err := m.Restore(name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(configDir, StagedDBName)); err != nil {
		t.Fatalf("restore did not stage a db: %v", err)
	}

	// simulate the restart: close, swap, reopen
	conn.Close()
	if err := SwapStaged(configDir); err != nil {
		t.Fatal(err)
	}
	conn2, err := db.Open(filepath.Join(configDir, "booky.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	var v string
	if err := conn2.QueryRow(`SELECT value FROM settings WHERE key = 'marker'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "before-backup" {
		t.Fatalf("restore did not roll state back: marker = %q", v)
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	configDir := t.TempDir()
	conn, err := db.Open(filepath.Join(configDir, "booky.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	m := New(conn, configDir)

	// create three archives with distinct names (timestamps have 1s
	// resolution, so write files directly)
	dir := filepath.Join(configDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"booky-20260101-000000.zip", "booky-20260201-000000.zip", "booky-20260301-000000.zip"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("zip"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Prune(2); err != nil {
		t.Fatal(err)
	}
	backups, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 || backups[0].Name != "booky-20260301-000000.zip" || backups[1].Name != "booky-20260201-000000.zip" {
		t.Fatalf("prune kept the wrong archives: %+v", backups)
	}
}

func TestRestoreRejectsPathTricks(t *testing.T) {
	configDir := t.TempDir()
	conn, err := db.Open(filepath.Join(configDir, "booky.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	m := New(conn, configDir)
	for _, name := range []string{"../evil.zip", "sub/dir.zip", "notzip.txt"} {
		if err := m.Restore(name); err == nil {
			t.Fatalf("restore accepted %q", name)
		}
	}
}
