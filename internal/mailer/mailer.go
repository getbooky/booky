// Package mailer sends a book file to an email address over SMTP — the whole
// of Send to Kindle's transport. Messages are deliberately bare: required
// headers, a subject, an empty body, and the attachment. Amazon reads the
// attached file and ignores everything else; the subject exists for the
// SENDING provider (subject-less mail scores worse with spam filters, and it
// keeps the account's Sent folder legible).
//
// Standard library only: net/smtp for the session, mime/multipart for the
// message, and a streaming base64 writer so a 50MB book never has to sit in
// memory encoded.
package mailer

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net"
	"net/smtp"
	"strings"
	"time"
)

// Account is a resolved, sendable SMTP account (password decrypted).
type Account struct {
	From     string
	Host     string
	Port     int
	Security string // starttls | tls | none
	Username string
	Password string
}

// Attachment is the file to send. Content is streamed, not buffered.
type Attachment struct {
	Filename    string
	ContentType string
	Content     io.Reader
}

// ContentTypeFor maps a book format to its attachment MIME type. Only the
// formats Amazon accepts by email are sendable.
func ContentTypeFor(format string) (string, bool) {
	switch strings.ToLower(strings.TrimPrefix(format, ".")) {
	case "epub":
		return "application/epub+zip", true
	case "pdf":
		return "application/pdf", true
	}
	return "", false
}

// MaxAttachmentBytes is Amazon's documented Send-to-Kindle email limit.
const MaxAttachmentBytes = 50 << 20

// Send delivers one message with an optional attachment. The context bounds
// the whole SMTP conversation.
func (a Account) Send(ctx context.Context, to, subject, body string, att *Attachment) error {
	if a.Host == "" || a.From == "" {
		return fmt.Errorf("outgoing email isn't configured")
	}
	port := a.Port
	if port <= 0 {
		port = 587
	}
	addr := net.JoinHostPort(a.Host, fmt.Sprint(port))

	d := net.Dialer{}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	// one deadline for the whole conversation; smtp.Client has no ctx support
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(2 * time.Minute))
	}
	if a.Security == "tls" {
		conn = tls.Client(conn, &tls.Config{ServerName: a.Host, MinVersion: tls.VersionTLS12})
	}
	c, err := smtp.NewClient(conn, a.Host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()

	if a.Security == "starttls" {
		if err := c.StartTLS(&tls.Config{ServerName: a.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return fmt.Errorf("STARTTLS: %w", err)
		}
	}
	if a.Username != "" {
		if err := c.Auth(a.auth()); err != nil {
			return fmt.Errorf("login rejected: %w", err)
		}
	}
	if err := c.Mail(a.From); err != nil {
		return err
	}
	if err := c.Rcpt(to); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if err := writeMessage(w, a.From, to, subject, body, att); err != nil {
		w.Close()
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// auth picks PLAIN over TLS and a permissive variant otherwise: net/smtp's
// PlainAuth refuses unencrypted connections, but security "none" is an
// explicit choice for LAN relays — honoring it beats failing it.
func (a Account) auth() smtp.Auth {
	if a.Security == "none" {
		return unencryptedPlain{user: a.Username, pass: a.Password}
	}
	return smtp.PlainAuth("", a.Username, a.Password, a.Host)
}

type unencryptedPlain struct{ user, pass string }

func (p unencryptedPlain) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + p.user + "\x00" + p.pass), nil
}
func (p unencryptedPlain) Next(_ []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("unexpected server challenge")
	}
	return nil, nil
}

func writeMessage(w io.Writer, from, to, subject, body string, att *Attachment) error {
	id := make([]byte, 12)
	if _, err := rand.Read(id); err != nil {
		return err
	}
	domain := from
	if i := strings.LastIndex(from, "@"); i >= 0 {
		domain = from[i+1:]
	}
	boundary := "booky-" + hex.EncodeToString(id)

	head := &strings.Builder{}
	fmt.Fprintf(head, "From: %s\r\n", from)
	fmt.Fprintf(head, "To: %s\r\n", to)
	if subject != "" {
		fmt.Fprintf(head, "Subject: %s\r\n", mime.QEncoding.Encode("utf-8", subject))
	}
	fmt.Fprintf(head, "Date: %s\r\n", time.Now().Format(time.RFC1123Z))
	fmt.Fprintf(head, "Message-ID: <%s@%s>\r\n", hex.EncodeToString(id), domain)
	head.WriteString("MIME-Version: 1.0\r\n")
	fmt.Fprintf(head, "Content-Type: multipart/mixed; boundary=%q\r\n", boundary)
	head.WriteString("\r\n")
	if _, err := io.WriteString(w, head.String()); err != nil {
		return err
	}

	// text part first, even when empty — a lone attachment with no text part
	// trips more spam heuristics than a standard multipart shape
	if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", boundary, body); err != nil {
		return err
	}

	if att != nil {
		disp := mime.FormatMediaType("attachment", map[string]string{"filename": att.Filename})
		if _, err := fmt.Fprintf(w, "--%s\r\nContent-Type: %s\r\nContent-Disposition: %s\r\nContent-Transfer-Encoding: base64\r\n\r\n",
			boundary, att.ContentType, disp); err != nil {
			return err
		}
		enc := base64.NewEncoder(base64.StdEncoding, &lineWrapper{w: w})
		if _, err := io.Copy(enc, att.Content); err != nil {
			return err
		}
		if err := enc.Close(); err != nil {
			return err
		}
		if _, err := io.WriteString(w, "\r\n"); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "--%s--\r\n", boundary)
	return err
}

// lineWrapper folds a base64 stream at 76 columns (RFC 2045).
type lineWrapper struct {
	w   io.Writer
	col int
}

func (lw *lineWrapper) Write(p []byte) (int, error) {
	written := 0
	for len(p) > 0 {
		room := 76 - lw.col
		if room == 0 {
			if _, err := lw.w.Write([]byte("\r\n")); err != nil {
				return written, err
			}
			lw.col = 0
			continue
		}
		n := min(room, len(p))
		m, err := lw.w.Write(p[:n])
		written += m
		if err != nil {
			return written, err
		}
		lw.col += m
		p = p[n:]
	}
	return written, nil
}
