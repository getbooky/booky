package api

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/getbooky/booky/internal/auth"
	"github.com/getbooky/booky/internal/kindle"
	"github.com/getbooky/booky/internal/mailer"
)

// Send to Kindle. Devices follow KoReader's ownership rules — everyone
// manages their own, an admin sees all — and outgoing email is strictly
// per-account: a device always sends through its OWNER's SMTP account, so an
// owner who hasn't set one up has the feature off. The SMTP password is
// write-only: no endpoint returns it, to any role.

// kindleOwner resolves the signed-in account every kindle endpoint works as.
// The pre-auth window has nobody to own a device or an SMTP account — the
// wizard creates the admin before it offers this feature.
func (s *Server) kindleOwner(w http.ResponseWriter, r *http.Request) (int64, bool) {
	a := s.access(r)
	if a.user == nil {
		writeErr(w, http.StatusForbidden, errors.New("create your account first — Send to Kindle belongs to an account"))
		return 0, false
	}
	return a.user.ID, true
}

// ownsKindleDevice mirrors ownsDevice: admins manage every device, everyone
// else only their own.
func (s *Server) ownsKindleDevice(a access, d *kindle.Device) bool {
	if a.admin() {
		return true
	}
	return a.user != nil && a.user.ID == d.OwnerID
}

// ownerHoldsTags caps a device's auto-send tags by its OWNER's tags. An admin
// may edit anyone's device, but the device must never reach past the account
// it belongs to — its sends always run as that owner. An admin owner may
// pick any tag, since they see every book anyway.
//
// Owner 0 is a KoReader device paired before accounts existed: only an admin
// can reach it (see ownsDevice), so any existing tag will do.
func (s *Server) ownerHoldsTags(ownerID int64, tagIDs []int64) error {
	var owner *auth.User
	if ownerID != 0 {
		if owner = s.Auth.UserByID(ownerID); owner == nil {
			return errors.New("the device's owner no longer exists")
		}
	}
	for _, id := range tagIDs {
		if _, err := s.Catalog.GetTag(id); err != nil {
			return fmt.Errorf("tag %d not found", id)
		}
		if owner != nil && !owner.IsAdmin() && !owner.HasTag(id) {
			return fmt.Errorf("%s doesn't hold tag %d", owner.Username, id)
		}
	}
	return nil
}

// ownerMayBook is the per-book rule run as the device's owner — not as
// whoever pressed the button. An admin sending to someone's Kindle still
// can't push them a book their tags don't cover.
func (s *Server) ownerMayBook(ownerID, bookID int64) bool {
	owner := s.Auth.UserByID(ownerID)
	if owner == nil {
		return false
	}
	return owner.IsAdmin() || s.Catalog.BookVisible(bookID, owner.Tags)
}

// ---- outgoing email (per-account) ----

func (s *Server) handleKindleSMTPGet(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.kindleOwner(w, r)
	if !ok {
		return
	}
	cfg, err := s.Kindle.GetSMTP(owner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": cfg != nil, "config": cfg})
}

func (s *Server) handleKindleSMTPPut(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.kindleOwner(w, r)
	if !ok {
		return
	}
	var req struct {
		kindle.SMTPConfig
		// Password is write-only: empty keeps the stored one.
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Kindle.SetSMTP(owner, req.SMTPConfig, req.Password); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := s.Kindle.GetSMTP(owner)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"configured": true, "config": cfg})
}

func (s *Server) handleKindleSMTPDelete(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.kindleOwner(w, r)
	if !ok {
		return
	}
	if err := s.Kindle.ClearSMTP(owner); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleKindleSMTPTest sends a small test email from the caller's own
// account, to any address they name (their Kindle, usually).
func (s *Server) handleKindleSMTPTest(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.kindleOwner(w, r)
	if !ok {
		return
	}
	var req struct {
		To string `json:"to"`
	}
	if err := decodeJSON(r, &req); err != nil || strings.TrimSpace(req.To) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("an address to send to is required"))
		return
	}
	acc, err := s.Kindle.ResolveAccount(owner)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := acc.Send(ctx, strings.TrimSpace(req.To), "Booky test",
		"Outgoing email works — books can reach this address.", nil); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// ---- devices ----

// kindleDeviceView decorates a device for the UI: the owner's name for
// admin rows, and whether the owner's outgoing email exists (a device whose
// owner has none can't receive anything — the list says why).
type kindleDeviceView struct {
	kindle.Device
	OwnerName       string `json:"ownerName,omitempty"`
	EmailConfigured bool   `json:"emailConfigured"`
}

