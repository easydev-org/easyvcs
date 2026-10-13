// Package rr implements Revision Requests (RR): the change-native collaboration
// unit. Unlike a git merge request (a mutable branch pointer), an RR's subject
// is a PINNED, content-addressed snapshot plus the target tip it was opened
// against. Both are immutable; a source branch moving only marks the RR stale
// and requires an explicit Update. The merge itself is a pure function of the
// pinned snapshots and the CURRENT target tip.
package rr

import (
	"errors"
	"fmt"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// ErrNotMergeable is returned when an RR cannot be merged (closed/merged).
var ErrNotMergeable = errors.New("rr: request is not open for merge")

// Source is a Revision Request's subject: a pinned, content-addressed snapshot
// that already lives in the repository (a "from a local directory" client builds
// a tree, commits a snapshot, and uploads its objects before opening the RR).
// There is exactly one source form — a snapshot — so there is no kind and no
// revision identity to track.
type Source struct {
	// Snapshot is the pinned snapshot under review. Required.
	Snapshot object.ID
}

// Service wraps a repository handle and its semantic workspace.
type Service struct {
	repo *store.Repo
	ws   *revision.Workspace
	cs   *store.CentralStore
}

// New builds an RR service over an opened repo.
func New(cs *store.CentralStore, repo *store.Repo) *Service {
	return &Service{repo: repo, ws: revision.NewWorkspace(repo), cs: cs}
}

// Create pins a new Revision Request: it records the pinned source snapshot and
// the CURRENT target branch tip as base_snapshot. Neither value changes
// afterwards (a snapshot is immutable).
func (s *Service) Create(src Source, targetBranch, title, body string, authorID *int64) (*store.RevisionRequest, error) {
	if targetBranch == "" {
		targetBranch = "main"
	}
	if title == "" {
		return nil, fmt.Errorf("rr: title is required")
	}
	if src.Snapshot.IsZero() {
		return nil, fmt.Errorf("rr: source snapshot is required")
	}
	if _, err := s.repo.GetSnapshot(src.Snapshot); err != nil {
		return nil, fmt.Errorf("rr: source snapshot %s not present in repo: %w", src.Snapshot, err)
	}
	base, err := s.branchTipSnapshot(targetBranch)
	if err != nil {
		return nil, err
	}
	rr, err := s.cs.CreateRevisionRequest(s.repo.RepoID(), &store.RevisionRequest{
		SourceSnapshot: src.Snapshot.String(),
		BaseSnapshot:   base.String(),
		TargetBranch:   targetBranch,
		State:          store.RROpen,
		Title:          title,
		Body:           body,
		AuthorID:       authorID,
	})
	if err != nil {
		return nil, err
	}
	// Record the initial pin as update #1 so the history is complete.
	_, _ = s.cs.AddRRUpdate(rr.ID, rr.SourceSnapshot, rr.BaseSnapshot, authorID)
	return rr, nil
}

// branchTipSnapshot returns the snapshot hash a branch currently points at
// (zero when the branch does not exist).
func (s *Service) branchTipSnapshot(branch string) (object.ID, error) {
	ref, err := s.repo.GetRef(branch)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return object.ID{}, nil
		}
		return object.ID{}, err
	}
	rev, err := s.repo.GetRevision(ref.Target)
	if err != nil {
		return object.ID{}, err
	}
	return rev.Hash, nil
}

// Diff returns the per-file changes between the pinned base and the pinned
// source. It is a pure function of the two pinned snapshots, so the review
// never drifts when a branch moves. A zero base_snapshot (the target branch did
// not exist when the RR was opened) is diffed against the empty tree, so every
// file in the source shows as added.
func (s *Service) Diff(rr *store.RevisionRequest) ([]revision.FileDiff, error) {
	baseTree, err := s.baseTree(rr)
	if err != nil {
		return nil, err
	}
	srcTree, err := s.treeOfSnapshot(rr.SourceSnapshot)
	if err != nil {
		return nil, err
	}
	return s.ws.DiffContent(baseTree, srcTree)
}

