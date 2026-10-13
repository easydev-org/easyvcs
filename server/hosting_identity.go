package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// ---- M1: identity / tokens ----

func (s *Server) handleWhoami(w http.ResponseWriter, r *http.Request) {
	_, u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
		return
	}
	if u == nil {
		writeJSON(w, http.StatusOK, map[string]any{"username": "anonymous", "authenticated": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
		"disabled": u.Disabled, "authenticated": true,
	})
}

func (s *Server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := s.cs.ListUsers()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	limit := limitFrom(r)
	out := make([]map[string]any, 0, len(users))
	for i, u := range users {
		if i >= limit {
			break
		}
		out = append(out, userView(u))
	}
	writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	req, err := decodeJSONBody[struct {
		Username    string `json:"username"`
		DisplayName string `json:"display_name"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Username == "" {
		writeErr(w, http.StatusBadRequest, errors.New("username is required"))
		return
	}
	u, err := s.cs.CreateUser(req.Username, req.DisplayName)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	// Mint a one-time write token so the new user can authenticate immediately.
	plaintext, err := newToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, err := s.cs.CreateToken(plaintext, u.ID, "write"); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userView(u), "token": plaintext})
}

// handleMintUserToken mints a NEW token for an EXISTING user (admin only). The
// plaintext is returned ONCE and existing tokens are NOT revoked (a user may
// hold several). This is the admin escape hatch for re-issuing a token when the
// only plaintext copy was lost — e.g. a gateway that dropped its local
// tenant-token cache: POST /users refuses an existing username (409) and never
// re-returns a token, so without this endpoint there is no way to obtain a
// fresh token for a user that already exists.
func (s *Server) handleMintUserToken(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid user id %q", r.PathValue("id")))
		return
	}
	// The body is OPTIONAL (level defaults to write). An empty body decodes as
	// io.EOF; treat that as "no level given" rather than a bad request.
	req, err := decodeJSONBody[struct {
		Level string `json:"level"`
	}](r)
	if err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Level == "" {
		req.Level = "write"
	}
	u, err := s.cs.GetUser(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	plaintext, err := newToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	t, err := s.cs.CreateToken(plaintext, u.ID, req.Level)
	if err != nil {
		// A bad level is the only client error CreateToken reports.
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userView(u), "token": plaintext, "level": t.Level})
}

func (s *Server) handlePatchUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid user id %q", r.PathValue("id")))
		return
	}
	req, err := decodeJSONBody[struct {
		DisplayName *string `json:"display_name"`
		Disabled    *bool   `json:"disabled"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	u, err := s.cs.UpdateUser(id, req.DisplayName, req.Disabled)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": userView(u)})
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid user id %q", r.PathValue("id")))
		return
	}
	if err := s.cs.DeleteUser(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request) {
	tk, u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
		return
	}
	if u == nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: a token is required"))
		return
	}
	toks, err := s.cs.ListTokens(u.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(toks))
	for _, t := range toks {
		out = append(out, map[string]any{
			"id": t.ID, "level": t.Level, "created": t.Created.UnixMilli(), "current": t.ID == tk.ID,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request) {
	_, u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
		return
	}
	if u == nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: a token is required"))
		return
	}
	req, err := decodeJSONBody[struct {
		Level string `json:"level"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Level == "" {
		req.Level = "write"
	}
	plaintext, err := newToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	t, err := s.cs.CreateToken(plaintext, u.ID, req.Level)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": t.ID, "level": t.Level, "token": plaintext})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request) {
	_, u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
		return
	}
	if u == nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: a token is required"))
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid token id %q", r.PathValue("id")))
		return
	}
	if err := s.cs.DeleteTokenByID(id, u.ID); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
