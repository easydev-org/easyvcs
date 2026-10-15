package revision

import (
	"errors"
	"time"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// FileChangeSpec describes one file modification for an atomic commit.
type FileChangeSpec struct {
	// Path is the repo-relative path (forward slashes).
	Path string
	// Content, if set and Delete is false, writes the given content to the path
	// (creating or replacing the file).
	Content []byte
	// Delete removes the path if true. Content is ignored.
	Delete bool
}

// CommitFromChanges atomically creates a revision from a set of file changes
// applied on top of a parent snapshot. It builds the new tree by starting from
// the parent's tree (or an empty tree for a root commit) and applying each
// change, then writes the snapshot and revision in a single database
// transaction so the commit is atomic.
//
// A new revision_id is generated unless revisionIDOverride is non-empty (used
// by amend to keep the stable id). parents is the single parent_hash (or empty
// for a root commit). ChangedPaths is set to the changed paths. If parent_hash
// is non-empty but does not exist in this repository, an error is returned.
func (w *Workspace) CommitFromChanges(parentHash object.ID, changes []FileChangeSpec, description string, author store.Author, revisionIDOverride string) (*store.Snapshot, *store.Revision, error) {
	revisionID := revisionIDOverride
	if revisionID == "" {
		var err error
		revisionID, err = object.RandomRevisionID()
		if err != nil {
			return nil, nil, err
		}
	}

	var baseTree *object.Tree
	var parents []object.ID
	if parentHash != (object.ID{}) {
		parentSnap, err := w.store.GetSnapshot(parentHash)
		if err != nil {
			return nil, nil, errors.New("parent snapshot not found")
		}
		baseTree = w.mustTree(parentSnap.TreeID)
		parents = []object.ID{parentHash}
	} else {
		baseTree = object.NewTree()
	}

	// Amend (revisionIDOverride != ""): keep the revision's existing parent
	// pointers (1:1, replace-in-place) while applying the new content on top of
	// the parent snapshot. Without this, an amended snapshot would point at its
	// own previous snapshot as a parent.
	if revisionIDOverride != "" {
		if old, err := w.store.GetRevision(revisionIDOverride); err == nil {
			if oldSnap, err := w.store.GetSnapshot(old.Hash); err == nil {
				parents = oldSnap.Parents
			}
		}
	}

	// Apply each change to the working tree by rewriting the base tree.
	// Apply the changes, COLLECTING the new blobs (instead of writing each one
	// immediately) so they can be flushed in ONE batch inside the transaction —
	// a per-file write is a separate DB round-trip (and fsync).
	newTree := baseTree
	var blobs []*object.Object
	for _, c := range changes {
		if c.Delete {
			tid, err := w.resolvePath(newTree, c.Path, object.ID{}, true)
			if err != nil {
				return nil, nil, err
			}
			newTree = w.mustTree(tid)
			continue
		}
		blob := &object.Object{Kind: object.KindBlob, Blob: c.Content}
		blobs = append(blobs, blob)
		tid, err := w.resolvePath(newTree, c.Path, blob.ID(), false)
		if err != nil {
			return nil, nil, err
		}
		newTree = w.mustTree(tid)
	}

	// Write the tree(s) built by resolvePath (writeTree persists them) + the
	// root tree. These are already batched below via the transaction.
	treeID, err := w.writeTree(newTree)
	if err != nil {
		return nil, nil, err
	}
	snap := &store.Snapshot{
		RevisionID:  revisionID,
		Parents:     parents,
		TreeID:      treeID,
		Description: description,
		Author:      author,
		CommitTime:  time.Now().UTC(),
	}
	snap.RevisionHash = snapshotID(snap)

	// ChangedPaths is computed relative to the revision's PARENT snapshot
	// (parents[0]) — not the revision's own previous snapshot. For a NEW revision
	// these coincide, but for an AMEND they do NOT: the amend replaces the
	// snapshot in-place while the revision's parent stays fixed, so the logical
	// parent is the correct baseline. This matches Commit (change.go) and keeps
	// FileHistory / blame accurate when an amend accumulates several batches of
	// edits. It is computed here (before the transaction) from the parent's tree
	// and the new tree directly, so no mid-transaction read is needed.
	changedPaths := w.changedPathsFromTrees(parents, snap.TreeID)

	// Pre-read the root tree BEFORE opening the transaction: the sqlite pool
	// has a single connection, so a mid-transaction read would deadlock (pool
	// exhaustion).
	rootTree := w.mustTree(snap.TreeID)

	// Atomic transaction: objects + snapshot + revision.
	tx := w.store.BeginTx()
	defer func() { _ = tx.Rollback() }()

	// Write the tree object and any blobs referenced by the tree (leaf blobs).
	// We re-walk the tree to collect all referenced blob/tree objects so they are
	// persisted. Blobs were created via WriteBlob (already persisted individually)
	// but to be safe we write the root tree object here.
	treeObj := &object.Object{Kind: object.KindTree, Tree: rootTree}
	batch := append([]*object.Object{treeObj}, blobs...)
	if err := w.store.WriteObjectsBatchTx(tx, batch); err != nil {
		return nil, nil, err
	}
	if err := w.store.PutSnapshotTx(tx, snap); err != nil {
		return nil, nil, err
	}
	rev := &store.Revision{
		ID: revisionID, Hash: snap.RevisionHash, Created: time.Now().UTC(),
		ChangedPaths: changedPaths,
	}
	if err := w.store.PutRevisionTx(tx, rev); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit().Error; err != nil {
		return nil, nil, err
	}
	return snap, rev, nil
}
