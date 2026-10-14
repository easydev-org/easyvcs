package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// ---- commit (no working copy): apply a file-op set to a NON-default branch ----

// commitFileOp is one file operation in a /commit request.
type commitFileOp struct {
	Path         string `json:"path"`
	Operation    string `json:"operation"`     // create | update | delete (empty = update)
	Content      string `json:"content"`       // UTF-8 text
	ContentBytes string `json:"content_bytes"` // base64; wins over Content
}

// commitReq is the /commit request body. Its shape supports BOTH a fresh commit
// and an AMEND:
//
//   - no revision_id  -> a NEW revision is created with parents = the branch tip
//     (or a root revision when the branch does not exist);
//   - with revision_id -> the named revision is AMENDED in place (same stable id,
//     replaced snapshot). This is how a staging writer accumulates edits across
//     several calls and then finalises them (reword) in one commit.
type commitReq struct {
	Branch     string         `json:"branch"`
	Message    string         `json:"message"`
	RevisionID string         `json:"revision_id"`
	Author     *store.Author  `json:"author"`
	Parents    []string       `json:"parents"`
	Files      []commitFileOp `json:"files"`
	// Path scopes the commit to a SUBDIRECTORY (docs/subrepo-scopes.md §5): the
	// file ops are relative to that dir and are LIFTED onto the root branch —
	// the new root revision touches only that subtree. Empty = a normal commit.
	Path string `json:"path"`
}