// Update explicitly re-pins an RR to a new source snapshot (and the current
// Close closes an RR without merging it. Only an OPEN (or conflicted/stale)
// request can be closed; a merged request is terminal. The source snapshot and
// any review/comment records are preserved (a closed RR stays readable); only
// the state changes. Idempotent: closing an already-closed RR is a no-op.
func (s *Service) Close(rr *store.RevisionRequest, authorID *int64) (*store.RevisionRequest, error) {
	switch rr.State {
	case store.RRClosed:
		return rr, nil
	case store.RRMerged:
		return nil, fmt.Errorf("rr: request is already merged")
	}
	if err := s.cs.UpdateRevisionRequestState(rr.RepoID, rr.IID, store.RRClosed); err != nil {
		return nil, err
	}
	return s.cs.GetRevisionRequest(rr.RepoID, rr.IID)
}

// Update explicitly re-pins an RR to a new source snapshot (and the current
// target tip), appending an rr_updates record and returning the RR to open.
func (s *Service) Update(rr *store.RevisionRequest, newSourceSnapshot object.ID, authorID *int64) (*store.RevisionRequest, error) {
	if newSourceSnapshot.IsZero() {
		return nil, fmt.Errorf("rr: new source snapshot is required")
	}
	if _, err := s.repo.GetSnapshot(newSourceSnapshot); err != nil {
		return nil, fmt.Errorf("rr: source snapshot %s not present: %w", newSourceSnapshot, err)
	}
	base, err := s.branchTipSnapshot(rr.TargetBranch)
	if err != nil {
		return nil, err
	}
	if err := s.cs.UpdateRevisionRequestPin(rr.RepoID, rr.IID, newSourceSnapshot.String(), base.String()); err != nil {
		return nil, err
	}
	if _, err := s.cs.AddRRUpdate(rr.ID, newSourceSnapshot.String(), base.String(), authorID); err != nil {
		return nil, err
	}
	return s.cs.GetRevisionRequest(rr.RepoID, rr.IID)
}

// IsStale reports whether the target branch has moved since the RR was opened
// (its tip differs from the pinned base_snapshot). The source is a pinned
// snapshot and can never drift, so only the target side is inspected. It never
// mutates; the merge itself always uses the CURRENT target tip, so staleness is
// only a UI hint.
func (s *Service) IsStale(rr *store.RevisionRequest) (bool, error) {
	tip, err := s.branchTipSnapshot(rr.TargetBranch)
	if err != nil {
		return false, err
	}
	if tip.String() != rr.BaseSnapshot {
		return true, nil
	}
	return false, nil
}

// MergeResult describes the outcome of ComputeMerge.
type MergeResult struct {
	// Clean is true when the merge produced no conflicts.
	Clean bool
	// MergedSnapshot is the tree-bearing snapshot carrying the merge result: a
	// freshly committed snapshot on the target (pending, for a conflict).
	MergedSnapshot object.ID
	// MergedRevisionID is the revision created on the target for the merge.
	MergedRevisionID string
	// Conflicts lists the paths carrying first-class conflict objects.
	Conflicts []string
}

