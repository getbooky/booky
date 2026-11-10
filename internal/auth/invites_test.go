package auth

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func inviteStore(t *testing.T) (*Store, int64) {
	t.Helper()
	s := testStore(t)
	if _, err := s.CreateUser("admin", "correct horse battery", "admin", nil); err != nil {
		t.Fatal(err)
	}
	res, err := s.db.Exec(`INSERT INTO tags (name) VALUES ('family')`)
	if err != nil {
		t.Fatal(err)
	}
	tag, _ := res.LastInsertId()
	return s, tag
}

func TestInviteMakesOnePlainAccount(t *testing.T) {
	s, tag := inviteStore(t)
	token, inv, err := s.CreateInvite("admin", " for Sam ", []int64{tag, tag})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Note != "for Sam" || len(inv.TagIDs) != 1 {
		t.Fatalf("invite = %+v", inv)
	}
	// only the hash is stored
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash FROM invites WHERE id = ?`, inv.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == token || stored != HashToken(token) {
		t.Fatal("the raw token must not be stored")
	}
	if err := s.CheckInvite(token, "1.1.1.1"); err != nil {
		t.Fatal(err)
	}

	// a refused sign-up hands the link back
	if _, _, err := s.AcceptInvite(token, "sam", "short", "1.1.1.1"); err == nil {
		t.Fatal("weak password must be refused")
	}
	if _, _, err := s.AcceptInvite(token, "ADMIN", "long enough password", "1.1.1.1"); err == nil {
		t.Fatal("a taken username must be refused")
	}
	if err := s.CheckInvite(token, "1.1.1.1"); err != nil {
		t.Fatalf("link should still work after refused sign-ups: %v", err)
	}

	session, u, err := s.AcceptInvite(token, "sam", "long enough password", "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if u.Role != "user" || !u.HasTag(tag) || u.PersonalTag == 0 || len(u.Tags) != 2 {
		t.Fatalf("user = %+v", u)
	}
	if got := s.SessionUser(session); got == nil || got.ID != u.ID {
		t.Fatal("accepting must sign the new account in")
	}
	// spent
	if err := s.CheckInvite(token, "1.1.1.1"); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("used link: %v", err)
	}
	if _, _, err := s.AcceptInvite(token, "sam2", "long enough password", "1.1.1.1"); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("used link accepted again: %v", err)
	}
	list, err := s.ListInvites()
	if err != nil || len(list) != 1 || list[0].UsedBy != "sam" || list[0].UsedAt == "" {
		t.Fatalf("list = %+v, %v", list, err)
	}
}

func TestInviteWithoutTags(t *testing.T) {
	s, _ := inviteStore(t)
	token, _, err := s.CreateInvite("admin", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, u, err := s.AcceptInvite(token, "kim", "long enough password", "1.1.1.1")
	if err != nil {
		t.Fatal(err)
	}
	if len(u.Tags) != 1 || u.Tags[0] != u.PersonalTag {
		t.Fatalf("a tagless invite gives only the personal tag, got %+v", u)
	}
}

func TestInviteRefusals(t *testing.T) {
	s, tag := inviteStore(t)
	admin := s.UserByID(1)
	if _, _, err := s.CreateInvite("admin", "", []int64{admin.PersonalTag}); err == nil {
		t.Fatal("a personal tag can't ride an invite")
	}
	if _, _, err := s.CreateInvite("admin", "", []int64{9999}); err == nil {
		t.Fatal("unknown tag must be refused")
	}

	// expired
	token, inv, err := s.CreateInvite("admin", "", []int64{tag})
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := s.db.Exec(`UPDATE invites SET expires_at = ? WHERE id = ?`, past, inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AcceptInvite(token, "late", "long enough password", "2.2.2.2"); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("expired link: %v", err)
	}

	// revoked
	token, inv, err = s.CreateInvite("admin", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RevokeInvite(inv.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.AcceptInvite(token, "gone", "long enough password", "2.2.2.3"); !errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("revoked link: %v", err)
	}

	// a tag deleted after the link was made doesn't sink the sign-up
	token, _, err = s.CreateInvite("admin", "", []int64{tag})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`DELETE FROM tags WHERE id = ?`, tag); err != nil {
		t.Fatal(err)
	}
	if _, u, err := s.AcceptInvite(token, "pat", "long enough password", "2.2.2.4"); err != nil || len(u.Tags) != 1 {
		t.Fatalf("sign-up after tag delete: %+v, %v", u, err)
	}
}

func TestInviteNeedsAnAccountFirst(t *testing.T) {
	s := testStore(t)
	if _, _, err := s.CreateInvite("", "", nil); err == nil {
		t.Fatal("no invites before the first account: the first account must be an admin")
	}
}

func TestInviteGuessingLocksOut(t *testing.T) {
	s, _ := inviteStore(t)
	token, _, err := s.CreateInvite("admin", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 40; i++ {
		_ = s.CheckInvite("not-a-token", "9.9.9.9")
	}
	if err := s.CheckInvite(token, "9.9.9.9"); err == nil || errors.Is(err, ErrInviteInvalid) {
		t.Fatalf("a guessing address should be locked out, got %v", err)
	}
	if err := s.CheckInvite(token, "8.8.8.8"); err != nil {
		t.Fatalf("other addresses are unaffected: %v", err)
	}
}

func TestInviteRaceMakesOneAccount(t *testing.T) {
	s, _ := inviteStore(t)
	token, _, err := s.CreateInvite("admin", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	won := 0
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := string(rune('a'+i)) + "racer"
			if _, _, err := s.AcceptInvite(token, name, "long enough password", "3.3.3.3"); err == nil {
				mu.Lock()
				won++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if won != 1 {
		t.Fatalf("%d accounts from one link", won)
	}
	var n int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n)
	if n != 2 {
		t.Fatalf("%d users, want admin + 1", n)
	}
}