// handleCommitFiles applies a set of file operations to a NON-default branch
// without a working copy: it builds the new tree from the branch tip (or the
// amended revision's current tree) and writes objects + snapshot + revision
// atomically, then moves the branch ref. It is the change-native replacement
// for a git clone/amend/force-push cycle (and for the gateway's "staging
// placeholder" hack): N file ops become exactly ONE commit.
//
// The default branch is protected: a commit targeting a protected branch is
// refused (403) — default branches may only change through a merged Revision
// Request. This endpoint is therefore only ever used on feature branches.
//
// (The name is handleCommitFiles because handleCommit already serves the read
// endpoint GET /commit/{sha}.)
func (s *Server) handleCommitFiles(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	if repo.IsMirror() {
		writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: %s is a read-only mirror", repo))
		return
	}
	req, err := decodeJSONBody[commitReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Branch = strings.TrimSpace(req.Branch)
	if req.Branch == "" {
		writeErr(w, http.StatusBadRequest, errors.New("branch is required"))
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("message is required"))
		return
	}
	// The default branch may only change through a merged RR.
	if protected, perr := repo.ProtectedBranches(); perr != nil {
		writeErr(w, http.StatusInternalServerError, perr)
		return
	} else if isProtectedBranch(protected, req.Branch) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":              "protected-branch",
			"protected_branches": protected,
			"message":            "protected branch(es) can only change through a merged Revision Request: " + req.Branch,
		})
		return
	}
	// An empty file set is only valid as a reword of an existing revision.
	if len(req.Files) == 0 && req.RevisionID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("files must not be empty unless revision_id is given (reword)"))
		return
	}
	changes, err := commitSpecs(req.Files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Author precedence: the request body's author, else the authenticated caller.
	// The server authorizes the ACTION and does NOT validate content, so it never
	// rejects a request over its author (consistent with push).
	author := s.callerAuthor(tokenFrom(r.Context()))
	if req.Author != nil {
		author = *req.Author
	}

	ws := revision.NewWorkspace(repo)
	var rev *store.Revision
	var snap *store.Snapshot
	conflict := ""
	// Sub-repo scope: LIFT the (scope-relative) file ops onto the root branch.
	// The new root revision touches only the subtree at req.Path.
	if scope := strings.Trim(strings.TrimSpace(req.Path), "/"); scope != "" {
		if req.RevisionID != "" {
			writeErr(w, http.StatusBadRequest, errors.New("revision_id (amend) is not supported with path"))
			return
		}
		err = s.withRepoLock(repo, func() error {
			newSnap, newRev, lerr := ws.Lift(req.Branch, scope, changes, req.Message, author)
			if lerr != nil {
				return lerr
			}
			snap, rev = newSnap, newRev
			return nil
		})
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		s.auditOK(r, tokenFrom(r.Context()), fmt.Sprintf("lift path=%s branch=%s rev=%s", scope, req.Branch, rev.ID))
		writeJSON(w, http.StatusOK, map[string]any{
			"ok": true, "branch": req.Branch, "path": scope, "revision_id": rev.ID, "snapshot": snap.RevisionHash.String(),
		})
		return
	}
	err = s.withRepoLock(repo, func() error {
		tipID := ""
		if ref, rerr := ws.GetRef(req.Branch); rerr == nil && ref != nil && ref.Kind == store.RefBranch {
			tipID = ref.Target
		}

		// The base tree for the new snapshot: the branch tip, or (for an amend)
		// the amended revision's CURRENT tree so previously accumulated edits are
		// preserved (CommitFromChanges only overrides the PARENT pointers on an
		// amend, so passing the revision's own snapshot here keeps its tree as the
		// base).
		var baseHash object.ID
		switch {
		case req.RevisionID != "":
			if tipID != "" && tipID != req.RevisionID {
				conflict = "amended revision is not the branch tip"
				return nil
			}
			old, oerr := ws.GetRevision(req.RevisionID)
			if oerr != nil {
				return fmt.Errorf("revision %q not found: %w", req.RevisionID, oerr)
			}
			baseHash = old.Hash
		case len(req.Parents) > 0:
			want, herr := object.HexToID(req.Parents[0])
			if herr != nil {
				return fmt.Errorf("bad parent sha %q: %w", req.Parents[0], herr)
			}
			// Optimistic concurrency: a pinned parent must match the branch tip
			// when the branch already exists.
			if tipID != "" {
				cur, gerr := tipSnapshotHash(repo, tipID)
				if gerr != nil {
					return gerr
				}
				if cur != want {
					conflict = "branch moved since the pinned parent"
					return nil
				}
			}
			baseHash = want
		case tipID != "":
			var gerr error
			baseHash, gerr = tipSnapshotHash(repo, tipID)
			if gerr != nil {
				return gerr
			}
		}

		newSnap, newRev, cerr := ws.CommitFromChanges(baseHash, changes, req.Message, author, req.RevisionID)
		if cerr != nil {
			return cerr
		}
		if _, serr := ws.SetRef(req.Branch, store.RefBranch, newRev.ID); serr != nil {
			return serr
		}
		snap, rev = newSnap, newRev
		return nil
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if conflict != "" {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "stale", "message": conflict})
		return
	}
	s.auditOK(r, tokenFrom(r.Context()), fmt.Sprintf("commit branch=%s rev=%s", req.Branch, rev.ID))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "branch": req.Branch, "revision_id": rev.ID, "snapshot": snap.RevisionHash.String(),
	})
}

// commitSpecs converts the request's file ops into FileChangeSpec, decoding
// content_bytes and rejecting unsafe paths (traversal / absolute) so a commit
// can never escape the tree root.
func commitSpecs(files []commitFileOp) ([]revision.FileChangeSpec, error) {
	out := make([]revision.FileChangeSpec, 0, len(files))
	for _, f := range files {
		p, err := safeRepoPath(f.Path)
		if err != nil {
			return nil, err
		}
		switch f.Operation {
		case "", "create", "update":
			var content []byte
			if f.ContentBytes != "" {
				content, err = base64.StdEncoding.DecodeString(f.ContentBytes)
				if err != nil {
					return nil, fmt.Errorf("file %q: invalid base64 content_bytes: %w", p, err)
				}
			} else {
				content = []byte(f.Content)
			}
			out = append(out, revision.FileChangeSpec{Path: p, Content: content})
		case "delete":
			out = append(out, revision.FileChangeSpec{Path: p, Delete: true})
		default:
			return nil, fmt.Errorf("file %q: unknown operation %q (want create|update|delete)", p, f.Operation)
		}
	}
	return out, nil
}

