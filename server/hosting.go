// Hosting surface: the REST API that manages identities, repositories and refs
// and serves content browsing for EasyVCS itself. A caller (the webui, an agent,
// or a CLI) uses these endpoints together with the Revision Request surface
// (server/rr.go) and the smart protocol (server/server.go), all under /api/v1.
//
// Endpoints:
//
//	GET    /api/v1/whoami                     resolves a token to its user
//	GET    /api/v1/users                      list users (admin)
//	POST   /api/v1/users                      create a user + a one-time write token (admin)
//	PATCH  /api/v1/users/{id}                 update a user (admin)
//	DELETE /api/v1/users/{id}                 delete a user (admin)
//	POST   /api/v1/users/{id}/tokens          mint a NEW token for an existing user (admin)
//	GET    /api/v1/tokens                     list the caller's tokens
//	POST   /api/v1/tokens                     create a token (one-time plaintext)
//	DELETE /api/v1/tokens/{id}                delete one of the caller's tokens
//	GET    /api/v1/repos                      list accessible repositories
//	GET    /api/v1/orgs                       list the caller's namespaces
//	POST   /api/v1/orgs                       create a namespace
//	GET    /api/v1/repos/{ns}/{name}          repository metadata
//	POST   /api/v1/repos/{ns}/{name}          create a repository
//	PATCH  /api/v1/repos/{ns}/{name}          update repository metadata
//	DELETE /api/v1/repos/{ns}/{name}          delete a repository
//	GET    /api/v1/namespaces/{ns}/refs     list refs
//	GET    /api/v1/namespaces/{ns}/branches list branches
//	POST   /api/v1/namespaces/{ns}/branches create a branch (derives a revision)
//	DELETE /api/v1/namespaces/{ns}/branches/{branch} delete a branch
//	GET    /api/v1/namespaces/{ns}/tags     list tags
//	POST   /api/v1/namespaces/{ns}/tags     create an immutable tag
//	GET    /api/v1/namespaces/{ns}/tree     list a directory
//	GET    /api/v1/namespaces/{ns}/blob     read a text file (binary is flagged)
//	GET    /api/v1/namespaces/{ns}/raw      read a file's raw bytes
//	GET    /api/v1/namespaces/{ns}/log      commit / file history
//	GET    /api/v1/namespaces/{ns}/commit/{sha} commit metadata
//	GET    /api/v1/namespaces/{ns}/diff     per-file diff between two snapshots
//	GET    /api/v1/namespaces/{ns}/blame    line-level attribution
//	GET    /api/v1/namespaces/{ns}/archive/{ref}.tar.gz  download a tree as tar.gz
//	GET    /api/v1/namespaces/{ns}/push-mirrors          list push mirrors (no token)
//	POST   /api/v1/namespaces/{ns}/push-mirrors          create/replace a push mirror
//	DELETE /api/v1/namespaces/{ns}/push-mirrors/{name}   delete a push mirror
//	POST   /api/v1/namespaces/{ns}/push-mirrors/{name}/sync  push one mirror now
//	POST   /api/v1/namespaces/{ns}/import   import an external git repository
//
// Merge requests are NOT here: they are Revision Requests, served by
// server/rr.go (there is no separate MR surface).
//
// Auth: a bearer token resolves via store.LookupToken to a user; when no user
// exists the instance is open (anonymous read/write, dev convenience). Reads use
// the read capability, writes use owner/propose as noted per handler. Identity
// and user administration additionally require EASYVCS_ADMIN_TOKEN (a static
// env credential); when it is unset the admin endpoints return 503.
//
// ref semantics: an empty `ref` means the repository default branch (RepoMeta.
// DefaultBranch, falling back to "main"). A non-empty ref may be a branch name,
// a tag name, a revision id (or unique prefix), or a snapshot hash.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// mountHosting registers the hosting REST surface on mux. It is additive: the
// existing smart-protocol and RR routes are untouched.
func (s *Server) mountHosting(mux *http.ServeMux) {
	// M1 identity / tokens.
	mux.HandleFunc("GET /api/v1/whoami", s.handleWhoami)
	mux.HandleFunc("GET /api/v1/users", s.requireAdmin(s.handleListUsers))
	mux.HandleFunc("POST /api/v1/users", s.requireAdmin(s.handleCreateUser))
	mux.HandleFunc("PATCH /api/v1/users/{id}", s.requireAdmin(s.handlePatchUser))
	mux.HandleFunc("DELETE /api/v1/users/{id}", s.requireAdmin(s.handleDeleteUser))
	mux.HandleFunc("POST /api/v1/users/{id}/tokens", s.requireAdmin(s.handleMintUserToken))
	mux.HandleFunc("GET /api/v1/tokens", s.handleListTokens)
	mux.HandleFunc("POST /api/v1/tokens", s.handleCreateToken)
	mux.HandleFunc("DELETE /api/v1/tokens/{id}", s.handleDeleteToken)

	// M2 hosting (orgs / repos).
	mux.HandleFunc("GET /api/v1/namespaces", s.handleListRepos)
	mux.HandleFunc("POST /api/v1/namespaces", s.handleCreateOrg)
	mux.HandleFunc("GET /api/v1/namespaces/{ns}", s.requireRepoRead(s.handleGetRepo))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}", s.handleCreateRepo)
	mux.HandleFunc("PATCH /api/v1/namespaces/{ns}", s.requireRepoAccess(true, s.handlePatchRepo))
	mux.HandleFunc("DELETE /api/v1/namespaces/{ns}", s.requireRepoAccess(true, s.handleDeleteRepo))

	// M3 refs.
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/refs", s.requireRepoAccess(false, s.handleListRefs))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/branches", s.requireRepoAccess(false, s.handleListBranches))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/branches", s.requireRepoAccess(true, s.handleCreateBranch))
	mux.HandleFunc("DELETE /api/v1/namespaces/{ns}/branches/{branch}", s.requireRepoAccess(true, s.handleDeleteBranch))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/tags", s.requireRepoAccess(false, s.handleListTags))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/tags", s.requireRepoAccess(true, s.handleCreateTag))

	// M3c branch sync: integrate a base branch into a feature branch.
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/branches/{branch}/sync", s.requireRepoAccess(true, s.handleBranchSync))

	// M3b commit (no working copy): apply a file-op set to a non-default branch.
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/commit", s.requireRepoAccess(true, s.handleCommitFiles))
	// M3b snapshot (no working copy): build a revision + snapshot, move NO ref.
	// PROPOSE level (developer+): the "propose from a sandbox" primitive feeding
	// POST /rr {source_snapshot}.
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/snapshot", s.requirePropose(s.handleSnapshot))
	// M3c import: seed a repository from an external git repository (owner).
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/import", s.handleImport)

	// Branch protection (owner-managed).
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/protected-branches", s.requireRepoAccess(false, s.handleGetProtectedBranches))
	mux.HandleFunc("PUT /api/v1/namespaces/{ns}/protected-branches", s.requireRepoAccess(true, s.handleSetProtectedBranches))
	// push mirrors (owner-managed; list is read).
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/push-mirrors", s.requireRepoAccess(false, s.handleListPushMirrors))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/push-mirrors", s.requireRepoAccess(true, s.handleCreatePushMirror))
	mux.HandleFunc("DELETE /api/v1/namespaces/{ns}/push-mirrors/{mirror}", s.requireRepoAccess(true, s.handleDeletePushMirror))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/push-mirrors/{mirror}/sync", s.requireRepoAccess(true, s.handleSyncPushMirror))

	// M4 browse.
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/tree", s.requireRepoAccess(false, s.handleTree))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/blob", s.requireRepoAccess(false, s.handleBlob))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/raw", s.requireRepoAccess(false, s.handleRaw))
	// Structured conflict view + raw object bytes (for binary/dir conflict terms).
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/conflict", s.requireRepoAccess(false, s.handleConflict))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/object", s.requireRepoAccess(false, s.handleObject))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/search", s.requireRepoAccess(false, s.handleSearch))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/log", s.requireRepoAccess(false, s.handleLog))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/commit/{sha}", s.requireRepoAccess(false, s.handleCommit))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/diff", s.requireRepoAccess(false, s.handleDiff))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/blame", s.requireRepoAccess(false, s.handleBlame))
	// archive/{file} where {file} = "<ref>.tar.gz" (see handleArchive).
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/archive/{file}", s.requireRepoAccess(false, s.handleArchive))
	// A SUBDIRECTORY as a (recursive) repository: its refs + history, indexed.
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/subrepo", s.requireRepoAccess(false, s.handleSubRepo))

	// M5 git smart HTTP (READ-ONLY: clone/fetch). A standard `git clone
	// <server>/api/v1/repos/{ns}/{name}[/<subdir>...]` works, so package managers
	// that treat a git repo (or a monorepo SUBDIRECTORY) as a package can use
	// easyvcs directly. No receive-pack (push). The catch-all {path...} also
	// supports the "each subdir is its own git endpoint" model.
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/{path...}", s.gitHandler)
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/{path...}", s.gitHandler)
}

