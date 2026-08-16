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

func TestKindleDeviceOwnership(t *testing.T) {
	f := newScopedFixture(t)

	// sam pairs a device in their own library
	rec, out := f.do(t, f.userCookie, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Sam's Paperwhite", "email": "sam_x1@kindle.com",
		"libraryIds": []int64{f.mine}, "autoLibraryIds": []int64{f.mine},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create device: %d %s", rec.Code, rec.Body)
	}
	samDevice := int64(out["id"].(float64))

	// ...but not in a library outside their scope
	rec, _ = f.do(t, f.userCookie, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Sneaky", "email": "x@kindle.com", "libraryIds": []int64{f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope library accepted: %d", rec.Code)
	}

	// admin pairs their own
	rec, out = f.do(t, f.adminCooke, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Root's Oasis", "email": "root_z9@kindle.com", "libraryIds": []int64{f.off},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("admin create device: %d %s", rec.Code, rec.Body)
	}
	rootDevice := int64(out["id"].(float64))

	// sam sees only their own; the admin sees both with owner names
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

	// sam may not remove the admin's device — and can't learn it exists
	rec, _ = f.do(t, f.userCookie, "DELETE", fmt.Sprintf("/api/v1/kindle/devices/%d", rootDevice), nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner delete = %d", rec.Code)
	}
	// the admin may remove sam's
	rec, _ = f.do(t, f.adminCooke, "DELETE", fmt.Sprintf("/api/v1/kindle/devices/%d", samDevice), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin delete = %d %s", rec.Code, rec.Body)
	}
}

func TestSendBookToKindle(t *testing.T) {
	f := newScopedFixture(t)
	host, port, mails := fakeSMTPServer(t)

	// admin's own outgoing email, pointed at the fake
	rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/kindle/smtp", smtpBody(host, port, "root.shelf@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatalf("put smtp: %d %s", rec.Code, rec.Body)
	}
	rec, out := f.do(t, f.adminCooke, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Oasis", "email": "root_z9@kindle.com", "libraryIds": []int64{f.mine}, "autoLibraryIds": []int64{f.mine},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create device: %d %s", rec.Code, rec.Body)
	}
	deviceID := int64(out["id"].(float64))

	// a book with a real file on the shelf
	rec, book := f.do(t, f.adminCooke, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "manual", "title": "Burrow", "authors": []string{"Emmett Hale"}},
		"libraryId": f.mine, "monitored": true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("add book: %d %s", rec.Code, rec.Body)
	}
	bookID := int64(book["id"].(float64))
	path := filepath.Join(t.TempDir(), "burrow.epub")
	if err := os.WriteFile(path, []byte("fake-epub-content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.Catalog.SetFile(f.mine, bookID, path, "epub", 17); err != nil {
		t.Fatal(err)
	}

	rec, _ = f.do(t, f.adminCooke, "POST", fmt.Sprintf("/api/v1/books/%d/send", bookID), map[string]any{"deviceId": deviceID})
	if rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body)
	}
	select {
	case msg := <-mails:
		for _, want := range []string{"Subject: Burrow", "To: root_z9@kindle.com",
			"application/epub+zip", `filename="Burrow - Emmett Hale.epub"`} {
			if !strings.Contains(msg, want) {
				t.Errorf("mail missing %q", want)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no mail arrived")
	}

	// sam holds no devices — the admin's device is not theirs to use
	rec, _ = f.do(t, f.userCookie, "POST", fmt.Sprintf("/api/v1/books/%d/send", bookID), map[string]any{"deviceId": deviceID})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner send = %d %s", rec.Code, rec.Body)
	}

	// a format Amazon refuses is rejected before any mail moves
	badPath := filepath.Join(t.TempDir(), "burrow.azw3")
	if err := os.WriteFile(badPath, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.srv.Catalog.SetFile(f.mine, bookID, badPath, "azw3", 1); err != nil {
		t.Fatal(err)
	}
	rec, _ = f.do(t, f.adminCooke, "POST", fmt.Sprintf("/api/v1/books/%d/send", bookID), map[string]any{"deviceId": deviceID})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "EPUB and PDF") {
		t.Fatalf("azw3 send = %d %s", rec.Code, rec.Body)
	}
}

