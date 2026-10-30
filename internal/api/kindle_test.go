package api

import (
	"bufio"
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeSMTPServer accepts messages forever and streams each DATA payload out.
func fakeSMTPServer(t *testing.T) (host string, port int, mails <-chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan string, 8)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(conn net.Conn) {
				defer func() { _ = conn.Close() }()
				_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
				r := bufio.NewReader(conn)
				w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
				w("220 fake ready")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					switch verb := strings.ToUpper(strings.SplitN(strings.TrimSpace(line), " ", 2)[0]); verb {
					case "EHLO", "HELO":
						_, _ = conn.Write([]byte("250-fake\r\n250 AUTH PLAIN\r\n"))
					case "AUTH":
						w("235 authenticated")
					case "DATA":
						w("354 go")
						var data bytes.Buffer
						for {
							dl, err := r.ReadString('\n')
							if err != nil {
								return
							}
							if dl == ".\r\n" {
								break
							}
							data.WriteString(dl)
						}
						ch <- data.String()
						w("250 accepted")
					case "QUIT":
						w("221 bye")
						return
					default:
						w("250 ok")
					}
				}
			}(conn)
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	pn, _ := strconv.Atoi(p)
	return h, pn, ch
}

func smtpBody(host string, port int, from string) map[string]any {
	return map[string]any{
		"fromAddr": from, "host": host, "port": port, "security": "none",
		"username": from, "password": "app-pass-secret",
	}
}

// The SMTP password is write-only: stored, honored, never echoed by any
// response — and each account's config is invisible to every other account.
func TestKindleSMTPWriteOnlyPerAccount(t *testing.T) {
	f := newScopedFixture(t)

	rec, out := f.do(t, f.userCookie, "PUT", "/api/v1/kindle/smtp", smtpBody("smtp.example.com", 587, "sam@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatalf("put smtp: %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "app-pass-secret") {
		t.Fatal("password echoed in PUT response")
	}
	cfg := out["config"].(map[string]any)
	if cfg["passwordSet"] != true {
		t.Fatalf("passwordSet missing: %v", out)
	}

	rec, _ = f.do(t, f.userCookie, "GET", "/api/v1/kindle/smtp", nil)
	if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), "app-pass-secret") {
		t.Fatalf("get leaked or failed: %d %s", rec.Code, rec.Body)
	}

	// an edit without a password keeps the stored one
	body := smtpBody("smtp2.example.com", 587, "sam@example.com")
	body["password"] = ""
	rec, out = f.do(t, f.userCookie, "PUT", "/api/v1/kindle/smtp", body)
	if rec.Code != http.StatusOK || out["config"].(map[string]any)["passwordSet"] != true {
		t.Fatalf("password lost on keep-edit: %d %s", rec.Code, rec.Body)
	}

	// the admin's own view is their own (empty) config, not sam's
	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/kindle/smtp", nil)
	if rec.Code != http.StatusOK || out["configured"] != false {
		t.Fatalf("admin sees someone else's config: %s", rec.Body)
	}

	// unauthenticated → the middleware, not the handler
	rec, _ = doJSON(t, f.h, "GET", "/api/v1/kindle/smtp", nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated smtp read = %d", rec.Code)
	}
}

func init() { kindlePace = 0 }

func TestKindleDeviceOwnership(t *testing.T) {
	f := newScopedFixture(t)

	// sam pairs a device auto-sending a tag they hold
	rec, out := f.do(t, f.userCookie, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Sam's Paperwhite", "email": "sam_x1@kindle.com", "autoTagIds": []int64{f.mine},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create device: %d %s", rec.Code, rec.Body)
	}
	samDevice := int64(out["id"].(float64))

	// ...but not one they don't
	rec, _ = f.do(t, f.userCookie, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Sneaky", "email": "x@kindle.com", "autoTagIds": []int64{f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("auto-send for a tag the user doesn't hold accepted: %d", rec.Code)
	}

	// admin pairs their own
	rec, out = f.do(t, f.adminCooke, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Root's Oasis", "email": "root_z9@kindle.com", "autoTagIds": []int64{f.off},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create device: %d %s", rec.Code, rec.Body)
	}
	rootDevice := int64(out["id"].(float64))

	rec, out = f.do(t, f.userCookie, "GET", "/api/v1/kindle/devices", nil)
	if rec.Code != http.StatusOK || len(list(out, "devices")) != 1 {
		t.Fatalf("sam's device list: %d %s", rec.Code, rec.Body)
	}
	rec, out = f.do(t, f.adminCooke, "GET", "/api/v1/kindle/devices", nil)
	devices := list(out, "devices")
	if rec.Code != http.StatusOK || len(devices) != 2 {
		t.Fatalf("admin device list: %d %s", rec.Code, rec.Body)
	}
	names := map[string]bool{}
	for _, d := range devices {
		names[d.(map[string]any)["ownerName"].(string)] = true
	}
	if !names["sam"] || !names["root"] {
		t.Fatalf("owner names missing: %v", names)
	}

	rec, _ = f.do(t, f.userCookie, "DELETE", fmt.Sprintf("/api/v1/kindle/devices/%d", rootDevice), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner delete = %d", rec.Code)
	}
	rec, _ = f.do(t, f.adminCooke, "DELETE", fmt.Sprintf("/api/v1/kindle/devices/%d", samDevice), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin delete = %d %s", rec.Code, rec.Body)
	}
}

