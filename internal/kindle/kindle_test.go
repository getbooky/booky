package kindle

import (
	"path/filepath"
	"testing"

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

func TestDeviceLifecycle(t *testing.T) {
	s := testStore(t)
	d, err := s.CreateDevice(7, "Paperwhite", "trevor_7fk2@kindle.com", []int64{1, 2}, []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if d.OwnerID != 7 || d.Email != "trevor_7fk2@kindle.com" {
		t.Fatalf("device = %+v", d)
	}
	if !d.MayAccess(2) || d.MayAccess(3) {
		t.Error("MayAccess wrong")
	}
	if !d.AutoFor(1) || d.AutoFor(2) {
		t.Error("AutoFor wrong")
	}

	// auto outside the library list is rejected
	if _, err := s.CreateDevice(7, "Bad", "x@kindle.com", []int64{1}, []int64{9}); err == nil {
		t.Fatal("auto-send library outside device list must be rejected")
	}
	// junk input rejected
	if _, err := s.CreateDevice(7, "NoEmail", "not-an-email", []int64{1}, nil); err == nil {
		t.Fatal("non-email address must be rejected")
	}
	if _, err := s.CreateDevice(7, "NoLibs", "y@kindle.com", nil, nil); err == nil {
		t.Fatal("device without libraries must be rejected")
	}

	all, err := s.ListDevices()
	if err != nil || len(all) != 1 {
		t.Fatalf("list = %+v, %v", all, err)
	}
	if err := s.RemoveDevice(d.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveDevice(d.ID); err == nil {
		t.Fatal("double remove should report not found")
	}
}

func TestSMTPRoundTripAndPasswordKeep(t *testing.T) {
	s := testStore(t)

	// nothing configured yet
	if c, err := s.GetSMTP(7); err != nil || c != nil {
		t.Fatalf("unset = %+v, %v", c, err)
	}
	if _, err := s.ResolveAccount(7); err == nil {
		t.Fatal("resolve without config must error")
	}

	cfg := SMTPConfig{FromAddr: "t@example.com", Host: "smtp.example.com", Port: 587, Security: "starttls", Username: "t@example.com"}
	if err := s.SetSMTP(7, cfg, "app-pass"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetSMTP(7)
	if err != nil || got == nil || !got.PasswordSet || got.Host != "smtp.example.com" {
		t.Fatalf("get = %+v, %v", got, err)
	}

	// editing without a password keeps the stored one
	cfg.Host = "smtp2.example.com"
	if err := s.SetSMTP(7, cfg, ""); err != nil {
		t.Fatal(err)
	}
	acc, err := s.ResolveAccount(7)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Host != "smtp2.example.com" || acc.Password != "app-pass" {
		t.Fatalf("resolve after keep-edit = %+v", acc)
	}

	// a new password replaces
	if err := s.SetSMTP(7, cfg, "new-pass"); err != nil {
		t.Fatal(err)
	}
	if acc, _ = s.ResolveAccount(7); acc.Password != "new-pass" {
		t.Fatalf("password not replaced: %q", acc.Password)
	}

	// per-account isolation
	if c, _ := s.GetSMTP(8); c != nil {
		t.Fatal("other account must not see the config")
	}

	// bad security rejected
	if err := s.SetSMTP(7, SMTPConfig{FromAddr: "a@b.c", Host: "h", Security: "ssl3"}, ""); err == nil {
		t.Fatal("invalid security must be rejected")
	}

	if err := s.ClearSMTP(7); err != nil {
		t.Fatal(err)
	}
	if c, _ := s.GetSMTP(7); c != nil {
		t.Fatal("clear should remove the config")
	}
}