// Auto-send: a delivered file goes to auto devices through their owner's
// account; an owner without outgoing email is skipped with an Activity note.
func TestKindleAutoSend(t *testing.T) {
	f := newScopedFixture(t)
	host, port, mails := fakeSMTPServer(t)

	rec, _ := f.do(t, f.adminCooke, "PUT", "/api/v1/kindle/smtp", smtpBody(host, port, "root.shelf@example.com"))
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	rec, _ = f.do(t, f.adminCooke, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Oasis", "email": "root_z9@kindle.com", "libraryIds": []int64{f.mine}, "autoLibraryIds": []int64{f.mine},
	})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	// sam's device auto-sends too, but sam has no outgoing email → skipped
	rec, _ = f.do(t, f.userCookie, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "SamsPW", "email": "sam_x1@kindle.com", "libraryIds": []int64{f.mine}, "autoLibraryIds": []int64{f.mine},
	})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}

	rec, book := f.do(t, f.adminCooke, "POST", "/api/v1/books", map[string]any{
		"meta":      map[string]any{"provider": "manual", "title": "Drift", "authors": []string{"Emmett Hale"}},
		"libraryId": f.mine, "monitored": true,
	})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	bookID := int64(book["id"].(float64))
	path := filepath.Join(t.TempDir(), "drift.epub")
	if err := os.WriteFile(path, []byte("fake-epub"), 0o600); err != nil {
		t.Fatal(err)
	}

	f.srv.KindleAutoSend(bookID, f.mine, path, "epub")

	select {
	case msg := <-mails:
		if !strings.Contains(msg, "To: root_z9@kindle.com") || !strings.Contains(msg, "Subject: Drift") {
			t.Errorf("auto-send mail wrong:\n%s", msg[:min(len(msg), 400)])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no auto-send mail arrived")
	}
	// only one mail: sam's device was skipped
	select {
	case <-mails:
		t.Fatal("second mail should not exist — sam has no outgoing email")
	case <-time.After(300 * time.Millisecond):
	}

	rec, out := f.do(t, f.adminCooke, "GET", "/api/v1/history", nil)
	if rec.Code != http.StatusOK {
		t.Fatal(rec.Body.String())
	}
	joined := rec.Body.String()
	if !strings.Contains(joined, "auto") || !strings.Contains(joined, "skipped") {
		t.Errorf("history should show the auto send and the skip: %s", joined)
	}
	_ = out
}

// Devices are editable in place: libraries and auto-send flip without
// re-pairing — capped by the OWNER's grants even when an admin edits, and
// never across owners for non-admins.
func TestKindleUpdateDevice(t *testing.T) {
	f := newScopedFixture(t)

	rec, out := f.do(t, f.userCookie, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Sam's PW", "email": "sam_x1@kindle.com", "libraryIds": []int64{f.mine},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	id := int64(out["id"].(float64))

	// sam flips auto-send on and fixes the name
	rec, out = f.do(t, f.userCookie, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", id), map[string]any{
		"name": "Paperwhite", "email": "sam_x1@kindle.com",
		"libraryIds": []int64{f.mine}, "autoLibraryIds": []int64{f.mine},
	})
	if rec.Code != http.StatusOK || out["name"] != "Paperwhite" {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if autos := list(out, "autoLibraryIds"); len(autos) != 1 {
		t.Fatalf("auto-send not enabled: %s", rec.Body)
	}

	// sam can't reach past their scope...
	rec, _ = f.do(t, f.userCookie, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", id), map[string]any{
		"name": "Paperwhite", "email": "sam_x1@kindle.com", "libraryIds": []int64{f.mine, f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("out-of-scope edit = %d", rec.Code)
	}
	// ...and neither can an admin ON SAM'S BEHALF — the device sends as its
	// owner, so it stays capped by the owner's grants
	rec, _ = f.do(t, f.adminCooke, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", id), map[string]any{
		"name": "Paperwhite", "email": "sam_x1@kindle.com", "libraryIds": []int64{f.off},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("admin edit past owner scope = %d %s", rec.Code, rec.Body)
	}

	// auto outside the library list stays rejected
	rec, _ = f.do(t, f.userCookie, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", id), map[string]any{
		"name": "Paperwhite", "email": "sam_x1@kindle.com",
		"libraryIds": []int64{f.mine}, "autoLibraryIds": []int64{f.mine, f.off},
	})
	if rec.Code != http.StatusForbidden && rec.Code != http.StatusBadRequest {
		t.Fatalf("bad auto list = %d", rec.Code)
	}

	// another user's device is not sam's to edit — and reads as absent.
	// (create one as admin, try to edit as sam)
	rec, out = f.do(t, f.adminCooke, "POST", "/api/v1/kindle/devices", map[string]any{
		"name": "Root's Oasis", "email": "root_z9@kindle.com", "libraryIds": []int64{f.off},
	})
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Body.String())
	}
	rootDevice := int64(out["id"].(float64))
	rec, _ = f.do(t, f.userCookie, "PUT", fmt.Sprintf("/api/v1/kindle/devices/%d", rootDevice), map[string]any{
		"name": "Hijacked", "email": "sam_x1@kindle.com", "libraryIds": []int64{f.mine},
	})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-owner edit = %d", rec.Code)
	}
}
