package revision

import (
	"fmt"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// conflictAt returns the conflict object stored at path in the revision's
// current tree, or an error when there is none.
func (w *Workspace) conflictAt(revisionID, path string) (*object.Conflict, error) {
	rev, err := w.store.GetRevision(revisionID)
	if err != nil {
		return nil, ErrRevisionNotFound
	}
	cur, err := w.store.GetSnapshot(rev.Hash)
	if err != nil {
		return nil, err
	}
	entry, err := w.findEntry(w.mustTree(cur.TreeID), path)
	if err != nil {
		return nil, err
	}
	if entry.Kind != object.KindConflict {
		return nil, fmt.Errorf("no conflict at path %s", path)
	}
	obj, err := w.store.ReadObject(entry.ID)
	if err != nil {
		return nil, err
	}
	if obj.Kind != object.KindConflict || obj.Conflict == nil {
		return nil, fmt.Errorf("path %s does not hold a conflict", path)
	}
	return obj.Conflict, nil
}

// resolveApply rewrites the conflict at `path` in revisionID's current tree
// using fn, then propagates the SAME resolution to descendant revisions that
// still carry the identical conflict object. It returns the new snapshot and
// revision. The revision id is stable throughout.
func (w *Workspace) resolveApply(revisionID, path string, fn func(tree *object.Tree) (object.ID, error)) (*store.Snapshot, *store.Revision, error) {
	rev, err := w.store.GetRevision(revisionID)
	if err != nil {
		return nil, nil, ErrRevisionNotFound
	}
	cur, err := w.store.GetSnapshot(rev.Hash)
	if err != nil {
		return nil, nil, err
	}
	entry, err := w.findEntry(w.mustTree(cur.TreeID), path)
	if err != nil {
		return nil, nil, err
	}

	descendants, err := w.descendantsFromSnapshot(cur.RevisionHash)
	if err != nil {
		return nil, nil, err
	}

	newTreeID, err := fn(w.mustTree(cur.TreeID))
	if err != nil {
		return nil, nil, err
	}
	ns := w.newSnapshotFor(revisionID, cur, newTreeID)
	if err := w.store.PutSnapshot(ns); err != nil {
		return nil, nil, err
	}
	if err := w.store.UpdateRevisionHashCAS(revisionID, cur.RevisionHash, ns.RevisionHash); err != nil {
		return nil, nil, err
	}

	for _, d := range descendants {
		curD, err := w.store.GetSnapshot(d.Hash)
		if err != nil {
			continue
		}
		treeD := w.mustTree(curD.TreeID)
		dEntry, err := w.findEntry(treeD, path)
		if err != nil {
			continue
		}
		if dEntry.Kind != object.KindConflict || dEntry.ID != entry.ID {
			continue
		}
		newTreeD, err := fn(treeD)
		if err != nil {
			return nil, nil, err
		}
		nsD := w.newSnapshotFor(d.ID, curD, newTreeD)
		if err := w.store.PutSnapshot(nsD); err != nil {
			return nil, nil, err
		}
		if err := w.store.UpdateRevisionHash(d.ID, nsD.RevisionHash); err != nil {
			return nil, nil, err
		}
	}
	return ns, rev, nil
}

// newSnapshotFor builds a new snapshot for a revision with the given tree,
// keeping the revision's other metadata and id.
func (w *Workspace) newSnapshotFor(revisionID string, cur *store.Snapshot, treeID object.ID) *store.Snapshot {
	ns := &store.Snapshot{
		RevisionID:  revisionID,
		Parents:     cur.Parents,
		TreeID:      treeID,
		Description: cur.Description,
		Author:      cur.Author,
		CommitTime:  cur.CommitTime,
	}
	ns.RevisionHash = snapshotID(ns)
	return ns
}