// ComputeMerge performs the 3-way merge of the pinned snapshots against the
// CURRENT target tip and materializes the result as a snapshot on the target:
//
//   - clean -> a NEW single-parent revision on the target whose tree is the
//     merged result (the target history stays linear and the source snapshot is
//     never touched);
//   - conflicted -> a PENDING single-parent revision on the target whose tree
//     carries the conflict objects, recorded in rr_merge so Resolve can operate
//     on it and ApplyMerge can adopt the resolved snapshot.
//
// There is no fast-forward case: the source is always a bare snapshot, so a
// merge always produces a revision on the target.
//
// It does NOT move the target ref; ApplyMerge does that under the repo lock.
//
// base = rr.BaseSnapshot (creation-time target tip), ours = rr.SourceSnapshot,
// theirs = current target tip snapshot. The landing commit is authored by the
// SOURCE snapshot's author (who wrote the change), so blame attributes the lines
// to their author — not to whoever pressed merge. The caller-supplied `merger`
// is only the fallback when the source snapshot has no author; "who merged it"
// is recorded separately as rr_merge.merged_by.
func (s *Service) ComputeMerge(rr *store.RevisionRequest, merger store.Author) (MergeResult, error) {
	if rr.State == store.RRMerged || rr.State == store.RRClosed {
		return MergeResult{}, ErrNotMergeable
	}
	targetTip, err := s.branchTipSnapshot(rr.TargetBranch)
	if err != nil {
		return MergeResult{}, err
	}
	srcSnap, err := s.snapshotOf(rr.SourceSnapshot)
	if err != nil {
		return MergeResult{}, err
	}
	// base: the pinned creation-time target tip tree. When the source does NOT
	// descend from it (a non-descendant / root source), re-base onto the source's
	// own parent so the merge applies the source's CHANGES rather than wiping the
	// branch (see mergeBase).
	baseTree, err := s.mergeBase(rr, srcSnap)
	if err != nil {
		return MergeResult{}, err
	}
	// Current target tip tree (zero when the branch does not exist yet).
	targetTree := object.ID{}
	if !targetTip.IsZero() {
		ts, err := s.snapshotOf(targetTip.String())
		if err != nil {
			return MergeResult{}, err
		}
		targetTree = ts.TreeID
	}

	mergedTree, atoms, err := s.ws.Merge(baseTree, srcSnap.TreeID, targetTree)
	if err != nil {
		return MergeResult{}, err
	}
	res := MergeResult{Clean: len(atoms) == 0}
	for _, a := range atoms {
		res.Conflicts = append(res.Conflicts, a.Path)
	}

	// Materialize the merged tree as a NEW single-parent revision on the target
	// (parent = the current target tip snapshot). A conflicted revision carries
	// the conflict objects in its tree; it stays "pending" until Resolve produces
	// a clean one.
	var parents []object.ID
	if !targetTip.IsZero() {
		parents = []object.ID{targetTip}
	}
	// Author the landing commit as the SOURCE snapshot's author (who wrote the
	// change), so blame attributes the merged lines to their author rather than
	// to whoever pressed merge. Fall back to the merger when the source snapshot
	// carries no author.
	commitAuthor := srcSnap.Author
	if commitAuthor.Name == "" {
		commitAuthor = merger
	}
	ns, rev, err := s.ws.Commit(revision.CommitParams{
		Parents:     parents,
		TreeID:      mergedTree,
		Description: fmt.Sprintf("Merge revision request #%d: %s", rr.IID, rr.Title),
		Author:      commitAuthor,
	})
	if err != nil {
		return MergeResult{}, err
	}
	res.MergedSnapshot = ns.RevisionHash
	res.MergedRevisionID = rev.ID
	return res, nil
}

// ApplyMerge moves the target branch to the merge result. It is intended to run
// under the server's per-repo lock. It first adopts an existing PENDING,
// already-resolved merge snapshot (produced by Resolve) when one exists and is
// conflict-free; otherwise it recomputes. On conflicts it records the pending
// merge and marks the RR conflicted (the conflict objects stay in the tree;
// nothing is rolled back).
//
// `merger` is the caller's identity (fallback author when the source snapshot has
// none); `mergedBy` is the caller's user id, recorded as rr_merge.merged_by.
func (s *Service) ApplyMerge(rr *store.RevisionRequest, merger store.Author, mergedBy *int64) (MergeResult, error) {
	// Prefer a pending merge that Resolve has already made conflict-free.
	if res, ok, err := s.pendingMergeIfResolved(rr); err != nil {
		return res, err
	} else if ok {
		return s.finishMerge(rr, res, mergedBy)
	}

	res, err := s.ComputeMerge(rr, merger)
	if err != nil {
		return res, err
	}
	if !res.Clean {
		if err := s.cs.PutRRMerge(rr.ID, res.MergedSnapshot.String(), res.MergedRevisionID, "pending", mergedBy); err != nil {
			return res, err
		}
		if err := s.cs.UpdateRevisionRequestState(rr.RepoID, rr.IID, store.RRConflicted); err != nil {
			return res, err
		}
		return res, nil
	}
	return s.finishMerge(rr, res, mergedBy)
}

// finishMerge moves the target ref to the merge revision and records the RR as
// merged, recording mergedBy as the user who triggered it.
func (s *Service) finishMerge(rr *store.RevisionRequest, res MergeResult, mergedBy *int64) (MergeResult, error) {
	if res.MergedRevisionID == "" {
		return res, fmt.Errorf("rr: merge produced no revision to point the target at")
	}
	if _, err := s.ws.SetRef(rr.TargetBranch, store.RefBranch, res.MergedRevisionID); err != nil {
		return res, err
	}
	if err := s.cs.PutRRMerge(rr.ID, res.MergedSnapshot.String(), res.MergedRevisionID, "done", mergedBy); err != nil {
		return res, err
	}
	if err := s.cs.UpdateRevisionRequestState(rr.RepoID, rr.IID, store.RRMerged); err != nil {
		return res, err
	}
	return res, nil
}

