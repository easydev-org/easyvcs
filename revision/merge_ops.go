package revision

import (
	"fmt"

	"github.com/easylab-platform/easyvcs/merge"
	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// Merge performs a 3-way merge of the trees of two snapshots, recording any
// conflicts as first-class objects in the resulting tree. A persistence failure
// while storing the merged tree (or an embedded conflict object) is returned as
// an error.
func (w *Workspace) Merge(base, ours, theirs object.ID) (object.ID, []merge.ConflictAtom, error) {
	baseTree := w.mustTree(base)
	oursTree := w.mustTree(ours)
	theirsTree := w.mustTree(theirs)
	merged, atoms, err := merge.Trees(baseTree, oursTree, theirsTree, w)
	if err != nil {
		return object.ID{}, nil, err
	}
	mergedID, err := w.writeTree(merged)
	if err != nil {
		return object.ID{}, nil, err
	}
	return mergedID, atoms, nil
}

// Resolve replaces the conflict at the given path in a revision's current tree
// with a chosen side, keeping the same revision id. Resolution propagates to
// descendant revisions that still carry the same conflict.
func (w *Workspace) Resolve(revisionID string, path string, sideIndex int) (*store.Snapshot, *store.Revision, error) {
	conflict, err := w.conflictAt(revisionID, path)
	if err != nil {
		return nil, nil, err
	}
	if sideIndex < 0 || sideIndex >= len(conflict.Adds) {
		return nil, nil, fmt.Errorf("side index %d out of range (0..%d)", sideIndex, len(conflict.Adds)-1)
	}
	term := conflict.Adds[sideIndex]
	return w.resolveApply(revisionID, path, func(tree *object.Tree) (object.ID, error) {
		if term.ID == (object.ID{}) {
			return w.resolvePath(tree, path, object.ID{}, true)
		}
		return w.resolvePath(tree, path, term.ID, false)
	})
}

// ResolveToContent resolves the conflict at `path` by writing `content` as a new
// blob at that path, keeping the revision id. It is the manual-merge escape
// hatch: a binary conflict (where side-selection is the only structural option)
// can be resolved by supplying the merged bytes directly. The new blob is
// content-addressed, so identical resolutions dedupe.
func (w *Workspace) ResolveToContent(revisionID string, path string, content []byte) (*store.Snapshot, *store.Revision, error) {
	if _, err := w.conflictAt(revisionID, path); err != nil {
		return nil, nil, err
	}
	blobID, err := w.WriteBlob(content)
	if err != nil {
		return nil, nil, err
	}
	return w.resolveApply(revisionID, path, func(tree *object.Tree) (object.ID, error) {
		return w.resolvePath(tree, path, blobID, false)
	})
}

// ResolveToPath resolves a rename conflict at `path` by choosing which side's
// PATH wins. chosenPath must equal the Path of one of the conflict's add terms
// (for a path-less conflict this is an error). The result places that term's
// content at chosenPath and removes the conflict path plus every other
// add-term path, so a rename conflict settles on exactly one location.
func (w *Workspace) ResolveToPath(revisionID string, path string, chosenPath string) (*store.Snapshot, *store.Revision, error) {
	conflict, err := w.conflictAt(revisionID, path)
	if err != nil {
		return nil, nil, err
	}
	var chosen object.ID
	found := false
	var others []string
	for _, t := range conflict.Adds {
		if t.Path == chosenPath {
			chosen, found = t.ID, true
			continue
		}
		if t.Path != "" && t.Path != path {
			others = append(others, t.Path)
		}
	}
	if !found {
		return nil, nil, fmt.Errorf("no side at path %q in the conflict at %s", chosenPath, path)
	}
	return w.resolveApply(revisionID, path, func(tree *object.Tree) (object.ID, error) {
		// Place the chosen content at chosenPath, then clear the conflict path
		// and every other side's path.
		tid, err := w.resolvePath(tree, chosenPath, chosen, false)
		if err != nil {
			return object.ID{}, err
		}
		if chosenPath != path {
			if tid, err = w.resolvePath(w.mustTree(tid), path, object.ID{}, true); err != nil {
				return object.ID{}, err
			}
		}
		for _, p := range others {
			if p == chosenPath {
				continue
			}
			if tid, err = w.resolvePath(w.mustTree(tid), p, object.ID{}, true); err != nil {
				return object.ID{}, err
			}
		}
		return tid, nil
	})
}
