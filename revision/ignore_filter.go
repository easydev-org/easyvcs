package revision

import (
	"github.com/easylab-platform/easyvcs/ignore"
	"github.com/easylab-platform/easyvcs/object"
)

// DropIgnoredPathsFromTree removes every path in a tree that is matched by the
// ignore matcher (compiled from the given root). It recursively rewrites trees,
// dropping ignored file leaves and pruning empty directories. It is used to
// filter a NEW tree before committing (see RebuildTreeFiltered); it never
// touches existing history.
func (w *Workspace) DropIgnoredPathsFromTree(root string, m *ignore.Matcher, treeID object.ID) (object.ID, error) {
	tree := w.mustTree(treeID)
	return w.dropIgnored(root, m, tree, "")
}

func (w *Workspace) dropIgnored(root string, m *ignore.Matcher, tree *object.Tree, prefix string) (object.ID, error) {
	filtered := object.NewTree()
	for _, e := range tree.SortedEntries() {
		rel := joinPath(prefix, e.Name)
		// Fast-path: if no ignore pattern could possibly reach this subtree,
		// keep it (and all of its descendants) verbatim without recursing.
		if e.Kind == object.KindTree && !m.HasAnyMatchUnder(rel) {
			filtered.Entries[e.Name] = e
			continue
		}
		if m.Ignored(rel) {
			continue
		}
		if e.Kind == object.KindTree {
			sub, err := w.ReadTree(e.ID)
			if err != nil {
				return object.ID{}, err
			}
			newSubID, err := w.dropIgnored(root, m, sub, rel)
			if err != nil {
				return object.ID{}, err
			}
			newSub := w.mustTree(newSubID)
			// Prune empty directories.
			if len(newSub.Entries) == 0 {
				continue
			}
			filtered.Entries[e.Name] = object.Entry{Name: e.Name, Kind: object.KindTree, ID: newSubID}
		} else {
			filtered.Entries[e.Name] = e
		}
	}
	treeID, err := w.writeTree(filtered)
	if err != nil {
		return object.ID{}, err
	}
	return treeID, nil
}

// RebuildTreeFiltered is a convenience that reads a tree, applies a matcher and
// writes back a filtered tree id. Root is the repo root (for relative ignore
// paths). It filters a NEW tree before committing (never rewrites history).
func (w *Workspace) RebuildTreeFiltered(root string, m *ignore.Matcher, treeID object.ID) (object.ID, error) {
	return w.DropIgnoredPathsFromTree(root, m, treeID)
}
