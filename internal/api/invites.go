package api

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"

	"github.com/getbooky/booky/internal/auth"
)

// Invite links. Admins make and revoke them; the two /invite/{token}
// endpoints are open to whoever holds a link, since that person has no
// account yet. The link itself is shown once, when it is made.

func (s *Server) handleInvites(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	invites, err := s.Auth.ListInvites()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"invites": invites})
}

func (s *Server) handleCreateInvite(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	var req struct {
		Note   string  `json:"note"`
		TagIDs []int64 `json:"tagIds"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	by := ""
	if u := s.access(r).user; u != nil {
		by = u.Username
	}
	token, inv, err := s.Auth.CreateInvite(by, req.Note, req.TagIDs)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	log.Printf("auth: %q created an invite link", by) //nolint:gosec // G706: %q escapes it
	writeJSON(w, http.StatusCreated, map[string]any{"invite": inv, "token": token})
}

// handleRevokeInvite cancels an unused link, or with ?used=1 drops a used
// one from the list. Revoking a link that turns out to have been used is
// refused and says who joined: the caller wanted someone kept out, and the
// account exists.
func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if r.URL.Query().Get("used") == "1" {
		if err := s.Auth.ForgetInvite(id); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	usedBy, err := s.Auth.RevokeInvite(id)
	switch {
	case errors.Is(err, auth.ErrInviteUsed):
		who := "someone"
		if usedBy != "" {
			who = strconv.Quote(usedBy)
		}
		writeErr(w, http.StatusConflict, fmt.Errorf("that link was already used — %s joined with it. Delete the account under Users if they shouldn't have one", who))
	case err != nil:
		writeErr(w, http.StatusNotFound, err)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// handleInviteCheck lets the sign-up page say up front whether a link still
// works. It gives away nothing about the link beyond that. The token rides
// in the body, never the address, so it stays out of proxy access logs.
func (s *Server) handleInviteCheck(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.Auth.CheckInvite(req.Token); err != nil {
		writeErr(w, inviteStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleInviteAccept creates the account the link stands for and signs it in.
func (s *Server) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token    string `json:"token"`
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// a link makes a new account for someone who has none: a signed-in
	// browser would end up holding two sessions
	if s.sessionUser(r) != nil {
		writeErr(w, http.StatusBadRequest, errors.New("you're already signed in — sign out to use an invite link"))
		return
	}
	token, user, err := s.Auth.AcceptInvite(req.Token, req.Username, req.Password)
	if err != nil {
		writeErr(w, inviteStatus(err), err)
		return
	}
	setSession(w, r, token)
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	log.Printf("auth: %q joined with an invite link from %s", user.Username, ip) //nolint:gosec // G706: %q escapes it; the address is parsed
	writeJSON(w, http.StatusCreated, user)
}

func inviteStatus(err error) int {
	if errors.Is(err, auth.ErrInviteInvalid) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
