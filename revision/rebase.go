package revision

import (
	"fmt"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// Rebase repoints a revision onto a single parent, re-creating its snapshot
// with a new revision_hash while keeping the same revision id. The target tree
// is recomputed via a 3-way merge so the revision's own diff is genuinely
// re-applied onto the new base (not merely re-parented). Overlapping edits are
// recorded as first-class conflict objects in the resulting tree.
//
// newParents must be exactly one snapshot hash. Multi-parent rebase is not a
// supported operation (the model is single-parent / linear).
func (w *Workspace) Rebase(revisionID string, newParents []object.ID) (*store.Snapshot, *store.Revision, error) {
	if len(newParents) != 1 {
		return nil, nil, fmt.Errorf("rebase: expected exactly one new parent, got %d", len(newParents))
	}
	rev, err := w.store.GetRevision(revisionID)
	if err != nil {
		return nil, nil, ErrRevisionNotFound
	}
	cur, err := w.store.GetSnapshot(rev.Hash)
	if err != nil {
		return nil, nil, err
	}

	newBase, err := w.store.GetSnapshot(newParents[0])
	if err != nil {
		return nil, nil, fmt.Errorf("rebase: new parent snapshot not found: %w", err)
	}

	// The 3-way base is the revision's original first parent tree (the point
	// at which it diverged). A root revision uses the empty tree as its base.
	var baseTree object.ID
	if len(cur.Parents) > 0 {
		if pSnap, err := w.store.GetSnapshot(cur.Parents[0]); err == nil {
			baseTree = pSnap.TreeID
		}
	}
	// Merge(base, ours=cur, theirs=newBase) re-applies the revision's own
	// delta (ours) onto the new base (theirs); overlapping edits become
	// conflict objects. "ours" is kept as the added side so a resolve to side 0
	// prefers the rebased revision's own content.
	mergedTree, _, err := w.Merge(baseTree, cur.TreeID, newBase.TreeID)
	if err != nil {
		return nil, nil, err
	}

	ns := &store.Snapshot{
		RevisionID:  revisionID,
		Parents:     newParents, // single parent
		TreeID:      mergedTree,
		Description: cur.Description,
		Author:      cur.Author,
		CommitTime:  cur.CommitTime,
	}
	ns.RevisionHash = snapshotID(ns)
	if err := w.store.PutSnapshot(ns); err != nil {
		return nil, nil, err
	}
	// Optimistic concurrency: only repoint if the caller's snapshot is still the
	// current one (avoids last-write-wins when two writers rewrite simultaneously).
	if err := w.store.UpdateRevisionHashCAS(revisionID, cur.RevisionHash, ns.RevisionHash); err != nil {
		return nil, nil, err
	}
	rev.Hash = ns.RevisionHash
	rev.ChangedPaths = w.changedPathsBetween(ns.Parents, ns.RevisionHash)
	if err := w.store.PutRevision(rev); err != nil {
		return nil, nil, err
	}
	return ns, rev, nil
}

// RebaseMany moves a set of revisions onto a new base in one chained operation,
// re-parenting them in ancestor order so the result is a linear sequence. It is
// the multi-revision convenience over Rebase: for example, rebasing [D, H]
// (with chain B-D-G-H) onto F yields A-B-C-E-F-D-H, dropping G. Revisions not
// in the set (e.g. G) become unreachable and are effectively discarded.
//
// revs are revision ids in the order they should appear on the new line (tip
// last). Every moved revision keeps its own id and has its tree recomputed via
// 3-way merge; overlapping edits become first-class conflict objects.
func (w *Workspace) RebaseMany(revs []string, onto object.ID) ([]*store.Revision, error) {
	if len(revs) == 0 {
		return nil, nil
	}
	parent := onto
	var moved []*store.Revision
	for _, rid := range revs {
		// Rebase onto the previous node in the sequence (or the given onto).
		_, ch, err := w.Rebase(rid, []object.ID{parent})
		if err != nil {
			return moved, err
		}
		moved = append(moved, ch)
		parent = ch.Hash
	}
	return moved, nil
}

// Drop removes a revision from history by rebasing all of its descendants onto
// the revision's parent, then re-pointing them so the dropped revision becomes
// unreachable. It is the "erase from history" operation (no op-log preserved).
// Returns the moved descendant revisions.
func (w *Workspace) Drop(revID string) ([]*store.Revision, error) {
	rev, err := w.store.GetRevision(revID)
	if err != nil {
		return nil, ErrRevisionNotFound
	}
	snap, err := w.store.GetSnapshot(rev.Hash)
	if err != nil {
		return nil, err
	}
	if len(snap.Parents) == 0 {
		// Dropping the root: no parent to rebase onto; refuse.
		return nil, fmt.Errorf("drop: cannot drop a root revision")
	}
	parentSnap, err := w.store.GetSnapshot(snap.Parents[0])
	if err != nil {
		return nil, err
	}
	desc, err := w.DescendantsOf(revID)
	if err != nil {
		return nil, err
	}
	return w.RebaseMany(desc, parentSnap.RevisionHash)
}

// Revert creates a new revision that undoes the changes introduced by revID,
// applied on top of onto (default: the current tip). The historical revision is
// preserved; this is the inverse-patch model (like `git revert`), distinct from
// Drop which erases the revision. Conflicts appear as first-class objects.
//
// It authors the new revision with the default identity; use RevertAs to
// attribute it to the caller.
func (w *Workspace) Revert(revID string, onto object.ID) (*store.Snapshot, *store.Revision, error) {
	return w.RevertAs(revID, onto, store.Author{Name: "easyvcs", Email: "easyvcs@example.com"})
}

// RevertAs is Revert with an explicit author (the caller's identity).
func (w *Workspace) RevertAs(revID string, onto object.ID, author store.Author) (*store.Snapshot, *store.Revision, error) {
	rev, err := w.store.GetRevision(revID)
	if err != nil {
		return nil, nil, ErrRevisionNotFound
	}
	revSnap, err := w.store.GetSnapshot(rev.Hash)
	if err != nil {
		return nil, nil, err
	}
	// The inverse base: the revision's parent tree (undoing rev's diff).
	var inverseBase object.ID
	if len(revSnap.Parents) > 0 {
		p, err := w.store.GetSnapshot(revSnap.Parents[0])
		if err != nil {
			return nil, nil, err
		}
		inverseBase = p.TreeID
	} else {
		inverseBase = object.ID{} // root: undoing it means empty tree
	}

	// Find the target to revert onto (current tip if onto is zero).
	var ontoSnap *store.Snapshot
	if onto.IsZero() {
		revs, err := w.store.ListRevisions()
		if err != nil || len(revs) == 0 {
			return nil, nil, fmt.Errorf("revert: no revisions")
		}
		newest := newestRevisionID(revs)
		r, err := w.store.GetRevision(newest)
		if err != nil {
			return nil, nil, err
		}
		ontoSnap, err = w.store.GetSnapshot(r.Hash)
		if err != nil {
			return nil, nil, err
		}
	} else {
		ontoSnap, err = w.store.GetSnapshot(onto)
		if err != nil {
			return nil, nil, fmt.Errorf("revert: onto snapshot not found: %w", err)
		}
	}

	// 3-way: base = rev's tree, ours = rev's parent tree (undo), theirs = onto.
	// Overlaps (where onto diverges from the reverted content) become conflicts.
	mergedTree, _, err := w.Merge(revSnap.TreeID, inverseBase, ontoSnap.TreeID)
	if err != nil {
		return nil, nil, err
	}

	return w.Commit(CommitParams{
		Parents:     []object.ID{ontoSnap.RevisionHash},
		TreeID:      mergedTree,
		Description: "Revert " + revID,
		Author:      author,
	})
}

// revisionHashOf resolves a revision id to its current snapshot hash.
func revisionHashOf(revID string, w *Workspace) object.ID {
	r, err := w.store.GetRevision(revID)
	if err != nil {
		return object.ID{}
	}
	return r.Hash
}

// Squash absorbs the changes in a revision into its parent revision, keeping
// the parent revision's id stable.
func (w *Workspace) Squash(childRevisionID string) (*store.Snapshot, *store.Revision, error) {
	child, err := w.store.GetRevision(childRevisionID)
	if err != nil {
		return nil, nil, ErrRevisionNotFound
	}
	childSnap, err := w.store.GetSnapshot(child.Hash)
	if err != nil {
		return nil, nil, err
	}
	if len(childSnap.Parents) == 0 {
		return nil, nil, fmt.Errorf("child revision has no parent to squash into")
	}
	parentSnap, err := w.store.GetSnapshot(childSnap.Parents[0])
	if err != nil {
		return nil, nil, err
	}
	parentRev, err := w.store.GetRevision(parentSnap.RevisionID)
	if err != nil {
		return nil, nil, err
	}
	merged, _, err := w.Merge(parentSnap.TreeID, parentSnap.TreeID, childSnap.TreeID)
	if err != nil {
		return nil, nil, err
	}
	ns := &store.Snapshot{
		RevisionID:  parentRev.ID,
		Parents:     parentSnap.Parents,
		TreeID:      merged,
		Description: parentSnap.Description,
		Author:      parentSnap.Author,
	}
	ns.RevisionHash = snapshotID(ns)
	if err := w.store.PutSnapshot(ns); err != nil {
		return nil, nil, err
	}
	// Guard the parent revision against concurrent rewrites: the expected value
	// is the parent REVISION's current hash, not the snapshot the child pointed
	// at (the parent may have been re-written since the child was created).
	if err := w.store.UpdateRevisionHashCAS(parentRev.ID, parentRev.Hash, ns.RevisionHash); err != nil {
		return nil, nil, err
	}
	return ns, parentRev, nil
}
