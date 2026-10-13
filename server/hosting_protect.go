package server

import (
	"net/http"
)

// ---- branch protection ----

// handleGetProtectedBranches returns the repository's protected branches: the
// set that may only change through a merged Revision Request.
func (s *Server) handleGetProtectedBranches(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	branches, err := repo.ProtectedBranches()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if branches == nil {
		branches = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"protected_branches": branches})
}

// handleSetProtectedBranches replaces the protected set (owner action). An empty
// list clears protection. It never touches the refs themselves.
func (s *Server) handleSetProtectedBranches(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	req, err := decodeJSONBody[struct {
		ProtectedBranches []string `json:"protected_branches"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := repo.SetProtectedBranches(req.ProtectedBranches); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Echo the stored set (normalised: trimmed + de-duplicated).
	branches, err := repo.ProtectedBranches()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if branches == nil {
		branches = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"protected_branches": branches})
}