// pendingMergeIfResolved returns a MergeResult built from an existing pending
// rr_merge whose snapshot is now conflict-free (Resolve ran). ok=false when
// there is no pending merge or it still carries conflicts.
func (s *Service) pendingMergeIfResolved(rr *store.RevisionRequest) (MergeResult, bool, error) {
	pm, err := s.cs.GetRRMerge(rr.ID)
	if err != nil || pm.State != "pending" || pm.MergedRevisionID == "" {
		return MergeResult{}, false, nil
	}
	rev, err := s.ws.GetRevision(pm.MergedRevisionID)
	if err != nil {
		return MergeResult{}, false, nil
	}
	snap, err := s.ws.GetSnapshot(rev.Hash)
	if err != nil {
		return MergeResult{}, false, nil
	}
	atoms, err := s.ws.ConflictsInTree(snap.TreeID)
	if err != nil {
		return MergeResult{}, false, nil
	}
	if len(atoms) > 0 {
		res := MergeResult{Clean: false, MergedSnapshot: snap.RevisionHash, MergedRevisionID: rev.ID}
		for _, a := range atoms {
			res.Conflicts = append(res.Conflicts, a.Path)
		}
		return res, false, nil
	}
	return MergeResult{Clean: true, MergedSnapshot: snap.RevisionHash, MergedRevisionID: rev.ID}, true, nil
}

// Resolve resolves a conflict at `path` in the RR's pending merge by choosing a
// side (an index into the conflict's add terms). It produces a genuinely
// conflict-free pending snapshot: the conflicted pending revision (created by
// ComputeMerge) is resolved in place via revision.Workspace.Resolve, which
// rewrites its tree with the chosen side and clears the conflict object. The RR
// is returned to open; a subsequent ApplyMerge adopts the resolved snapshot.
//
// `merger` is the caller's identity (fallback author when the source snapshot has
// none); `mergedBy` is the caller's user id, recorded as rr_merge.merged_by.
func (s *Service) Resolve(rr *store.RevisionRequest, path string, side int, merger store.Author, mergedBy *int64) (*store.RevisionRequest, error) {
	// Ensure a pending conflicted merge exists to resolve against.
	res, err := s.ComputeMerge(rr, merger)
	if err != nil {
		return nil, err
	}
	if res.Clean {
		return nil, fmt.Errorf("rr: no conflict to resolve")
	}
	pendingRev := res.MergedRevisionID
	if pendingRev == "" {
		return nil, fmt.Errorf("rr: no pending merge revision to resolve")
	}
	found := false
	for _, p := range res.Conflicts {
		if p == path {
			found = true
			break
		}
	}
	if !found {
		return nil, fmt.Errorf("rr: path %s is not conflicted in this request", path)
	}
	// Record the pending merge (idempotent) so ApplyMerge can find it.
	if err := s.cs.PutRRMerge(rr.ID, res.MergedSnapshot.String(), pendingRev, "pending", mergedBy); err != nil {
		return nil, err
	}
	// Genuinely resolve: rewrite the pending revision's tree with the chosen side.
	if _, _, err := s.ws.Resolve(pendingRev, path, side); err != nil {
		return nil, fmt.Errorf("rr: resolve %s: %w", path, err)
	}
	rev, err := s.ws.GetRevision(pendingRev)
	if err != nil {
		return nil, err
	}
	snap, err := s.ws.GetSnapshot(rev.Hash)
	if err != nil {
		return nil, err
	}
	remaining, err := s.ws.ConflictsInTree(snap.TreeID)
	if err != nil {
		return nil, err
	}
	if len(remaining) == 0 {
		if err := s.cs.UpdateRevisionRequestState(rr.RepoID, rr.IID, store.RROpen); err != nil {
			return nil, err
		}
	}
	return s.cs.GetRevisionRequest(rr.RepoID, rr.IID)
}

