package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/easylab-platform/easyvcs/gitbridge"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// ---- M3c: import an external git repository ----
//
// POST /api/v1/repos/{ns}/{name}/import clones an EXTERNAL git repository
// (heads only — never refs/pull/*) and imports its history as easyvcs
// revisions, landing the selected ref on the repository's `main`. It is the
// engine-native replacement for the workspace gateway's repo-import tool.
//
// It belongs in the engine (not a caller) because importing converts git
// commits into snapshot/revision objects, which needs direct store access. It
// is the same family as push mirrors (git interop): both reach an external git
// endpoint and touch the store, so neither can live in a pure RPC caller.
//
// Unlike push mirrors (which shell out to the git CLI), the import path uses
// go-git (gitbridge), so the server image needs no `git` binary for it.

// importReq is the /import request body.
type importReq struct {
	URL     string `json:"url"`
	Branch  string `json:"branch"`
	Token   string `json:"token"`
	SSHKey  string `json:"ssh_key"`
	SSHPass string `json:"ssh_key_passphrase"`
	Desc    string `json:"description"`
	// Depth limits history: 0 (default) = full history; 1 = tip only (snapshot);
	// N = last N commits. Executed by go-git (no `git` binary).
	Depth int `json:"depth"`
	// Create, when nil, defaults to true: create the repository when absent.
	Create *bool `json:"create"`
}

// importDefaultTimeout bounds a whole import when EASYVCS_IMPORT_TIMEOUT is
// unset. An import clones the source and walks its history, so it is far slower
// than a normal request.
const importDefaultTimeout = 15 * time.Minute

// importTimeout returns the import ceiling (EASYVCS_IMPORT_TIMEOUT seconds; a
// missing or invalid value falls back to the default).
func importTimeout() time.Duration {
	v := strings.TrimSpace(os.Getenv("EASYVCS_IMPORT_TIMEOUT"))
	if v == "" {
		return importDefaultTimeout
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return importDefaultTimeout
	}
	return time.Duration(n) * time.Second
}

// handleImport clones an external git repository into {ns}/{name}. It is the
// only way to seed a repository from an existing git history; the content lands
// on `main` (the source ref name is irrelevant, matching repo-import).
//
// Auth is owner-level. The repository may not exist yet (create=true, default),
// so this handler does its own identity + ownership check instead of
// requireRepoAccess (which needs an existing repo).
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	_, u, ok := s.currentUser(r)
	if !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
		return
	}
	if u == nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: a token is required"))
		return
	}
	ns, name := r.PathValue("ns"), r.PathValue("name")
	req, err := decodeJSONBody[importReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.URL) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("url is required"))
		return
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	create := true
	if req.Create != nil {
		create = *req.Create
	}

	// Open or create the destination repository.
	repo, err := s.cs.OpenRepo(store.RepoRef{Namespace: ns, Name: name})
	if err != nil {
		if !create {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		// The org must be owned by (or claimable by) the caller.
		if owner, oerr := s.cs.OwnerOfNamespace(ns); oerr == nil && owner != 0 && owner != u.ID {
			writeErr(w, http.StatusConflict, errors.New("organization is owned by another user"))
			return
		}
		repo, err = s.cs.Create(store.RepoRef{Owner: u.ID, Namespace: ns, Name: name})
		if err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		if req.Desc != "" {
			if meta, merr := repo.RepoMeta(); merr == nil {
				meta.Description = req.Desc
				_ = repo.UpdateRepoMeta(meta)
			}
		}
	}

	// Owner-level access on the destination.
	if role, rerr := s.cs.RoleOf(repo.RepoID(), u.ID); rerr != nil || !role.CanPush() {
		writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: no import access to %s/%s", ns, name))
		return
	}

	// Refuse to overwrite a repository that already carries content on `main`.
	if tip, terr := repo.GetRef("main"); terr == nil && tip != nil && tip.Target != "" {
		writeErr(w, http.StatusConflict, errors.New("repository already has content on main; refusing to overwrite"))
		return
	}

	// go-git wants a key FILE, so a PEM body is written to a 0600 temp file.
	sshPath, cleanup, kerr := writeTempSSHKey(req.SSHKey)
	if kerr != nil {
		writeErr(w, http.StatusInternalServerError, kerr)
		return
	}
	defer cleanup()

	ws := revision.NewWorkspace(repo)
	revIDs, ierr := importWithTimeout(r.Context(), ws, repo, gitbridge.ImportOptions{
		Source: req.URL, Branch: req.Branch, Token: req.Token,
		SSHKey: sshPath, SSHKeyPassphrase: req.SSHPass,
		Depth: req.Depth,
	}, importTimeout())
	if ierr != nil {
		// The raw error may embed a credential or the source URL's userinfo;
		// log it and return a redacted message.
		log.Printf("easyvcs-server: import %s/%s from %s: %v", ns, name, redactURL(req.URL), ierr)
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":     false,
			"error":  "import failed",
			"detail": sanitizeImportError(ierr, req.URL, req.Token, req.SSHPass),
		})
		return
	}
	if len(revIDs) == 0 {
		writeErr(w, http.StatusBadGateway, errors.New("import produced no revisions"))
		return
	}
	// Land the imported ref on `main`.
	lastID := revIDs[len(revIDs)-1]
	if _, err := ws.SetRef("main", store.RefBranch, lastID); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "org": ns, "repo": name, "branch": "main", "revisions": len(revIDs),
	})
}