func (s *Server) handleKindleDevices(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.kindleOwner(w, r); !ok {
		return
	}
	a := s.access(r)
	devices, err := s.Kindle.ListDevices()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	owners := map[int64]string{}
	if a.admin() {
		if users, err := s.Auth.ListUsers(); err == nil {
			for _, u := range users {
				owners[u.ID] = u.Username
			}
		}
	}
	emailSet := map[int64]bool{}
	visible := []kindleDeviceView{}
	for i := range devices {
		d := devices[i]
		if !s.ownsKindleDevice(a, &d) {
			continue
		}
		if _, probed := emailSet[d.OwnerID]; !probed {
			cfg, _ := s.Kindle.GetSMTP(d.OwnerID)
			emailSet[d.OwnerID] = cfg != nil
		}
		view := kindleDeviceView{Device: d, EmailConfigured: emailSet[d.OwnerID]}
		if a.admin() {
			view.OwnerName = owners[d.OwnerID]
		}
		visible = append(visible, view)
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": visible})
}

func (s *Server) handleKindleCreateDevice(w http.ResponseWriter, r *http.Request) {
	owner, ok := s.kindleOwner(w, r)
	if !ok {
		return
	}
	var req struct {
		Name       string  `json:"name"`
		Email      string  `json:"email"`
		AutoTagIDs []int64 `json:"autoTagIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// the device may only auto-send tags its owner holds
	if err := s.ownerHoldsTags(owner, req.AutoTagIDs); err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	device, err := s.Kindle.CreateDevice(owner, req.Name, req.Email, req.AutoTagIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, device)
}

// handleKindleUpdateDevice edits a device in place — change its auto-send
// tags, fix a typo'd address — without re-pairing. Adding an auto-send tag
// doesn't email the backlog: only books that gain the tag (or land on disk
// with it) from here on go out.
func (s *Server) handleKindleUpdateDevice(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.kindleOwner(w, r); !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	a := s.access(r)
	device, err := s.Kindle.GetDevice(id)
	if err != nil || !s.ownsKindleDevice(a, device) {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	var req struct {
		Name       string  `json:"name"`
		Email      string  `json:"email"`
		AutoTagIDs []int64 `json:"autoTagIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.ownerHoldsTags(device.OwnerID, req.AutoTagIDs); err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	s.markBacklogSent(device, req.AutoTagIDs)
	updated, err := s.Kindle.UpdateDevice(id, req.Name, req.Email, req.AutoTagIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) handleKindleRemoveDevice(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.kindleOwner(w, r); !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	device, err := s.Kindle.GetDevice(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	// same shape as the plugin-zip check: not yours reads as not there
	if !s.ownsKindleDevice(s.access(r), device) {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	if err := s.Kindle.RemoveDevice(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleKindleDeviceTest emails a test message to the device — through its
// owner's account, exactly like a real send would go.
func (s *Server) handleKindleDeviceTest(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.kindleOwner(w, r); !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	device, err := s.Kindle.GetDevice(id)
	if err != nil || !s.ownsKindleDevice(s.access(r), device) {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	acc, err := s.Kindle.ResolveAccount(device.OwnerID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := acc.Send(ctx, device.Email, "Booky test",
		"Outgoing email works — books can reach this Kindle. If nothing shows up on the device, check the approved sender list on Amazon.", nil); err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// ---- sending a book ----

// shelfFile returns the book's file, if it has one on disk.
func (s *Server) shelfFile(bookID int64) (path, format string, err error) {
	err = s.db.QueryRow(`SELECT file_path, COALESCE(file_format, '') FROM library_books
		WHERE book_id = ? AND file_path IS NOT NULL AND file_path != '' LIMIT 1`, bookID).Scan(&path, &format)
	if err != nil {
		return "", "", errors.New("this book has no file on the shelf yet")
	}
	return path, format, nil
}

// POST /books/{id}/send — email the book's file to one of the caller's
// Kindle devices (any device, for an admin). The book must be one the
// device's OWNER can see right now. The message is minimal: the title as
// subject, no body, the file attached. A manual send always goes, even if
// the book went to this device before — asking twice is the person's call.
func (s *Server) handleBookSendToKindle(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.kindleOwner(w, r); !ok {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if !s.requireBook(w, r, id) {
		return
	}
	var req struct {
		DeviceID int64 `json:"deviceId"`
	}
	if err := decodeJSON(r, &req); err != nil || req.DeviceID == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("deviceId required"))
		return
	}
	device, err := s.Kindle.GetDevice(req.DeviceID)
	if err != nil || !s.ownsKindleDevice(s.access(r), device) {
		writeErr(w, http.StatusNotFound, errors.New("device not found"))
		return
	}
	if !s.ownerMayBook(device.OwnerID, id) {
		writeErr(w, http.StatusForbidden, errors.New("this book isn't shared with the device's owner"))
		return
	}
	if err := s.sendToKindle(r.Context(), device, id, false); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}

// sendToKindle emails one book to one device through the owner's account.
// auto marks a send-on-import/tag: those are recorded as "auto" and given
// back to the send log if the mail fails, so a later trigger can retry.
func (s *Server) sendToKindle(ctx context.Context, device *kindle.Device, bookID int64, auto bool) error {
	lib := s.Catalog.TheLibrary()
	book, err := s.Catalog.GetBook(bookID)
	if err != nil {
		return errors.New("book not found")
	}
	path, format, err := s.shelfFile(bookID)
	if err != nil {
		return err
	}
	ct, ok := mailer.ContentTypeFor(format)
	if !ok {
		return fmt.Errorf("%q is %s — Amazon only accepts EPUB and PDF by email", book.Title, strings.ToUpper(format))
	}
	acc, err := s.Kindle.ResolveAccount(device.OwnerID)
	if err != nil {
		return err
	}
	f, err := os.Open(path) //nolint:gosec // the path came from library_books, not the request
	if err != nil {
		return fmt.Errorf("can't read the book file: %w", err)
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > mailer.MaxAttachmentBytes {
		return fmt.Errorf("%q is %.0f MB — over Amazon's 50 MB email limit", book.Title, float64(info.Size())/1e6)
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	att := &mailer.Attachment{Filename: kindleFilename(book.Title, book.Author, format), ContentType: ct, Content: f}
	if err := acc.Send(ctx, device.Email, book.Title, "", att); err != nil {
		_ = s.Catalog.AddHistory(bookID, lib, "warning", fmt.Sprintf("send to kindle failed: %q → %s: %v", book.Title, device.Name, err))
		return err
	}
	s.Kindle.TouchSent(device.ID)
	how := ""
	if auto {
		how = ", auto"
	}
	_ = s.Catalog.AddHistory(bookID, lib, "sent", fmt.Sprintf("%q → %s (%s)%s", book.Title, device.Name, device.Email, how))
	return nil
}

// kindleFilename names the attachment "Title - Author.ext" with filesystem
// junk stripped; Amazon titles the document from the file's own metadata, so
// this only has to be tidy, not perfect.
func kindleFilename(title, author, format string) string {
	name := title
	if author != "" {
		name += " - " + author
	}
	name = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return ' '
		}
		return r
	}, name)
	name = strings.Join(strings.Fields(name), " ")
	if name == "" {
		name = "book"
	}
	return name + "." + strings.ToLower(strings.TrimPrefix(format, "."))
}

// ---- auto-send: on import and on tag ----
//
// A device auto-sends a book once, the first moment all of these hold:
//   - the book is on disk;
//   - it carries one of the device's auto-send tags;
//   - the device's owner holds that tag (checked again right before sending,
//     so taking a tag away stops anything still queued);
//   - the send log has no record of it going to this device.
//
// Sends go through one paced worker: a bulk tag edit or an import burst
// never hits a mail server with dozens of messages at once.

// kindlePace spaces auto-send emails; a var so tests can run unpaced.
var kindlePace = 3 * time.Second

type kindleJob struct {
	deviceID int64
	bookID   int64
}

// autoTargets lists the devices that should receive bookID because it
// carries tags (the triggering ones, or all of the book's), and haven't yet.
func (s *Server) autoTargets(bookID int64, tags []int64) []kindle.Device {
	devices, err := s.Kindle.ListDevices()
	if err != nil {
		log.Printf("kindle auto-send: list devices: %v", err)
		return nil
	}
	var out []kindle.Device
	for _, d := range devices {
		if s.autoEligible(&d, bookID, tags) && !s.Kindle.WasSent(d.ID, bookID) {
			out = append(out, d)
		}
	}
	return out
}

// autoEligible: the device auto-sends one of tags, and its owner holds that
// tag right now. A device whose owner is gone (or never existed — owner 0)
// sends nothing.
func (s *Server) autoEligible(d *kindle.Device, bookID int64, tags []int64) bool {
	owner := s.Auth.UserByID(d.OwnerID)
	if owner == nil {
		return false
	}
	bookTags, err := s.Catalog.BookTags(bookID)
	if err != nil {
		return false
	}
	carried := map[int64]bool{}
	for _, t := range bookTags {
		carried[t] = true
	}
	for _, t := range tags {
		if !carried[t] || !d.AutoFor([]int64{t}) {
			continue
		}
		if owner.IsAdmin() || owner.HasTag(t) {
			return true
		}
	}
	return false
}

// KindleOnImport runs when a file lands: every device auto-sending one of the
// book's tags gets it. Wired into the importer's OnFileAdded hook.
func (s *Server) KindleOnImport(bookID int64) {
	tags, err := s.Catalog.BookTags(bookID)
	if err != nil || len(tags) == 0 {
		return
	}
	for _, d := range s.autoTargets(bookID, tags) {
		s.enqueueKindle(kindleJob{deviceID: d.ID, bookID: bookID})
	}
}

// KindleOnTagged runs when a book gains tags. A book not on disk yet waits:
// KindleOnImport catches it when the file lands. send=false records the
// sends as handled without emailing — the person declined a bulk send.
func (s *Server) KindleOnTagged(bookID int64, added []int64, send bool) {
	if _, _, err := s.shelfFile(bookID); err != nil {
		return
	}
	for _, d := range s.autoTargets(bookID, added) {
		if !send {
			if s.Kindle.ClaimSend(d.ID, bookID) {
				_ = s.Catalog.AddHistory(bookID, s.Catalog.TheLibrary(), "info",
					fmt.Sprintf("%s: not sent to %s (declined when tagging)", s.bookTitle(bookID), d.Name))
			}
			continue
		}
		s.enqueueKindle(kindleJob{deviceID: d.ID, bookID: bookID})
	}
}

// kindleTagPreview counts the emails tagging bookIDs with tagIDs would set
// off, and across how many devices — the bulk prompt's numbers.
func (s *Server) kindleTagPreview(bookIDs, tagIDs []int64) (sends, devices int) {
	all, err := s.Kindle.ListDevices()
	if err != nil {
		return 0, 0
	}
	hit := map[int64]bool{}
	for _, bookID := range bookIDs {
		if _, _, err := s.shelfFile(bookID); err != nil {
			continue
		}
		for _, d := range all {
			if !d.AutoFor(tagIDs) || s.Kindle.WasSent(d.ID, bookID) {
				continue
			}
			owner := s.Auth.UserByID(d.OwnerID)
			if owner == nil {
				continue
			}
			ok := false
			for _, t := range tagIDs {
				if d.AutoFor([]int64{t}) && (owner.IsAdmin() || owner.HasTag(t)) {
					ok = true
				}
			}
			if ok {
				sends++
				hit[d.ID] = true
			}
		}
	}
	return sends, len(hit)
}

// markBacklogSent: when a device gains an auto-send tag, the books already
// carrying it count as sent. Adding "Kids" to a Kindle means "send new Kids
// books", not "email the whole Kids shelf right now".
func (s *Server) markBacklogSent(d *kindle.Device, newAuto []int64) {
	old := map[int64]bool{}
	for _, t := range d.AutoTagIDs {
		old[t] = true
	}
	for _, t := range newAuto {
		if old[t] {
			continue
		}
		_, _ = s.db.Exec(`INSERT OR IGNORE INTO kindle_sent (device_id, book_id)
			SELECT ?, bt.book_id FROM book_tags bt
			JOIN library_books lb ON lb.book_id = bt.book_id
			WHERE bt.tag_id = ? AND lb.file_path IS NOT NULL AND lb.file_path != ''`, d.ID, t)
	}
}

func (s *Server) enqueueKindle(j kindleJob) {
	s.kindleOnce.Do(func() {
		s.kindleJobs = make(chan kindleJob, 4096)
		go s.kindleWorker()
	})
	select {
	case s.kindleJobs <- j:
	default:
		log.Printf("kindle auto-send: queue full, dropping book %d → device %d (it goes on the next trigger)", j.bookID, j.deviceID)
	}
}

func (s *Server) kindleWorker() {
	for j := range s.kindleJobs {
		s.runKindleJob(j)
		time.Sleep(kindlePace)
	}
}

// runKindleJob re-checks everything at send time — the owner may have lost
// the tag, the device may be gone — then claims the send log row, sends, and
// gives the claim back if the mail fails.
func (s *Server) runKindleJob(j kindleJob) {
	d, err := s.Kindle.GetDevice(j.deviceID)
	if err != nil {
		return
	}
	tags, err := s.Catalog.BookTags(j.bookID)
	if err != nil || !s.autoEligible(d, j.bookID, tags) {
		return
	}
	if !s.Kindle.ClaimSend(d.ID, j.bookID) {
		return // already sent, or another trigger is sending it
	}
	if err := s.sendToKindle(context.Background(), d, j.bookID, true); err != nil {
		s.Kindle.ReleaseSend(d.ID, j.bookID)
		_ = s.Catalog.AddHistory(j.bookID, s.Catalog.TheLibrary(), "warning",
			fmt.Sprintf("auto-send to %s skipped: %v", d.Name, err))
	}
}
