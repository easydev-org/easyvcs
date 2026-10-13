package server

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// ---- branch sync: integrate a base branch into a feature branch ----

// errSyncNotFound marks a missing branch/base so the handler can answer 404.
var errSyncNotFound = errors.New("not found")

// syncReq is the /branches/{branch}/sync request body.
type syncReq struct {
	// Base is the branch to integrate (default: the repo default branch, i.e.
	// "main"). It must differ from the target branch.
	Base string `json:"base"`
}

// handleBranchSync integrates `base` (default "main") into a feature `branch`,
// so the branch catches up with the base. It is the server-side replacement for
// a git "update branch" (merge base into this branch): the branch's author can
// resolve conflicts in place before opening / merging a Revision Request.
//
// DESIGN (easyvcs-native, deliberately NOT a git two-parent merge): easyvcs
// history is linear single-parent and conflicts are first-class objects
// (object.Conflict), so a sync commits ONE new single-parent revision on the
// branch (parent = the branch tip) whose tree is the three-way merge of
// (merge base, branch tip, base tip). Conflicts stay as first-class objects in
// that tree — never as text markers — and are rendered as marker text only when
// a client reads the file (/raw, /blob, Materialize). The base branch is never
// touched.
//
// Auth is write (owner); a protected branch (e.g. main) cannot be the TARGET.
func (s *Server) handleBranchSync(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	if repo.IsMirror() {
		writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: %s is a read-only mirror", repo))
		return
	}
	branch := r.PathValue("branch")
	if branch == "" {
		writeErr(w, http.StatusBadRequest, errors.New("branch is required"))
		return
	}
	req, err := decodeJSONBody[syncReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	base := strings.TrimSpace(req.Base)
	if base == "" {
		base = "main"
		if meta, merr := repo.RepoMeta(); merr == nil && meta.DefaultBranch != "" {
			base = meta.DefaultBranch
		}
	}
	if base == branch {
		writeErr(w, http.StatusBadRequest, errors.New("base and branch must differ"))
		return
	}
	// A protected branch may only change through a merged RR, so it cannot be the
	// sync TARGET (it is still a perfectly good base).
	if protected, perr := repo.ProtectedBranches(); perr != nil {
		writeErr(w, http.StatusInternalServerError, perr)
		return
	} else if isProtectedBranch(protected, branch) {
		writeJSON(w, http.StatusForbidden, map[string]any{
			"error":              "protected-branch",
			"protected_branches": protected,
			"message":            "protected branch(es) can only change through a merged Revision Request: " + branch,
		})
		return
	}

	ws := revision.NewWorkspace(repo)
	author := s.callerAuthor(tokenFrom(r.Context()))

	var (
		clean      bool
		upToDate   bool
		newRevID   string
		newSnap    string
		conflicts  []string
		noAncestor bool
	)
	err = s.withRepoLock(repo, func() error {
		branchRef, rerr := ws.GetRef(branch)
		if rerr != nil || branchRef == nil || branchRef.Kind != store.RefBranch {
			return fmt.Errorf("%w: branch %q", errSyncNotFound, branch)
		}
		baseRef, rerr := ws.GetRef(base)
		if rerr != nil || baseRef == nil || baseRef.Kind != store.RefBranch {
			return fmt.Errorf("%w: base branch %q", errSyncNotFound, base)
		}
		branchRev, rerr := repo.GetRevision(branchRef.Target)
		if rerr != nil {
			return rerr
		}
		baseRev, rerr := repo.GetRevision(baseRef.Target)
		if rerr != nil {
			return rerr
		}
		branchSnap, rerr := repo.GetSnapshot(branchRev.Hash)
		if rerr != nil {
			return rerr
		}
		baseSnap, rerr := repo.GetSnapshot(baseRev.Hash)
		if rerr != nil {
			return rerr
		}

		// Ancestors of the branch tip. A "branch" in easyvcs is created with
		// revision.Derive, which keeps the source's PARENTS and records the
		// source in ForkFrom — so the ancestry must follow BOTH the first parent
		// and the fork edge, or a derived branch would look like it has no
		// relation to the base it forked from.
		anc := revisionAncestors(repo, branchRev.ID)

		// Already up to date: the base tip is an ancestor of the branch tip, so
		// the branch already contains every base change.
		if anc[baseRev.ID] {
			clean, upToDate = true, true
			newRevID, newSnap = branchRev.ID, branchSnap.RevisionHash.String()
			return nil
		}
		// Merge base: the nearest common ancestor of the two revision graphs
		// (walking fork + first-parent edges), nearest to the branch tip first.
		mbID, found := nearestCommonAncestor(repo, branchRev.ID, baseRev.ID)
		if !found {
			noAncestor = true
			return nil
		}
		mbRev, rerr := repo.GetRevision(mbID)
		if rerr != nil {
			return rerr
		}
		mbSnap, rerr := repo.GetSnapshot(mbRev.Hash)
		if rerr != nil {
			return rerr
		}

		// 3-way: base = merge base, ours = branch tip, theirs = base tip.
		mergedTree, atoms, merr := ws.Merge(mbSnap.TreeID, branchSnap.TreeID, baseSnap.TreeID)
		if merr != nil {
			return merr
		}
		// Idempotency: in the single-parent model a sync does NOT make the base
		// tip an ancestor of the branch, so a repeat call would otherwise commit
		// an identical tree every time. When the merged tree equals the branch's
		// current tree, the base is already fully integrated — report up_to_date
		// and commit nothing.
		if mergedTree == branchSnap.TreeID {
			clean, upToDate = true, true
			newRevID, newSnap = branchRev.ID, branchSnap.RevisionHash.String()
			return nil
		}
		// One NEW single-parent revision on the branch (parent = the branch tip).
		snap, rev, cerr := ws.Commit(revision.CommitParams{
			Parents:     []object.ID{branchSnap.RevisionHash},
			TreeID:      mergedTree,
			Description: fmt.Sprintf("Sync %s into %s", base, branch),
			Author:      author,
		})
		if cerr != nil {
			return cerr
		}
		if _, serr := ws.SetRef(branch, store.RefBranch, rev.ID); serr != nil {
			return serr
		}
		clean = len(atoms) == 0
		newRevID, newSnap = rev.ID, snap.RevisionHash.String()
		for _, a := range atoms {
			conflicts = append(conflicts, a.Path)
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, errSyncNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if noAncestor {
		writeErr(w, http.StatusBadRequest, errors.New("no common ancestor between the branch and the base"))
		return
	}
	s.auditOK(r, tokenFrom(r.Context()), fmt.Sprintf("branch-sync branch=%s base=%s rev=%s", branch, base, newRevID))
	out := map[string]any{
		"ok": true, "branch": branch, "base": base,
		"clean": clean, "revision_id": newRevID, "snapshot": newSnap,
		"conflicts": conflicts,
	}
	if upToDate {
		out["up_to_date"] = true
	}
	writeJSON(w, http.StatusOK, out)
}

// revisionAncestors returns every revision id reachable from revID by following
// the fork edge (ForkFrom) and the first-parent snapshot edge, including revID
// itself. This is the ancestry a "branch" carries: revision.Derive keeps the
// source's parents and records the source in ForkFrom, so both edges are needed.
func revisionAncestors(repo *store.Repo, revID string) map[string]bool {
	set := map[string]bool{}
	snapOwner := snapshotOwners(repo)
	var walk func(id string)
	walk = func(id string) {
		if id == "" || set[id] {
			return
		}
		rev, err := repo.GetRevision(id)
		if err != nil {
			return
		}
		set[id] = true
		if rev.ForkFrom != "" {
			walk(rev.ForkFrom)
		}
		if snap, err := repo.GetSnapshot(rev.Hash); err == nil && len(snap.Parents) > 0 {
			if owner, ok := snapOwner[snap.Parents[0].String()]; ok {
				walk(owner)
			}
		}
	}
	walk(revID)
	return set
}

// nearestCommonAncestor returns the common ancestor of a and b nearest to a
// (breadth-first over fork + first-parent edges), i.e. the merge base. ok=false
// when the two share no ancestor.
func nearestCommonAncestor(repo *store.Repo, a, b string) (string, bool) {
	bset := revisionAncestors(repo, b)
	snapOwner := snapshotOwners(repo)
	seen := map[string]bool{}
	queue := []string{a}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		if bset[id] {
			return id, true
		}
		rev, err := repo.GetRevision(id)
		if err != nil {
			continue
		}
		if rev.ForkFrom != "" {
			queue = append(queue, rev.ForkFrom)
		}
		if snap, err := repo.GetSnapshot(rev.Hash); err == nil && len(snap.Parents) > 0 {
			if owner, ok := snapOwner[snap.Parents[0].String()]; ok {
				queue = append(queue, owner)
			}
		}
	}
	return "", false
}

// snapshotOwners maps a snapshot hash to the revision id that owns it, so a
// parent snapshot can be resolved back to its revision.
func snapshotOwners(repo *store.Repo) map[string]string {
	out := map[string]string{}
	revs, err := repo.ListRevisions()
	if err != nil {
		return out
	}
	for _, r := range revs {
		out[r.Hash.String()] = r.ID
	}
	return out
}
