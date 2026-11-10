package api

import (
	"errors"
	"log"
	"net"
	"net/http"

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
	log.Printf("auth: %s created an invite link", by) //nolint:gosec // G706: a validated username
	writeJSON(w, http.StatusCreated, map[string]any{"invite": inv, "token": token})
}

func (s *Server) handleRevokeInvite(w http.ResponseWriter, r *http.Request) {
	if !s.requireAdmin(w, r) {
		return
	}
	id, err := pathID(r, "id")
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad id"))
		return
	}
	if err := s.Auth.RevokeInvite(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleInviteCheck lets the sign-up page say up front whether a link still
// works. It gives away nothing about the link beyond that.
func (s *Server) handleInviteCheck(w http.ResponseWriter, r *http.Request) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if err := s.Auth.CheckInvite(r.PathValue("token"), ip); err != nil {
		writeErr(w, inviteStatus(err), err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleInviteAccept creates the account the link stands for and signs it in.
func (s *Server) handleInviteAccept(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	token, user, err := s.Auth.AcceptInvite(r.PathValue("token"), req.Username, req.Password, ip)
	if err != nil {
		writeErr(w, inviteStatus(err), err)
		return
	}
	setSession(w, r, token)
	log.Printf("auth: %s joined with an invite link from %s", user.Username, ip) //nolint:gosec // G706: a validated username and a parsed address
	writeJSON(w, http.StatusCreated, user)
}

func inviteStatus(err error) int {
	if errors.Is(err, auth.ErrInviteInvalid) {
		return http.StatusNotFound
	}
	return http.StatusBadRequest
}