// shelfBook adds a book with tags and gives it a real file on disk.
func (f *scopedFixture) shelfBook(t *testing.T, title, format string, tags []int64) int64 {
	t.Helper()
	out := f.addBook(t, f.adminCooke, map[string]any{"provider": "manual", "title": title}, tags)
	bookID := int64(out["id"].(float64))
	path := filepath.Join(t.TempDir(), strings.ToLower(title)+"."+format)
	if err := os.WriteFile(path, []byte("fake-"+format+"-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.Catalog.SetFile(f.lib, bookID, path, format, 17); err != nil {
		t.Fatal(err)
	}
	return bookID
}

func (f *scopedFixture) kindleDevice(t *testing.T, c *http.Cookie, name, email string, auto []int64) int64 {
	t.Helper()
	rec, out := f.do(t, c, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": name, "email": email, "autoTagIds": auto,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create device %s: %d %s", name, rec.Code, rec.Body)
	}
	return int64(out["id"].(float64))
}

func expectMail(t *testing.T, mails <-chan string, wants ...string) {
	t.Helper()
	select {
	case msg := <-mails:
		for _, want := range wants {
			if !strings.Contains(msg, want) {
				t.Errorf("mail missing %q", want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no mail arrived (wanted %v)", wants)
	}
}

func expectNoMail(t *testing.T, mails <-chan string, why string) {
	t.Helper()
	select {
	case msg := <-mails:
		t.Fatalf("%s — but a mail went out:\n%s", why, msg[:min(len(msg), 300)])
	case <-time.After(400 * time.Millisecond):
	}
}

func TestSendBookToKindle(t *testing.T) {
	f := newScopedFixture(t)
	host, port, mails := fakeSMTPServer(t)

	rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/kindle/smtp", smtpBody(host, port, "root.shelf@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatalf("put smtp: %d %s", rec.Code, rec.Body)
	}
	deviceID := f.kindleDevice(t, f.adminCooke, "Oasis", "root_z9@kindle.com", nil)
	bookID := f.shelfBook(t, "Burrow", "epub", []int64{f.mine})

	rec, _ = f.do(t, f.adminCooke, "POST", fmt.Sprintf("/api/v1/books/%d/send", bookID), map[string]any{"deviceId": deviceID})
	if rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body)
	}
	expectMail(t, mails, "Subject: Burrow", "To: root_z9@kindle.com",
		"application/epub+zip", `filename="Burrow - Emmett Hale.epub"`)

	// sam holds no devices — the admin's device is not theirs to use
	rec, _ = f.do(t, f.userCookie, "POST", fmt.Sprintf("/api/v1/books/%d/send", bookID), map[string]any{"deviceId": deviceID})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner send = %d %s", rec.Code, rec.Body)
	}

	// an admin can't push a book to someone's Kindle that their tags don't
	// cover: the send runs as the device's owner
	samDevice := f.kindleDevice(t, f.userCookie, "Sam's PW", "sam_x1@kindle.com", nil)
	private := f.shelfBook(t, "Haze", "epub", []int64{f.off})
	rec, _ = f.do(t, f.adminCooke, "POST", fmt.Sprintf("/api/v1/books/%d/send", private), map[string]any{"deviceId": samDevice})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin sending sam a book outside sam's tags = %d %s", rec.Code, rec.Body)
	}

	// a format Amazon refuses is rejected before any mail moves
	azw := f.shelfBook(t, "Drift", "azw3", []int64{f.mine})
	rec, _ = f.do(t, f.adminCooke, "POST", fmt.Sprintf("/api/v1/books/%d/send", azw), map[string]any{"deviceId": deviceID})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "EPUB and PDF") {
		t.Fatalf("azw3 send = %d %s", rec.Code, rec.Body)
	}
}

