package revision

import (
	"fmt"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// Lift is the sub-repo scope "write" path (docs/subrepo-scopes.md §5): a change
// made IN a subdirectory is committed to the ROOT revision, touching only that
// subtree. The root branch is the single source of truth; the sub-scope's view
// is always a projection of it.
//
// Concretely, given a scope path Q and a set of file changes relative to Q,
// Lift:
//
//  1. takes the root tip snapshot's tree (the parent),
//  2. applies the changes to the SUBTREE at Q (creating it if absent),
//  3. commits a NEW root revision whose tree is the parent tree with that
//     subtree replaced,
//  4. repoints the root branch to the new revision.
//
// The new revision's ChangedPaths are the changed files UNDER Q (so the path
// index records exactly the sub-scope's paths — O(subtree), not O(repo)).
func (w *Workspace) Lift(rootBranch, scope string, changes []FileChangeSpec, description string, author store.Author) (*store.Snapshot, *store.Revision, error) {
	scope = strings.Trim(strings.TrimSpace(scope), "/")
	if scope == "" {
		return nil, nil, fmt.Errorf("lift: scope path is required (use the normal commit for the root)")
	}
	if len(changes) == 0 {
		return nil, nil, fmt.Errorf("lift: no changes")
	}

	// Parent = the current root branch tip (or an empty root if it has none).
	var parentSnap *store.Snapshot
	parentHash := object.ID{}
	if ref, err := w.store.GetRef(rootBranch); err == nil && ref != nil {
		rev, err := w.store.GetRevision(ref.Target)
		if err != nil {
			return nil, nil, fmt.Errorf("lift: root branch %q target missing: %w", rootBranch, err)
		}
		parentSnap, err = w.store.GetSnapshot(rev.Hash)
		if err != nil {
			return nil, nil, fmt.Errorf("lift: root tip snapshot missing: %w", err)
		}
		parentHash = parentSnap.RevisionHash
	}

	// The subtree currently at Q (empty if absent).
	var subTree *object.Tree
	if parentSnap != nil {
		root := w.mustTree(parentSnap.TreeID)
		if e, err := w.FindEntry(root, scope); err == nil && e.Kind == object.KindTree {
			subTree = w.mustTree(e.ID)
		}
	}
	if subTree == nil {
		subTree = object.NewTree()
	}

	// Apply the (scope-relative) changes to the subtree.
	for _, c := range changes {
		if strings.TrimSpace(c.Path) == "" {
			return nil, nil, fmt.Errorf("lift: empty change path")
		}
		var err error
		subTree, err = w.applyChange(subTree, c)
		if err != nil {
			return nil, nil, err
		}
	}
	subID, err := w.writeTree(subTree)
	if err != nil {
		return nil, nil, err
	}

	// Splice the new subtree back into the parent's root tree at Q.
	rootTree := object.NewTree()
	if parentSnap != nil {
		rootTree = w.mustTree(parentSnap.TreeID)
	}
	newRootID, err := w.spliceTree(rootTree, scope, subID)
	if err != nil {
		return nil, nil, err
	}

	var parents []object.ID
	if parentHash != (object.ID{}) {
		parents = []object.ID{parentHash}
	}
	snap, rev, err := w.Commit(CommitParams{
		Parents:     parents,
		TreeID:      newRootID,
		Description: description,
		Author:      author,
	})
	if err != nil {
		return nil, nil, err
	}
	// Repoint the ROOT branch at the new revision (the single source of truth).
	if _, err := w.SetRef(rootBranch, store.RefBranch, rev.ID); err != nil {
		return nil, nil, err
	}
	return snap, rev, nil
}

// applyChange applies one change (path relative to the subtree root) to a tree,
// returning the rewritten tree. It mirrors CommitFromChanges' file semantics.
func (w *Workspace) applyChange(t *object.Tree, c FileChangeSpec) (*object.Tree, error) {
	if c.Delete {
		id, err := w.resolvePath(t, c.Path, object.ID{}, true)
		if err != nil {
			return nil, err
		}
		return w.mustTree(id), nil
	}
	blobID, err := w.WriteBlob(c.Content)
	if err != nil {
		return nil, err
	}
	id, err := w.resolvePath(t, c.Path, blobID, false)
	if err != nil {
		return nil, err
	}
	return w.mustTree(id), nil
}

// SpliceSubtree returns a new tree identical to treeID but with the entry at
// `path` replaced by the TREE `sub`. Intermediate directories are created as
// needed. It is the primitive behind Lift and a scoped RR merge.
func (w *Workspace) SpliceSubtree(treeID object.ID, path string, sub object.ID) (object.ID, error) {
	var t *object.Tree
	if treeID == (object.ID{}) {
		t = object.NewTree()
	} else {
		t = w.mustTree(treeID)
	}
	return w.spliceTree(t, strings.Trim(strings.TrimSpace(path), "/"), sub)
}

// spliceTree returns a new root tree identical to t but with the entry at
// `path` replaced by the TREE `sub`. Intermediate directories are created as
// needed. A sub of the empty tree is allowed (an empty directory is not
// representable in git trees, so the entry is still written).
func (w *Workspace) spliceTree(t *object.Tree, path string, sub object.ID) (object.ID, error) {
	comps := strings.Split(path, "/")
	var rec func(t *object.Tree, idx int) (*object.Tree, error)
	rec = func(t *object.Tree, idx int) (*object.Tree, error) {
		clone := object.NewTree()
		for _, e := range t.SortedEntries() {
			clone.Entries[e.Name] = e
		}
		name := comps[idx]
		if idx == len(comps)-1 {
			clone.Entries[name] = object.Entry{Name: name, Kind: object.KindTree, ID: sub}
			return clone, nil
		}
		child := object.NewTree()
		if e, ok := t.Entries[name]; ok && e.Kind == object.KindTree {
			child = w.mustTree(e.ID)
		}
		nc, err := rec(child, idx+1)
		if err != nil {
			return nil, err
		}
		ncID, err := w.writeTree(nc)
		if err != nil {
			return nil, err
		}
		clone.Entries[name] = object.Entry{Name: name, Kind: object.KindTree, ID: ncID}
		return clone, nil
	}
	newTree, err := rec(t, 0)
	if err != nil {
		return object.ID{}, err
	}
	return w.writeTree(newTree)
}
