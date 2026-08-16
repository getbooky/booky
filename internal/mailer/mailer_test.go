package mailer

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

type smtpResult struct {
	commands []string
	data     string
}

// fakeSMTP speaks just enough SMTP to accept one message and hand back its
// DATA payload and the envelope commands it saw.
func fakeSMTP(t *testing.T) (host string, port int, result <-chan smtpResult) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	ch := make(chan smtpResult, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		r := bufio.NewReader(conn)
		w := func(s string) { _, _ = conn.Write([]byte(s + "\r\n")) }
		w("220 fake ready")
		var res smtpResult
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSpace(line)
			res.commands = append(res.commands, cmd)
			switch verb := strings.ToUpper(strings.SplitN(cmd, " ", 2)[0]); verb {
			case "EHLO", "HELO":
				_, _ = conn.Write([]byte("250-fake\r\n250-AUTH PLAIN\r\n250 8BITMIME\r\n"))
			case "AUTH":
				w("235 authenticated")
			case "MAIL", "RCPT":
				w("250 ok")
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
				res.data = data.String()
				w("250 accepted")
			case "QUIT":
				w("221 bye")
				ch <- res
				return
			default:
				w("250 ok")
			}
		}
	}()
	h, p, _ := net.SplitHostPort(ln.Addr().String())
	pn, _ := strconv.Atoi(p)
	return h, pn, ch
}

func TestSendWritesMinimalMessageWithAttachment(t *testing.T) {
	host, port, resultCh := fakeSMTP(t)
	acc := Account{
		From: "trevor.shelf@example.com", Host: host, Port: port, Security: "none",
		Username: "trevor.shelf@example.com", Password: "app-pass",
	}
	book := bytes.Repeat([]byte("epub-bytes-"), 40) // enough to force base64 line wrapping

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := acc.Send(ctx, "trevor_7fk2@kindle.com", "The River", "", &Attachment{
		Filename: "The River - Noelle W. Ihli.epub", ContentType: "application/epub+zip",
		Content: bytes.NewReader(book),
	})
	if err != nil {
		t.Fatal(err)
	}

	res := <-resultCh
	joined := strings.Join(res.commands, "\n")
	if !strings.Contains(joined, "MAIL FROM:<trevor.shelf@example.com>") ||
		!strings.Contains(joined, "RCPT TO:<trevor_7fk2@kindle.com>") {
		t.Fatalf("envelope wrong:\n%s", joined)
	}
	if !strings.Contains(joined, "AUTH PLAIN") {
		t.Errorf("expected AUTH PLAIN, got:\n%s", joined)
	}

	// exactly the promised headers — subject, required plumbing, nothing else
	for _, want := range []string{
		"From: trevor.shelf@example.com", "To: trevor_7fk2@kindle.com",
		"Subject: The River", "MIME-Version: 1.0", "Message-ID: <",
		"Content-Type: multipart/mixed", "Content-Transfer-Encoding: base64",
		`filename="The River - Noelle W. Ihli.epub"`, "Content-Type: application/epub+zip",
	} {
		if !strings.Contains(res.data, want) {
			t.Errorf("message missing %q", want)
		}
	}
	for _, banned := range []string{"text/html", "X-Mailer", "List-Unsubscribe"} {
		if strings.Contains(res.data, banned) {
			t.Errorf("message must not contain %q", banned)
		}
	}

	// the attachment round-trips through the folded base64
	b64lines := regexp.MustCompile(`(?s)Content-Transfer-Encoding: base64\r\n\r\n(.*?)\r\n--booky-`).
		FindStringSubmatch(res.data)
	if b64lines == nil {
		t.Fatal("no base64 body found")
	}
	for _, l := range strings.Split(b64lines[1], "\r\n") {
		if len(l) > 76 {
			t.Fatalf("base64 line longer than 76 chars: %d", len(l))
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.ReplaceAll(b64lines[1], "\r\n", ""))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, book) {
		t.Fatal("attachment did not round-trip")
	}
}

func TestSendNoSubjectOmitsHeader(t *testing.T) {
	host, port, resultCh := fakeSMTP(t)
	acc := Account{From: "a@example.com", Host: host, Port: port, Security: "none"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := acc.Send(ctx, "b@example.com", "", "test body", nil); err != nil {
		t.Fatal(err)
	}
	res := <-resultCh
	if strings.Contains(res.data, "Subject:") {
		t.Error("empty subject should omit the header entirely")
	}
	if !strings.Contains(res.data, "test body") {
		t.Error("body missing")
	}
}

func TestContentTypeFor(t *testing.T) {
	if ct, ok := ContentTypeFor("epub"); !ok || ct != "application/epub+zip" {
		t.Errorf("epub → %q, %v", ct, ok)
	}
	if ct, ok := ContentTypeFor(".PDF"); !ok || ct != "application/pdf" {
		t.Errorf("pdf → %q, %v", ct, ok)
	}
	for _, f := range []string{"azw3", "mobi", "cbz", "fb2", ""} {
		if _, ok := ContentTypeFor(f); ok {
			t.Errorf("%q must not be sendable", f)
		}
	}
}