// A device auto-sends a book once, the moment it is both on disk and
// carrying one of the device's auto-send tags — whichever comes second: the
// file landing (send on import) or the tag arriving (send on tag).
func TestKindleSendOnImportAndOnTag(t *testing.T) {
	f := newScopedFixture(t)
	host, port, mails := fakeSMTPServer(t)

	rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/kindle/smtp", smtpBody(host, port, "root.shelf@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	f.kindleDevice(t, f.adminCooke, "Oasis", "root_z9@kindle.com", []int64{f.mine})
	// sam's device auto-sends too, but sam has no outgoing email → skipped
	f.kindleDevice(t, f.userCookie, "SamsPW", "sam_x1@kindle.com", []int64{f.mine})

	// send on import
	drift := f.shelfBook(t, "Drift", "epub", []int64{f.mine})
	f.srv.KindleOnImport(drift)
	expectMail(t, mails, "To: root_z9@kindle.com", "Subject: Drift")
	expectNoMail(t, mails, "sam has no outgoing email")

	// ...exactly once
	f.srv.KindleOnImport(drift)
	expectNoMail(t, mails, "a book already sent must not go again")

	// a book outside the device's auto tags stays put
	haze := f.shelfBook(t, "Haze", "epub", []int64{f.off})
	f.srv.KindleOnImport(haze)
	expectNoMail(t, mails, "Haze carries no auto-send tag")

	// send on tag: tagging it Alex now sends it
	rec, _ = f.do(t, f.adminCooke, "PUT", fmt.Sprintf("/api/v1/books/%d/tags", haze),
		map[string]any{"tagIds": []int64{f.off, f.mine}})
	if rec.Code != http.StatusOK {
		t.Fatalf("tag: %d %s", rec.Code, rec.Body)
	}
	expectMail(t, mails, "To: root_z9@kindle.com", "Subject: Haze")

	// re-tagging doesn't re-send
	f.do(t, f.adminCooke, "PUT", fmt.Sprintf("/api/v1/books/%d/tags", haze), map[string]any{"tagIds": []int64{f.off}})
	f.do(t, f.adminCooke, "PUT", fmt.Sprintf("/api/v1/books/%d/tags", haze), map[string]any{"tagIds": []int64{f.off, f.mine}})
	expectNoMail(t, mails, "re-tagging a book already sent")

	rec, _ = f.do(t, f.adminCooke, "GET", "/api/v1/history", nil)
	joined := rec.Body.String()
	if !strings.Contains(joined, "auto") || !strings.Contains(joined, "skipped") {
		t.Errorf("history should show the auto send and the skip: %s", joined)
	}
}