// importWithTimeout runs gitbridge.ImportBranch under a deadline. go-git's
// clone takes no context, so the call runs in a goroutine and the handler
// stops WAITING at the deadline (a timed-out import may therefore still finish
// server-side and leave the repository partially populated; a retry is safe —
// imported commits reuse their embedded revision id).
func importWithTimeout(ctx context.Context, ws *revision.Workspace, repo *store.Repo, opts gitbridge.ImportOptions, d time.Duration) ([]string, error) {
	if d <= 0 {
		return gitbridge.ImportBranch(ws, repo, opts)
	}
	type result struct {
		ids []string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		ids, err := gitbridge.ImportBranch(ws, repo, opts)
		ch <- result{ids, err}
	}()
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case r := <-ch:
		return r.ids, r.err
	case <-t.C:
		return nil, fmt.Errorf("import timed out after %s", d)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// writeTempSSHKey writes a PEM key body to a 0600 temp file and returns its
// path plus a cleanup func. An empty key is a no-op.
func writeTempSSHKey(pem string) (string, func(), error) {
	if strings.TrimSpace(pem) == "" {
		return "", func() {}, nil
	}
	f, err := os.CreateTemp("", "easyvcs-import-key-*")
	if err != nil {
		return "", func() {}, err
	}
	name := f.Name()
	fail := func(e error) (string, func(), error) {
		_ = f.Close()
		_ = os.Remove(name)
		return "", func() {}, e
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return fail(err)
	}
	if _, err := f.WriteString(pem); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(name)
		return "", func() {}, err
	}
	return name, func() { _ = os.Remove(name) }, nil
}

// redactURL hides the userinfo (credentials) of a URL for logging.
func redactURL(u string) string {
	i := strings.Index(u, "://")
	if i < 0 {
		return u
	}
	rest := u[i+3:]
	at := strings.LastIndex(rest, "@")
	if at < 0 {
		return u
	}
	return u[:i+3] + "***@" + rest[at+1:]
}

// sanitizeImportError returns a message safe to hand back to the caller: the
// source URL's userinfo is redacted and every supplied secret is masked.
func sanitizeImportError(err error, secrets ...string) string {
	msg := err.Error()
	for _, s := range secrets {
		if s == "" {
			continue
		}
		msg = strings.ReplaceAll(msg, s, "***")
	}
	return msg
}
