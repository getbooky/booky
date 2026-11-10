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
	d, err := s.CreateDevice(7, "Paperwhite", "trevor_7fk2@kindle.com", []int64{1})
	if err != nil {
		t.Fatal(err)
	}
	if d.OwnerID != 7 || d.Email != "trevor_7fk2@kindle.com" {
		t.Fatalf("device = %+v", d)
	}
	if !d.AutoFor([]int64{3, 1}) || d.AutoFor([]int64{2}) {
		t.Error("AutoFor wrong")
	}

	// junk input rejected
	if _, err := s.CreateDevice(7, "NoEmail", "not-an-email", nil); err == nil {
		t.Fatal("non-email address must be rejected")
	}
	// a device with no auto-send tags is fine: manual sends only
	if _, err := s.CreateDevice(7, "Manual", "y@kindle.com", nil); err != nil {
		t.Fatalf("manual-only device rejected: %v", err)
	}

	all, err := s.ListDevices()
	if err != nil || len(all) != 2 {
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
	cfg.Port = 2525
	if err := s.SetSMTP(7, cfg, ""); err != nil {
		t.Fatal(err)
	}
	acc, err := s.ResolveAccount(7)
	if err != nil {
		t.Fatal(err)
	}
	if acc.Port != 2525 || acc.Password != "app-pass" {
		t.Fatalf("resolve after keep-edit = %+v", acc)
	}
	// ...but not for a different server, or one with encryption dropped:
	// the stored password was given for the stored server, and would
	// otherwise be sent wherever the form now points
	moved := cfg
	moved.Host = "smtp.elsewhere.example"
	if err := s.SetSMTP(7, moved, ""); err == nil {
		t.Fatal("the stored password was kept for a different server")
	}
	plain := cfg
	plain.Security = "none"
	if err := s.SetSMTP(7, plain, ""); err == nil {
		t.Fatal("the stored password was kept with encryption turned off")
	}
	if err := s.SetSMTP(7, moved, "new-pass"); err != nil {
		t.Fatalf("changing server with the password given: %v", err)
	}
	cfg = moved
	// nobody is trusted by default: mail only goes to the public internet
	if acc, _ := s.ResolveAccount(7); !acc.PublicOnly {
		t.Error("an account with no trust set may reach private addresses")
	}
	s.Trusted = func(id int64) bool { return id == 7 }
	if acc, _ := s.ResolveAccount(7); acc.PublicOnly {
		t.Error("a trusted owner is held to public addresses")
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

func TestKindleAddress(t *testing.T) {
	for _, ok := range []string{"sam_x1@kindle.com", " Sam.B+1@Kindle.com ", "a@free.kindle.com", "a@kindle.cn"} {
		if !KindleAddress(ok) {
			t.Errorf("%q should be a Kindle inbox", ok)
		}
	}
	for _, bad := range []string{"", "a@example.org", "a@kindle.com.evil.org", "a@notkindle.com", "kindle.com",
		"victim@example.org,a@kindle.com", "victim@example.org a@kindle.com", "a@b@kindle.com",
		"<a@kindle.com>", "a@kindle.com\r\nBcc: x@example.org", "a@sub.kindle.com"} {
		if KindleAddress(bad) {
			t.Errorf("%q must not pass as a Kindle inbox", bad)
		}
	}
}