// safeRepoPath validates a repo-relative path: it must be non-empty, relative,
// free of "." / ".." components and backslashes, and not start with a slash, so
// it cannot escape the tree root.
func safeRepoPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("file path is required")
	}
	if strings.Contains(p, "\\") {
		return "", fmt.Errorf("file path %q must use forward slashes", p)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("unsafe file path %q (must be relative)", p)
	}
	parts := strings.Split(p, "/")
	for _, c := range parts {
		if c == "" || c == "." || c == ".." {
			return "", fmt.Errorf("unsafe file path %q", p)
		}
	}
	return strings.Join(parts, "/"), nil
}

// tipSnapshotHash returns the snapshot hash a revision id currently points at.
func tipSnapshotHash(repo *store.Repo, revisionID string) (object.ID, error) {
	rev, err := repo.GetRevision(revisionID)
	if err != nil {
		return object.ID{}, err
	}
	return rev.Hash, nil
}

// ---- snapshot (no working copy): build a revision + snapshot, move NO ref ----

// snapshotReq is the /snapshot request body. It is deliberately the SAME shape
// as /commit minus `branch` / `revision_id` (there is no branch to move and no
// staging to amend): a set of file ops applied on an optional base snapshot
// produces a NEW revision + snapshot that NO ref points at. It is the
// "propose" primitive: the caller then opens a Revision Request with the
// returned snapshot as its source.
type snapshotReq struct {
	Message string         `json:"message"`
	Author  *store.Author  `json:"author"`
	Parents []string       `json:"parents"`
	Files   []commitFileOp `json:"files"`
}

// handleSnapshot builds a revision + snapshot from a file-op set WITHOUT moving
// any ref. It is the change-native way to "propose from a sandbox": a client
// uploads the files it changed and gets back a snapshot it can hand to
// POST /rr as `source_snapshot`. Because no ref moves, it needs no branch, no
// branch-protection check and no per-repo lock.
//
// Auth is PROPOSE level (developer and above), matching /rr and /rr/objects —
// this is a proposal channel, not the owner-only /commit. Owners and
// maintainers are >= developer, so they may use it too.
func (s *Server) handleSnapshot(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	if repo.IsMirror() {
		writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: %s is a read-only mirror", repo))
		return
	}
	req, err := decodeJSONBody[snapshotReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("message is required"))
		return
	}
	if len(req.Files) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("files must not be empty"))
		return
	}
	changes, err := commitSpecs(req.Files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// Optional base snapshot: the tree the changes are applied on top of. Absent
	// = the empty tree (a root snapshot of brand-new content), matching /commit's
	// root semantics.
	var baseHash object.ID
	if len(req.Parents) > 0 {
		want, herr := object.HexToID(req.Parents[0])
		if herr != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("bad parent sha %q: %w", req.Parents[0], herr))
			return
		}
		if _, gerr := repo.GetSnapshot(want); gerr != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("parent snapshot %s not found: %w", req.Parents[0], gerr))
			return
		}
		baseHash = want
	}
	// Author precedence: the request body's author, else the authenticated
	// caller. As with /commit and push, the server authorizes the ACTION and does
	// NOT validate content, so it never rejects over the author.
	author := s.callerAuthor(tokenFrom(r.Context()))
	if req.Author != nil {
		author = *req.Author
	}

	ws := revision.NewWorkspace(repo)
	snap, rev, cerr := ws.CommitFromChanges(baseHash, changes, req.Message, author, "")
	if cerr != nil {
		writeErr(w, http.StatusBadRequest, cerr)
		return
	}
	s.auditOK(r, tokenFrom(r.Context()), fmt.Sprintf("snapshot rev=%s", rev.ID))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "revision_id": rev.ID, "snapshot": snap.RevisionHash.String(),
	})
}
