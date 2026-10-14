package server

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// ---- M2: hosting (orgs / repos) ----

func (s *Server) handleListRepos(w http.ResponseWriter, r *http.Request) {
	_, u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
		return
	}
	var uid int64
	if u != nil {
		uid = u.ID
	}
	// `?owned=true` lists ONLY the caller's own/member repos (what a
	// tenant-scoped client may act on), excluding every other tenant's public
	// repos. Without it the historical behaviour (accessible = public + own)
	// is preserved for backward compatibility.
	var (
		infos []store.NSInfo
		err   error
	)
	if r.URL.Query().Get("owned") == "true" {
		if u == nil {
			// No credential => owns nothing; an empty list (not an error).
			infos = nil
		} else {
			infos, err = s.cs.ListOwnedInfo(uid)
		}
	} else {
		infos, err = s.cs.ListAccessibleInfo(uid)
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	limit := limitFrom(r)
	out := make([]map[string]any, 0, len(infos))
	for i, info := range infos {
		if i >= limit {
			break
		}
		// Carry default_branch + private inline so a client (e.g.
		// agent-gateway) does not have to GET each repo individually.
		out = append(out, map[string]any{
			"namespace":      info.Name,
			"default_branch": info.DefaultBranch,
			"private":        info.Visibility == "private",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"repos": out})
}

func (s *Server) handleListOrgs(w http.ResponseWriter, r *http.Request) {
	_, u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
		return
	}
	out := []map[string]any{}
	if u != nil {
		ns, err := s.cs.ListNamespacesByOwner(u.ID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		for _, n := range ns {
			out = append(out, map[string]any{"org": n.Name, "created": n.Created.UnixMilli()})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"orgs": out})
}

func (s *Server) handleCreateOrg(w http.ResponseWriter, r *http.Request) {
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
		Org string `json:"org"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Org == "" {
		writeErr(w, http.StatusBadRequest, errors.New("org is required"))
		return
	}
	// A namespace is globally unique: refuse to claim one owned by another user.
	if owner, err := s.cs.OwnerOfNamespace(req.Org); err == nil && owner != 0 && owner != u.ID {
		writeErr(w, http.StatusConflict, errors.New("organization is owned by another user"))
		return
	}
	// Already owned by the caller -> idempotent OK.
	if owner, err := s.cs.OwnerOfNamespace(req.Org); err == nil && owner == u.ID {
		writeJSON(w, http.StatusOK, map[string]any{"namespace": req.Org})
		return
	}
	if _, err := s.cs.Create(store.NSRef{Owner: u.ID, Name: req.Org}); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespace": req.Org})
}

func (s *Server) handleGetRepo(w http.ResponseWriter, r *http.Request) {
	// Go module vanity resolution: `?go-get=1` returns an HTML meta tag telling
	// `go` to fetch this module from easyvcs's own git endpoint (works with
	// GOPROXY=direct). `go` GETs `<module-path>?go-get=1` before cloning.
	if r.URL.Query().Get("go-get") == "1" {
		host := r.Host
		prefix := host + r.URL.Path // the FULL import path, e.g. easyvcs.local/api/v1/repos/acme/lib
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta name="go-import" content="%s git http://%s%s"></head><body>easyvcs</body></html>`,
			prefix, host, r.URL.Path)
		return
	}
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	meta, err := repo.RepoMeta()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	refs, err := repo.ListRefs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"namespace":   repo.Name,
		"description": meta.Description, "visibility": meta.Visibility,
		"default_branch": meta.DefaultBranch, "kind": meta.Kind, "empty": len(refs) == 0,
	})
}

func (s *Server) handleCreateRepo(w http.ResponseWriter, r *http.Request) {
	_, u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
		return
	}
	if u == nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: a token is required"))
		return
	}
	ns := nsRefName(r)
	req, err := decodeJSONBody[struct {
		Description   string `json:"description"`
		Visibility    string `json:"visibility"`
		DefaultBranch string `json:"default_branch"`
		// Init, when true, seeds the new repository with an initial empty commit
		// on `main`. Default false = the previous behaviour (an empty repo with
		// no refs, from which "create a branch from main" would fail).
		Init bool `json:"init"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The org must be owned by (or claimable by) the caller.
	if owner, err := s.cs.OwnerOfNamespace(ns); err == nil && owner != 0 && owner != u.ID {
		writeErr(w, http.StatusConflict, errors.New("organization is owned by another user"))
		return
	}
	repo, err := s.cs.Create(store.NSRef{Owner: u.ID, Name: ns})
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	// Apply the caller's metadata overrides. The store already defaulted a new
	// repo to PRIVATE (multi-tenant safety); a caller that wants it public must
	// say so explicitly with visibility:"public".
	if req.Visibility != "" || req.DefaultBranch != "" || req.Description != "" {
		if req.Visibility != "" && req.Visibility != "public" && req.Visibility != "private" {
			writeErr(w, http.StatusBadRequest, errors.New("visibility must be public|private"))
			return
		}
		meta, err := repo.RepoMeta()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if req.Description != "" {
			meta.Description = req.Description
		}
		if req.Visibility != "" {
			meta.Visibility = req.Visibility
		}
		if req.DefaultBranch != "" {
			meta.DefaultBranch = req.DefaultBranch
		}
		if err := repo.UpdateRepoMeta(meta); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	if req.Init {
		// Seed an initial empty commit on `main` so the repository has a ref to
		// branch from. This is a SERVER-SIDE BOOTSTRAP: `main` is protected (it
		// may only change through a merged Revision Request), so we write the
		// ref directly here rather than going through the user-facing commit
		// path (which would be refused 403). We do NOT touch the protection set.
		ws := revision.NewWorkspace(repo)
		treeID, err := ws.WriteTree(object.NewTree())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		_, rev, err := ws.Commit(revision.CommitParams{
			TreeID:      treeID,
			Description: "chore: initial commit",
			Author:      s.callerAuthor(tokenFrom(r.Context())),
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if _, err := ws.SetRef("main", store.RefBranch, rev.ID); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"namespace": ns, "ok": true})
}

func (s *Server) handlePatchRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	req, err := decodeJSONBody[struct {
		Description   *string `json:"description"`
		Visibility    *string `json:"visibility"`
		DefaultBranch *string `json:"default_branch"`
	}](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	meta, err := repo.RepoMeta()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if req.Description != nil {
		meta.Description = *req.Description
	}
	if req.Visibility != nil {
		if *req.Visibility != "public" && *req.Visibility != "private" {
			writeErr(w, http.StatusBadRequest, errors.New("visibility must be public|private"))
			return
		}
		meta.Visibility = *req.Visibility
	}
	if req.DefaultBranch != nil && *req.DefaultBranch != "" {
		meta.DefaultBranch = *req.DefaultBranch
	}
	if err := repo.UpdateRepoMeta(meta); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"namespace":   repo.Name,
		"description": meta.Description, "visibility": meta.Visibility, "default_branch": meta.DefaultBranch,
	})
}

func (s *Server) handleDeleteRepo(w http.ResponseWriter, r *http.Request) {
	ns := nsRefName(r)
	if err := s.cs.Delete(store.NSRef{Name: ns}); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "namespace": ns})
}
