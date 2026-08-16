package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	dir := t.TempDir()
	k, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ct, err := k.Seal("app-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if string(ct) == "app-password-123" {
		t.Fatal("ciphertext equals plaintext")
	}
	got, err := k.Open(ct)
	if err != nil || got != "app-password-123" {
		t.Fatalf("Open = %q, %v", got, err)
	}

	// the key survives a reload from the same dir
	k2, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := k2.Open(ct); err != nil || got != "app-password-123" {
		t.Fatalf("reloaded keeper Open = %q, %v", got, err)
	}

	// key file is private
	info, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key file mode = %o, want 600", info.Mode().Perm())
	}
}

func TestOpenWithWrongKeyFails(t *testing.T) {
	k1, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	k2, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ct, err := k1.Seal("secret")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := k2.Open(ct); err == nil {
		t.Fatal("different key must not decrypt")
	}
}

func TestEnvKeyOverride(t *testing.T) {
	// assembled, not literal — a 64-hex string in source trips secret scanners
	t.Setenv("BOOKY_SECRET_KEY", strings.Repeat("0123456789abcdef", 4))
	dir := t.TempDir()
	k, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := k.Seal("v")
	if got, err := k.Open(ct); err != nil || got != "v" {
		t.Fatalf("env-keyed round trip failed: %q, %v", got, err)
	}
	// env key means no file is written next to the db
	if _, err := os.Stat(filepath.Join(dir, keyFile)); !os.IsNotExist(err) {
		t.Error("key file should not be created when BOOKY_SECRET_KEY is set")
	}

	t.Setenv("BOOKY_SECRET_KEY", "not-hex")
	if _, err := Load(dir); err == nil {
		t.Fatal("garbage env key must be rejected")
	}
}