// baseTree resolves an RR's pinned base_snapshot to its tree id. A zero (or
// all-zero) base_snapshot means the target branch did not exist when the RR was
// opened (an empty repository), which is the empty tree: the source is then
// diffed/merged against nothing. revision.Workspace.Diff/Merge treat a zero
// tree id as the empty tree (mustTree), so the zero id is returned as-is.
func (s *Service) baseTree(rr *store.RevisionRequest) (object.ID, error) {
	id, err := object.HexToID(rr.BaseSnapshot)
	if err != nil {
		return object.ID{}, fmt.Errorf("rr: bad base snapshot hash %q: %w", rr.BaseSnapshot, err)
	}
	if id.IsZero() {
		return object.ID{}, nil
	}
	snap, err := s.repo.GetSnapshot(id)
	if err != nil {
		return object.ID{}, fmt.Errorf("rr: base snapshot %s not found: %w", rr.BaseSnapshot, err)
	}
	return snap.TreeID, nil
}

// mergeBase returns the 3-way merge base tree for an RR. It uses the RR's
// recorded base when the SOURCE snapshot descends from it; otherwise (a
// non-descendant source — e.g. a root revision produced by a commit to a
// non-existent branch) it re-bases onto the source's own parent. Without this,
// a non-descendant source with base=∅ would make the merge treat every target
// file the source lacks as a DELETION and wipe the branch.
func (s *Service) mergeBase(rr *store.RevisionRequest, srcSnap *store.Snapshot) (object.ID, error) {
	baseID, err := object.HexToID(rr.BaseSnapshot)
	if err != nil {
		return object.ID{}, fmt.Errorf("rr: bad base snapshot hash %q: %w", rr.BaseSnapshot, err)
	}
	if baseID.IsZero() {
		// Target branch was empty at creation: use the source's own parent as the
		// base (its true origin) when present.
		if len(srcSnap.Parents) > 0 {
			if ps, perr := s.repo.GetSnapshot(srcSnap.Parents[0]); perr == nil {
				return ps.TreeID, nil
			}
		}
		return object.ID{}, nil
	}
	if s.sourceDescendsFromBase(srcSnap, baseID) {
		bs, berr := s.repo.GetSnapshot(baseID)
		if berr != nil {
			return object.ID{}, fmt.Errorf("rr: base snapshot %s not found: %w", rr.BaseSnapshot, berr)
		}
		return bs.TreeID, nil
	}
	// Source does NOT descend from the recorded base: re-base onto the source's
	// own parent so the merge applies the source's CHANGES instead of replacing
	// the whole tree (the non-descendant wipe).
	if len(srcSnap.Parents) > 0 {
		if ps, perr := s.repo.GetSnapshot(srcSnap.Parents[0]); perr == nil {
			return ps.TreeID, nil
		}
	}
	return object.ID{}, nil
}

// sourceDescendsFromBase reports whether `src` is `base` or a descendant of it,
// walking the snapshot-parent DAG (bounded). A non-descendant means the RR's
// recorded base is not a valid 3-way base.
func (s *Service) sourceDescendsFromBase(src *store.Snapshot, base object.ID) bool {
	if src == nil {
		return false
	}
	seen := map[object.ID]bool{}
	queue := []object.ID{src.RevisionHash}
	for len(queue) > 0 && len(seen) < 100000 {
		cur := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		if cur == base {
			return true
		}
		if seen[cur] {
			continue
		}
		seen[cur] = true
		snap, err := s.repo.GetSnapshot(cur)
		if err != nil {
			continue
		}
		queue = append(queue, snap.Parents...)
	}
	return false
}

// treeOfSnapshot resolves a snapshot hash string to its tree id.
func (s *Service) treeOfSnapshot(hash string) (object.ID, error) {
	snap, err := s.snapshotOf(hash)
	if err != nil {
		return object.ID{}, err
	}
	return snap.TreeID, nil
}

// snapshotOf loads a snapshot by hex hash.
func (s *Service) snapshotOf(hash string) (*store.Snapshot, error) {
	id, err := object.HexToID(hash)
	if err != nil {
		return nil, fmt.Errorf("rr: bad snapshot hash %q: %w", hash, err)
	}
	snap, err := s.repo.GetSnapshot(id)
	if err != nil {
		return nil, fmt.Errorf("rr: snapshot %s not found: %w", hash, err)
	}
	return snap, nil
}