// ---- auth helpers ----

// bearerToken extracts a bearer credential from the Authorization header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if strings.HasPrefix(h, "Bearer ") {
		return strings.TrimPrefix(h, "Bearer ")
	}
	// The git CLI sends Basic auth (any username, the token as password) for
	// `git clone http://user:token@host/...`. Accept that shape so the same
	// token works for both the REST API and git smart HTTP.
	if strings.HasPrefix(h, "Basic ") {
		if raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(h, "Basic ")); err == nil {
			if _, pass, ok := strings.Cut(string(raw), ":"); ok {
				return pass
			}
		}
	}
	return ""
}

// adminCredential returns the configured admin token ("" when unset).
func adminCredential() string { return os.Getenv("EASYVCS_ADMIN_TOKEN") }

// requireAdmin guards user administration with the static admin credential.
// When EASYVCS_ADMIN_TOKEN is unset the admin API is not configured and every
// request is refused with 503 (never silently treated as authorized).
func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
		want := adminCredential()
		if want == "" {
			writeErr(w, http.StatusServiceUnavailable, errors.New("admin API not configured: set EASYVCS_ADMIN_TOKEN"))
			return
		}
		got := bearerToken(r)
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			writeErr(w, http.StatusForbidden, errors.New("forbidden: admin token required"))
			return
		}
		next(w, r)
	}
}

