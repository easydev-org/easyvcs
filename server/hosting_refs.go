package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// ---- M3: refs ----

// scopePath reads the ?path= query (a sub-repo scope). "" = the root scope.
func scopePath(r *http.Request) string {
	return strings.Trim(strings.TrimSpace(r.URL.Query().Get("path")), "/")
}

func (s *Server) handleListRefs(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	// A ?path= scopes the listing (docs/subrepo-scopes.md): the root's refs
	// project down (when the dir exists at the ref's tip) plus the scope's own.
	path := scopePath(r)
	refs, err := revision.NewWorkspace(repo).ListRefsIn(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(refs))
	for _, ref := range refs {
		out = append(out, refView(ref))
	}
	writeJSON(w, http.StatusOK, map[string]any{"refs": out})
}

func (s *Server) handleListBranches(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	refs, err := revision.NewWorkspace(repo).ListRefsIn(scopePath(r))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := []map[string]any{}
	for _, ref := range refs {
		if ref.Kind != store.RefBranch {
			continue
		}
		entry := refView(ref)
		if rev, err := repo.GetRevision(ref.Target); err == nil {
			entry["sha"] = rev.Hash.String()
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"branches": out})
}

func (s *Server) handleCreateBranch(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	req, err := decodeJSONBody[struct {
		Name string `json:"name"`
		From string `json:"from"`
		// Path scopes the branch to a sub-repo ("" = root). May also be given as
		// ?path= (docs/subrepo-scopes.md).
		Path string `json:"path"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	path := strings.Trim(strings.TrimSpace(req.Path), "/")
	if path == "" {
		path = scopePath(r)
	}
	ws := revision.NewWorkspace(repo)
	// Idempotent: an existing branch (in this scope) is returned unchanged.
	if cur, err := ws.GetRefIn(path, req.Name); err == nil && cur != nil && cur.Kind == store.RefBranch {
		writeJSON(w, http.StatusOK, refView(cur))
		return
	}
	fromID, err := resolveRefRevisionID(repo, ws, req.From)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// A branch owns a derived revision (fresh id, same content), so an in-place
	// amend on the branch never mutates the revision another branch shares.
	_, ch, err := ws.Derive(fromID, "", true)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ref, err := ws.SetRefIn(path, req.Name, store.RefBranch, ch.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, refView(ref))
}

func (s *Server) handleDeleteBranch(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	branch := r.PathValue("branch")
	if branch == "" {
		writeErr(w, http.StatusBadRequest, errors.New("branch is required"))
		return
	}
	ws := revision.NewWorkspace(repo)
	if err := ws.DeleteRefIn(scopePath(r), branch); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "branch": branch, "path": scopePath(r)})
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	refs, err := repo.ListRefs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := []map[string]any{}
	for _, ref := range refs {
		if ref.Kind != store.RefTag {
			continue
		}
		out = append(out, refView(ref))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": out})
}

func (s *Server) handleCreateTag(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	req, err := decodeJSONBody[struct {
		Name   string `json:"name"`
		Target string `json:"target"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	ws := revision.NewWorkspace(repo)
	targetID, err := resolveRefRevisionID(repo, ws, req.Target)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// A tag is immutable: SetRef refuses to move an existing tag.
	ref, err := ws.SetRef(req.Name, store.RefTag, targetID)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, refView(ref))
}
