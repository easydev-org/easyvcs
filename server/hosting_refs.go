package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// ---- M3: refs ----

// scopePath is the ONE way a request names its scope: the ?path= query is a
// repo-relative directory ("" = the root scope). Every scoped endpoint —
// refs/branches/tags, commit/snapshot, rr, and the browse views (tree/blob/raw/
// log/blame) — reads the scope from HERE, so there is a single coordinate
// system and a single parameter (docs/namespace-container.md).
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
	ids := make([]string, 0, len(refs))
	for _, ref := range refs {
		if ref.Kind == store.RefBranch {
			ids = append(ids, ref.Target)
		}
	}
	revs, _ := repo.RevisionsByIDs(ids)
	out := []map[string]any{}
	for _, ref := range refs {
		if ref.Kind != store.RefBranch {
			continue
		}
		entry := refView(ref)
		if rev, ok := revs[ref.Target]; ok {
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
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	// The SCOPE is always the ?path= query (docs/subrepo-scopes.md): one
	// concept, one place.
	path := scopePath(r)
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
	s.advCache.invalidate(repo.NamespaceID())
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
	s.advCache.invalidate(repo.NamespaceID())
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "branch": branch, "path": scopePath(r)})
}

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	// A ?path= scopes the listing (projected tags + the scope's own).
	refs, err := revision.NewWorkspace(repo).ListRefsIn(scopePath(r))
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
		// Path scopes the tag to a subdirectory ("" = root).
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
	targetID, err := resolveRefRevisionID(repo, ws, req.Target)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// A tag is immutable: SetRef refuses to move an existing tag (within its scope).
	ref, err := ws.SetRefIn(path, req.Name, store.RefTag, targetID)
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	s.advCache.invalidate(repo.NamespaceID())
	writeJSON(w, http.StatusOK, refView(ref))
}
