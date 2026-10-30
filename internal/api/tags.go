package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/getbooky/booky/internal/catalog"
)

// Tags are who-sees-what. Only an admin changes them: a tag on a book decides
// which accounts, OPDS feeds, KoReader devices and Kindles it reaches, so
// editing one is sharing — the same call Reely leaves to its admin.
// Everyone else gets read access to the tags they hold, for filters and for
// picking an audience when they add a book.

// tagView is a tag as the caller may see it: members are the admin's
// business, so a plain user gets names only.
type tagView struct {
	catalog.Tag
	Personal bool `json:"personal"`
}

func (s *Server) handleTags(w http.ResponseWriter, r *http.Request) {
	tags, err := s.Catalog.ListTags()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	a := s.access(r)
	out := []tagView{}
	for _, t := range tags {
		if !a.admin() {
			if !a.user.HasTag(t.ID) {
				continue
			}
			t.MemberIDs = nil
			t.BookCount = 0
		}
		out = append(out, tagView{Tag: t, Personal: t.Personal()})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": out})
}

func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Name      string  `json:"name"`
		MemberIDs []int64 `json:"memberIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id, err := s.Catalog.CreateTag(req.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(req.MemberIDs) > 0 {
		if err := s.setTagMembers(id, req.MemberIDs); err != nil {
			_ = s.Catalog.DeleteTag(id)
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusCreated, map[string]int64{"id": id})
}

func (s *Server) handleRenameTag(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Catalog.RenameTag(id, req.Name); err != nil {
		writeErr(w, tagErrStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if err := s.Catalog.DeleteTag(id); err != nil {
		writeErr(w, tagErrStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

func tagErrStatus(err error) int {
	switch {
	case errors.Is(err, catalog.ErrTagNotFound):
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

// handleSetTagMembers replaces who holds a shared tag — the tag-centric twin
// of PUT /users/{id}/tags, for the Tags settings page.
func (s *Server) handleSetTagMembers(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req struct {
		MemberIDs []int64 `json:"memberIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.setTagMembers(id, req.MemberIDs); err != nil {
		writeErr(w, tagErrStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) setTagMembers(tagID int64, memberIDs []int64) error {
	t, err := s.Catalog.GetTag(tagID)
	if err != nil {
		return err
	}
	if t.Personal() {
		return catalog.ErrTagPersonal
	}
	want := map[int64]bool{}
	for _, id := range memberIDs {
		if s.Auth.UserByID(id) == nil {
			return fmt.Errorf("user %d not found", id)
		}
		want[id] = true
	}
	users, err := s.Auth.ListUsers()
	if err != nil {
		return err
	}
	// each account's shared tags are rewritten whole through the one
	// validated path, so this can never grant someone a personal tag
	for _, u := range users {
		has := u.HasTag(tagID)
		if has == want[u.ID] {
			continue
		}
		next := []int64{}
		for _, id := range u.Tags {
			if id != tagID && id != u.PersonalTag {
				next = append(next, id)
			}
		}
		if want[u.ID] {
			next = append(next, tagID)
		}
		if err := s.Auth.SetUserTags(u.ID, next); err != nil {
			return err
		}
	}
	return nil
}

// handleSetBookTags replaces one book's tags. A single book is never a flood,
// so send-on-tag always goes ahead.
func (s *Server) handleSetBookTags(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	var req struct {
		TagIDs []int64 `json:"tagIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if _, err := s.Catalog.GetBook(id); err != nil {
		writeErr(w, http.StatusNotFound, errors.New("book not found"))
		return
	}
	ch, err := s.Catalog.SetBookTags(id, req.TagIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.tagsChanged([]catalog.TagChange{ch}, true)
	tags, _ := s.Catalog.BookTags(id)
	writeJSON(w, http.StatusOK, map[string]any{"tagIds": tags})
}

// kindleBulkPrompt is how many Kindle emails a bulk tag edit may set off
// before it asks first: tagging 200 old books "Kids" shouldn't silently fire
// 200 emails at a Kindle.
const kindleBulkPrompt = 5

// handleBulkTags edits tags across a selection. When the edit would email
// more than a handful of books to Kindles, the first call changes nothing
// and answers with the count; the client asks the person and repeats the
// call with sendToKindle set either way.
func (s *Server) handleBulkTags(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		BookIDs []int64 `json:"bookIds"`
		TagIDs  []int64 `json:"tagIds"`
		// add | remove | replace
		Mode         string `json:"mode"`
		SendToKindle *bool  `json:"sendToKindle"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Mode = strings.ToLower(req.Mode)
	if req.Mode != "add" && req.Mode != "remove" && req.Mode != "replace" {
		writeErr(w, http.StatusBadRequest, errors.New("mode must be add, remove or replace"))
		return
	}
	if len(req.BookIDs) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("no books selected"))
		return
	}
	for _, id := range req.TagIDs {
		if _, err := s.Catalog.GetTag(id); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("tag %d not found", id))
			return
		}
	}
	if req.Mode != "remove" && req.SendToKindle == nil {
		if n, devices := s.kindleTagPreview(req.BookIDs, req.TagIDs); n > kindleBulkPrompt {
			writeJSON(w, http.StatusOK, map[string]any{
				"needsConfirm": true, "kindleSends": n, "kindleDevices": devices,
			})
			return
		}
	}
	send := req.SendToKindle == nil || *req.SendToKindle
	var changes []catalog.TagChange
	for _, bookID := range req.BookIDs {
		var ch catalog.TagChange
		var err error
		switch req.Mode {
		case "add":
			ch, err = s.Catalog.AddBookTags(bookID, req.TagIDs)
		case "remove":
			ch, err = s.Catalog.RemoveBookTags(bookID, req.TagIDs)
		case "replace":
			ch, err = s.Catalog.SetBookTags(bookID, req.TagIDs)
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("book %d: %w", bookID, err))
			return
		}
		changes = append(changes, ch)
	}
	s.tagsChanged(changes, send)
	writeJSON(w, http.StatusOK, map[string]any{"updated": len(changes)})
}

// tagsChanged is the one place every tag edit reports to. It records what
// changed and hands new tags to send-on-tag; send=false (the person declined
// a bulk send) marks those books as handled for the devices instead, so they
// don't trickle out later behind the person's back.
func (s *Server) tagsChanged(changes []catalog.TagChange, send bool) {
	for _, ch := range changes {
		if len(ch.Added) == 0 && len(ch.Removed) == 0 {
			continue
		}
		_ = s.Catalog.AddHistory(ch.BookID, s.Catalog.TheLibrary(), "tagged", s.describeTagChange(ch))
		if len(ch.Added) > 0 {
			s.KindleOnTagged(ch.BookID, ch.Added, send)
		}
	}
}

func (s *Server) describeTagChange(ch catalog.TagChange) string {
	name := func(id int64) string {
		if t, err := s.Catalog.GetTag(id); err == nil {
			return t.Name
		}
		return fmt.Sprintf("#%d", id)
	}
	var parts []string
	for _, id := range ch.Added {
		parts = append(parts, "+"+name(id))
	}
	for _, id := range ch.Removed {
		parts = append(parts, "−"+name(id))
	}
	return s.bookTitle(ch.BookID) + ": " + strings.Join(parts, ", ")
}