// currentUser resolves the request's bearer token to its user. It returns
// (nil, true) for an anonymous request, and (nil, false) for an invalid or
// disabled credential.
func (s *Server) currentUser(r *http.Request) (*store.Token, *store.User, bool) {
	raw := bearerToken(r)
	if raw == "" {
		return nil, nil, true
	}
	tk, err := s.cs.LookupToken(raw)
	if err != nil {
		return nil, nil, false
	}
	u, err := s.cs.GetUser(tk.UserID)
	if err != nil || u.Disabled {
		return nil, nil, false
	}
	return tk, u, true
}

// ---- small helpers ----

// limitFrom parses ?limit (default 50, max 500).
func limitFrom(r *http.Request) int {
	n := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if p, err := strconv.Atoi(v); err == nil && p > 0 {
			n = p
		}
	}
	if n > 500 {
		n = 500
	}
	return n
}

// newToken generates a 32-hex-character bearer token.
func newToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// resolveRefRevisionID resolves a ref expression to a revision id. Empty means
// the repo default branch (falling back to "main"). A ref may be a branch/tag
// name, a revision id (or unique prefix), or a snapshot hash.
func resolveRefRevisionID(repo *store.Repo, ws *revision.Workspace, ref string) (string, error) {
	if ref == "" {
		ref = "main"
		if meta, err := repo.RepoMeta(); err == nil && meta.DefaultBranch != "" {
			ref = meta.DefaultBranch
		}
	}
	if r, err := ws.GetRef(ref); err == nil && r != nil {
		return r.Target, nil
	}
	if rev, err := repo.GetRevision(ref); err == nil {
		return rev.ID, nil
	}
	if revs, err := repo.ListRevisions(); err == nil {
		for _, r := range revs {
			if strings.HasPrefix(r.ID, ref) {
				return r.ID, nil
			}
		}
		for _, r := range revs {
			if r.Hash.String() == ref {
				return r.ID, nil
			}
		}
	}
	return "", fmt.Errorf("unknown ref %q", ref)
}

// resolveRefSnapshot resolves a ref expression to the snapshot it points at.
func resolveRefSnapshot(repo *store.Repo, ws *revision.Workspace, ref string) (object.ID, error) {
	id, err := resolveRefRevisionID(repo, ws, ref)
	if err != nil {
		return object.ID{}, err
	}
	rev, err := repo.GetRevision(id)
	if err != nil {
		return object.ID{}, err
	}
	return rev.Hash, nil
}

// resolveRefTree resolves a ref expression to its root tree id.
func resolveRefTree(repo *store.Repo, ws *revision.Workspace, ref string) (object.ID, error) {
	snapID, err := resolveRefSnapshot(repo, ws, ref)
	if err != nil {
		return object.ID{}, err
	}
	snap, err := repo.GetSnapshot(snapID)
	if err != nil {
		return object.ID{}, err
	}
	return snap.TreeID, nil
}

func userView(u *store.User) map[string]any {
	return map[string]any{
		"id": u.ID, "username": u.Username, "display_name": u.DisplayName,
		"disabled": u.Disabled, "created": u.Created.UnixMilli(),
	}
}

func refView(r *store.Ref) map[string]any {
	v := map[string]any{"name": r.Name, "kind": string(r.Kind), "target": r.Target}
	if r.Path != "" {
		v["path"] = r.Path // the scope this ref belongs to (absent = root)
	}
	return v
}
