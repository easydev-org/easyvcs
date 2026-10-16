package server

import (
	"encoding/base64"
	"errors"
	"fmt"
	"github.com/easylab-platform/easyvcs/syncproto"
	"net/http"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// ---- commit (no working copy): apply a file-op set to a NON-default branch ----

// commitFileOp / commitReq / snapshotReq are the SHARED request-body types
// (syncproto). commitReq supports BOTH a fresh commit and an AMEND: no
// revision_id = a NEW revision on the branch tip; with revision_id = amend that
// revision in place (same stable id, replaced snapshot).
type (
	commitFileOp = syncproto.FileOp
	commitReq    = syncproto.CommitReq
	snapshotReq  = syncproto.SnapshotReq
)

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
	// A `/commit` MUST target an EXISTING branch. Committing to a branch that
	// does not exist used to silently create a ROOT revision (the tree = only
	// the files in this request), which then merged into the target and wiped
	// every other file. Require the branch up front; a deliberate root revision
	// must be requested EXPLICITLY with `parents: []` (an empty array, distinct
	// from an omitted field) — that is the import/bootstrap path.
	branchExists := false
	if ref, rerr := revision.NewWorkspace(repo).GetRef(req.Branch); rerr == nil && ref != nil && ref.Kind == store.RefBranch {
		branchExists = true
	}
	if !branchExists && req.RevisionID == "" && req.Parents == nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf(
			"branch %q does not exist; create it first (POST /branches {name, from}) or pass an explicit empty \"parents\": [] to create a root revision",
			req.Branch,
		))
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
	// The SCOPE is always the ?path= query; the new root revision touches only
	// that subtree.
	if scope := scopePath(r); scope != "" {
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

// ---- draft (private, no working copy): a jj-style working-copy revision ----

// draftReq is the /draft request body: a set of file ops applied to the caller's
// PRIVATE draft revision on a PRIVATE branch. No revision_id => create a NEW
// draft; with revision_id => amend the caller's own draft. The draft branch and
// revision are visible ONLY to the caller until they open a Revision Request.
// DraftRefName is the FIXED name of every user's draft branch: a draft IS a
// private branch with this name, so each user has exactly ONE draft per
// namespace (a draft and a private branch are the SAME mechanism).
const DraftRefName = "@draft"

type draftReq struct {
	// Target is the branch the draft proposes to (the RR target, e.g. "main").
	// The draft itself is always DraftRefName.
	Target     string             `json:"target"`
	Message    string             `json:"message"`
	RevisionID string             `json:"revision_id"`
	Author     *store.Author      `json:"author"`
	Parents    []string           `json:"parents"`
	Files      []syncproto.FileOp `json:"files"`
}

// handleDraft applies a set of file operations to the caller's PRIVATE draft
// branch (creating it if needed) as a PRIVATE revision. This REPLACES /commit:
// there is no way to write a PUBLIC branch directly — all public changes go
// through a merged Revision Request. A draft is the caller's private scratch.
func (s *Server) handleDraft(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	if repo.IsMirror() {
		writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: %s is a read-only mirror", repo))
		return
	}
	_, u, ok := s.currentUser(r)
	if !ok || u == nil {
		writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: a token is required to draft"))
		return
	}
	req, err := decodeJSONBody[draftReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// The draft always lives at DraftRefName; Target names the branch it will
	// eventually propose to (defaults to the namespace's default branch).
	target := strings.TrimSpace(req.Target)
	if target == "" {
		target = "main"
	}
	if strings.TrimSpace(req.Message) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("message is required"))
		return
	}
	if len(req.Files) == 0 && req.RevisionID == "" {
		writeErr(w, http.StatusBadRequest, errors.New("files must not be empty unless revision_id is given (reword)"))
		return
	}
	changes, err := commitSpecs(req.Files)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	author := s.callerAuthor(tokenFrom(r.Context()))
	if req.Author != nil {
		author = *req.Author
	}
	ws := revision.NewWorkspace(repo.WithViewer(u.ID))
	// Sub-scope lift: a ?path= draft lifts a (scope-relative) change onto the
	// draft branch, touching only that subtree — same as the old /commit lift.
	if scope := scopePath(r); scope != "" {
		if req.RevisionID != "" {
			writeErr(w, http.StatusBadRequest, errors.New("revision_id (amend) is not supported with path"))
			return
		}
		var lrev *store.Revision
		var lsnap *store.Snapshot
		lerr := s.withRepoLock(repo, func() error {
			// Lift onto the DRAFT, whose base is the target branch (or the draft's
			// own private tip when it already exists).
			liftBase := DraftRefName
			if dr, derr := repo.GetRefIn("", DraftRefName); derr != nil || dr == nil || dr.OwnerUserID != u.ID {
				liftBase = target
			}
			ns, rv, e := ws.Lift(liftBase, scope, changes, req.Message, author)
			if e != nil {
				return e
			}
			if err := repo.SetRevisionOwner(rv.ID, u.ID); err != nil {
				return err
			}
			if e := repo.PutRef(&store.Ref{Name: DraftRefName, Kind: store.RefBranch, Target: rv.ID, OwnerUserID: u.ID}); e != nil {
				return e
			}
			lsnap, lrev = ns, rv
			return nil
		})
		if lerr != nil {
			writeErr(w, http.StatusBadRequest, lerr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "draft": DraftRefName, "target": target, "path": scope, "revision_id": lrev.ID, "snapshot": lsnap.RevisionHash.String(), "private": true})
		return
	}
	// Resolve the draft's BASE: the caller's private draft tip if present, else
	// the PUBLIC branch tip of the same name (the draft starts from the shared
	// branch, then diverges privately).
	draftRef, derr := repo.GetRefIn("", DraftRefName)
	var draftTip string
	if derr == nil && draftRef != nil && draftRef.OwnerUserID == u.ID {
		draftTip = draftRef.Target
	}
	publicRef, perr := repo.GetRefIn("", DraftRefName) // viewer-scoped: may return the private one
	_ = publicRef
	_ = perr
	var rev *store.Revision
	var snap *store.Snapshot
	conflict := ""
	err = s.withRepoLock(repo, func() error {
		var baseHash object.ID
		switch {
		case req.RevisionID != "":
			// Amend: only the caller's OWN draft revision may be amended.
			old, oerr := ws.GetRevision(req.RevisionID)
			if oerr != nil {
				return fmt.Errorf("draft revision %q not found: %w", req.RevisionID, oerr)
			}
			if old.OwnerUserID != u.ID {
				return fmt.Errorf("forbidden: revision %q is not your draft", req.RevisionID)
			}
			baseHash = old.Hash
		case draftTip != "":
			cur, gerr := tipSnapshotHash(repo, draftTip)
			if gerr != nil {
				return gerr
			}
			baseHash = cur
		default:
			// Start the draft from the PUBLIC target branch (else main).
			startName := target
			if pub, gerr := repo.GetRefIn("", target); gerr != nil || pub == nil || pub.OwnerUserID != 0 {
				startName = "main"
			}
			if r, gerr := repo.GetRefIn("", startName); gerr == nil && r != nil {
				if cur, e := tipSnapshotHash(repo, r.Target); e == nil {
					baseHash = cur
				}
			}
		}
		// Optimistic concurrency: a pinned parent must match the base tip.
		if len(req.Parents) > 0 {
			want, herr := object.HexToID(req.Parents[0])
			if herr != nil {
				return fmt.Errorf("bad parent sha %q: %w", req.Parents[0], herr)
			}
			if baseHash != (object.ID{}) && baseHash != want {
				conflict = "branch moved since the pinned parent"
				return nil
			}
		}
		newSnap, newRev, cerr := ws.CommitFromChanges(baseHash, changes, req.Message, author, req.RevisionID)
		if cerr != nil {
			return cerr
		}
		// Stamp the revision PRIVATE to the caller (hidden from everyone else).
		if err := repo.SetRevisionOwner(newRev.ID, u.ID); err != nil {
			return err
		}
		newRev.OwnerUserID = u.ID
		// Point the PRIVATE branch at it (owner = caller).
		if serr := repo.PutRef(&store.Ref{Name: DraftRefName, Kind: store.RefBranch, Target: newRev.ID, OwnerUserID: u.ID}); serr != nil {
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
	s.auditOK(r, tokenFrom(r.Context()), fmt.Sprintf("draft rev=%s", rev.ID))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok": true, "draft": DraftRefName, "target": target, "revision_id": rev.ID, "snapshot": snap.RevisionHash.String(),
		"private": true,
	})
}