// Taking a tag away from the device's owner stops its auto-sends at once,
// even for a device that still lists the tag.
func TestKindleAutoSendFollowsTheOwnersTags(t *testing.T) {
	f := newScopedFixture(t)
	host, port, mails := fakeSMTPServer(t)

	rec, _ := f.do(t, f.userCookie, "PUT", "/api/v1/kindle/smtp", smtpBody(host, port, "sam@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	f.kindleDevice(t, f.userCookie, "SamsPW", "sam_x1@kindle.com", []int64{f.mine})

	first := f.shelfBook(t, "Burrow", "epub", []int64{f.mine})
	f.srv.KindleOnImport(first)
	expectMail(t, mails, "To: sam_x1@kindle.com", "Subject: Burrow")

	rec, _ = f.do(t, f.adminCooke, "PUT", fmt.Sprintf("/api/v1/users/%d/tags", f.samID), map[string]any{"tagIds": []int64{}})
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	second := f.shelfBook(t, "Drift", "epub", []int64{f.mine})
	f.srv.KindleOnImport(second)
	expectNoMail(t, mails, "sam no longer holds the tag")
}

// Tagging a batch asks first when it would email more than a handful of
// books, and a "no" means those books never trickle out later either.
func TestBulkTaggingAsksBeforeFloodingAKindle(t *testing.T) {
	f := newScopedFixture(t)
	host, port, mails := fakeSMTPServer(t)

	rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/kindle/smtp", smtpBody(host, port, "root.shelf@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	device := f.kindleDevice(t, f.adminCooke, "Oasis", "root_z9@kindle.com", []int64{f.mine})

	var ids []int64
	for i := 0; i < kindleBulkPrompt+2; i++ {
		ids = append(ids, f.shelfBook(t, fmt.Sprintf("Book %d", i), "epub", []int64{f.off}))
	}
	rec, out := f.do(t, f.adminCooke, "POST", "/api/v1/tags/bulk", map[string]any{
		"bookIds": ids, "tagIds": []int64{f.mine}, "mode": "add",
	})
	if rec.Code != http.StatusOK || out["needsConfirm"] != true || int(out["kindleSends"].(float64)) != len(ids) {
		t.Fatalf("bulk tag should ask first: %d %v", rec.Code, out)
	}
	if tags, _ := f.srv.Catalog.BookTags(ids[0]); len(tags) != 1 {
		t.Fatalf("asking must change nothing yet, got tags %v", tags)
	}

	rec, _ = f.do(t, f.adminCooke, "POST", "/api/v1/tags/bulk", map[string]any{
		"bookIds": ids, "tagIds": []int64{f.mine}, "mode": "add", "sendToKindle": false,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk tag: %d %s", rec.Code, rec.Body)
	}
	expectNoMail(t, mails, "the person declined the send")
	if tags, _ := f.srv.Catalog.BookTags(ids[0]); len(tags) != 2 {
		t.Fatalf("declining the send must still tag the books, got %v", tags)
	}
	// declined means handled: a file landing again won't sneak it out
	f.srv.KindleOnImport(ids[0])
	expectNoMail(t, mails, "a declined send must not trickle out later")
	if !f.srv.Kindle.WasSent(device, ids[0]) {
		t.Fatal("a declined send should be recorded as handled")
	}
}

// Adding an auto-send tag to an existing device means "send new books with
// this tag", not "email the whole shelf right now".
func TestAddingAnAutoTagDoesNotSendTheBacklog(t *testing.T) {
	f := newScopedFixture(t)
	host, port, mails := fakeSMTPServer(t)
	rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/kindle/smtp", smtpBody(host, port, "root.shelf@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	device := f.kindleDevice(t, f.adminCooke, "Oasis", "root_z9@kindle.com", nil)
	old := f.shelfBook(t, "Burrow", "epub", []int64{f.mine})

	rec, _ = f.do(t, f.adminCooke, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", device), map[string]any{
		"name": "Oasis", "email": "root_z9@kindle.com", "autoTagIds": []int64{f.mine},
	})
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	f.srv.KindleOnImport(old)
	expectNoMail(t, mails, "the backlog must not be emailed when a device gains an auto tag")

	fresh := f.shelfBook(t, "Drift", "epub", []int64{f.mine})
	f.srv.KindleOnImport(fresh)
	expectMail(t, mails, "Subject: Drift")
}

// Devices are editable in place, capped by the OWNER's tags even when an
// admin edits, and never across owners for non-admins.
func TestKindleUpdateDevice(t *testing.T) {
	f := newScopedFixture(t)
	id := f.kindleDevice(t, f.userCookie, "Sam's PW", "sam_x1@kindle.com", nil)

	rec, out := f.do(t, f.userCookie, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", id), map[string]any{
		"name": "Paperwhite", "email": "sam_x1@kindle.com", "autoTagIds": []int64{f.mine},
	})
	if rec.Code != http.StatusOK || out["name"] != "Paperwhite" {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if autos := list(out, "autoTagIds"); len(autos) != 1 {
		t.Fatalf("auto-send not enabled: %s", rec.Body)
	}

	rec, _ = f.do(t, f.userCookie, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", id), map[string]any{
		"name": "Paperwhite", "email": "sam_x1@kindle.com", "autoTagIds": []int64{f.mine, f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope edit = %d", rec.Code)
	}
	rec, _ = f.do(t, f.adminCooke, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", id), map[string]any{
		"name": "Paperwhite", "email": "sam_x1@kindle.com", "autoTagIds": []int64{f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin edit past owner scope = %d %s", rec.Code, rec.Body)
	}

	rootDevice := f.kindleDevice(t, f.adminCooke, "Root's Oasis", "root_z9@kindle.com", []int64{f.off})
	rec, _ = f.do(t, f.userCookie, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", rootDevice), map[string]any{
		"name": "Hijacked", "email": "sam_x1@kindle.com", "autoTagIds": []int64{f.mine},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner edit = %d", rec.Code)
	}
}
