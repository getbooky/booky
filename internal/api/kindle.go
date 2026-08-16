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
		Name           string  `json:"name"`
		Email          string  `json:"email"`
		LibraryIDs     []int64 `json:"libraryIds"`
		AutoLibraryIDs []int64 `json:"autoLibraryIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// the device may only draw from libraries its owner can reach
	a := s.access(r)
	for _, id := range req.LibraryIDs {
		if !a.mayLibrary(id) {
			writeErr(w, http.StatusForbidden, errForbidden)
			return
		}
	}
	device, err := s.Kindle.CreateDevice(owner, req.Name, req.Email, req.LibraryIDs, req.AutoLibraryIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusCreated, device)
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

// sendableFile finds the book's file in a library the device may draw from.
func (s *Server) sendableFile(bookID int64, device *kindle.Device) (path, format string, libraryID int64, err error) {
	rows, qerr := s.db.Query(`SELECT library_id, file_path, COALESCE(file_format, '')
		FROM library_books WHERE book_id = ? AND file_path IS NOT NULL AND file_path != ''`, bookID)
	if qerr != nil {
		return "", "", 0, qerr
	}
	defer rows.Close()
	found := false
	for rows.Next() {
		var lib int64
		var p, f string
		if err := rows.Scan(&lib, &p, &f); err != nil {
			continue
		}
		found = true
		if device.MayAccess(lib) {
			return p, f, lib, nil
		}
	}
	if !found {
		return "", "", 0, errors.New("this book has no file on the shelf yet")
	}
	return "", "", 0, errors.New("the book's file isn't in any of this device's libraries")
}

// POST /books/{id}/send — email the book's file to one of the caller's
// Kindle devices (any device, for an admin). The message is minimal: the
// title as subject, no body, the file attached.
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
	book, err := s.Catalog.GetBook(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("book not found"))
		return
	}
	path, format, libraryID, err := s.sendableFile(id, device)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ct, ok := mailer.ContentTypeFor(format)
	if !ok {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("this file is %s — Amazon only accepts EPUB and PDF by email", strings.ToUpper(format)))
		return
	}
	acc, err := s.Kindle.ResolveAccount(device.OwnerID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	f, err := os.Open(path) //nolint:gosec // the path came from library_books, not the request
	if err != nil {
		writeErr(w, http.StatusInternalServerError, fmt.Errorf("can't read the book file: %w", err))
		return
	}
	defer f.Close()
	if info, err := f.Stat(); err == nil && info.Size() > mailer.MaxAttachmentBytes {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%.0f MB is over Amazon's 50 MB email limit", float64(info.Size())/1e6))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	att := &mailer.Attachment{Filename: kindleFilename(book.Title, book.Author, format), ContentType: ct, Content: f}
	if err := acc.Send(ctx, device.Email, book.Title, "", att); err != nil {
		_ = s.Catalog.AddHistory(id, libraryID, "warning", fmt.Sprintf("send to kindle failed: %q → %s: %v", book.Title, device.Name, err))
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	s.Kindle.TouchSent(device.ID)
	_ = s.Catalog.AddHistory(id, libraryID, "sent", fmt.Sprintf("%q → %s (%s)", book.Title, device.Name, device.Email))
	writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
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

// ---- auto-send on import ----

// KindleAutoSend emails a freshly delivered file to every device that
// auto-sends for its library. Wired into the importer's OnFileAdded hook and
// run in the background: a slow mail server must never stall a delivery.
// Scan-matched backlogs never come through here — only new arrivals do, so
// pairing a device doesn't flood a Kindle with an existing shelf.
func (s *Server) KindleAutoSend(bookID, libraryID int64, path, format string) {
	devices, err := s.Kindle.ListDevices()
	if err != nil {
		log.Printf("kindle auto-send: list devices: %v", err)
		return
	}
	var targets []kindle.Device
	for _, d := range devices {
		if d.AutoFor(libraryID) {
			targets = append(targets, d)
		}
	}
	if len(targets) == 0 {
		return
	}
	book, err := s.Catalog.GetBook(bookID)
	if err != nil {
		return
	}
	ct, ok := mailer.ContentTypeFor(format)
	if !ok {
		_ = s.Catalog.AddHistory(bookID, libraryID, "warning",
			fmt.Sprintf("auto-send skipped: Amazon only accepts EPUB and PDF by email — %q arrived as %s", book.Title, strings.ToUpper(format)))
		return
	}
	if info, err := os.Stat(path); err != nil || info.Size() > mailer.MaxAttachmentBytes {
		_ = s.Catalog.AddHistory(bookID, libraryID, "warning",
			fmt.Sprintf("auto-send skipped: %q is over Amazon's 50 MB email limit", book.Title))
		return
	}

	for _, d := range targets {
		acc, err := s.Kindle.ResolveAccount(d.OwnerID)
		if err != nil {
			_ = s.Catalog.AddHistory(bookID, libraryID, "warning",
				fmt.Sprintf("auto-send to %s skipped: %v", d.Name, err))
			continue
		}
		f, err := os.Open(path) //nolint:gosec // library-owned path from the importer
		if err != nil {
			_ = s.Catalog.AddHistory(bookID, libraryID, "warning",
				fmt.Sprintf("auto-send to %s failed: %v", d.Name, err))
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		att := &mailer.Attachment{Filename: kindleFilename(book.Title, book.Author, format), ContentType: ct, Content: f}
		err = acc.Send(ctx, d.Email, book.Title, "", att)
		cancel()
		f.Close()
		if err != nil {
			_ = s.Catalog.AddHistory(bookID, libraryID, "warning",
				fmt.Sprintf("auto-send to %s failed: %v", d.Name, err))
			continue
		}
		s.Kindle.TouchSent(d.ID)
		_ = s.Catalog.AddHistory(bookID, libraryID, "sent",
			fmt.Sprintf("%q → %s (%s), auto", book.Title, d.Name, d.Email))
	}
}
