package revision

import (
	"sort"

	"github.com/easylab-platform/easyvcs/object"
)

// Diff walks two trees and lists per-file differences.
func (w *Workspace) Diff(a, b object.ID) ([]FileChange, error) {
	ta := w.mustTree(a)
	tb := w.mustTree(b)
	var out []FileChange
	if err := w.diffTree("", ta, tb, &out); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// DiffWithRenames is Diff plus deterministic rename detection. A path removed on
// the base side and a path added on the new side whose blob id is IDENTICAL are
// paired into a single StatusRenamed change (OldPath = the base path, Path = the
// new path). Only exact content-id matches are paired — no similarity
// heuristics — so the result is deterministic and cheap. Renames are resolved
// greedily in path order when one blob id maps to several candidates.
//
// Diff (without renames) is unchanged and remains the default; callers opt in to
// rename detection explicitly.
func (w *Workspace) DiffWithRenames(a, b object.ID) ([]FileChange, error) {
	changes, err := w.Diff(a, b)
	if err != nil {
		return nil, err
	}
	return pairRenames(changes), nil
}

// pairRenames folds a removed+added pair sharing a blob id into a rename.
func pairRenames(changes []FileChange) []FileChange {
	addedByID := map[object.ID][]string{}
	for _, c := range changes {
		if c.Status == StatusAdded {
			addedByID[c.NewID] = append(addedByID[c.NewID], c.Path)
		}
	}
	// First pass: decide the pairings (removed path -> added path), consuming
	// each added path at most once. Deciding up-front avoids the ordering trap
	// where an added entry is emitted before its matching removed entry.
	renameByRemoved := map[string]string{} // removed path -> added path
	usedAdded := map[string]bool{}
	for _, c := range changes {
		if c.Status != StatusRemoved {
			continue
		}
		for _, p := range addedByID[c.OldID] {
			if !usedAdded[p] {
				usedAdded[p] = true
				renameByRemoved[c.Path] = p
				break
			}
		}
	}
	// Second pass: emit.
	var out []FileChange
	for _, c := range changes {
		switch c.Status {
		case StatusRemoved:
			if to, ok := renameByRemoved[c.Path]; ok {
				out = append(out, FileChange{
					Path: to, Status: StatusRenamed, OldPath: c.Path,
					OldID: c.OldID, NewID: c.OldID,
				})
				continue
			}
			out = append(out, c)
		case StatusAdded:
			if usedAdded[c.Path] {
				continue // consumed by a rename pair
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// DiffBaseForRevision returns the tree to diff a revision against, always the
// first parent's tree (root -> empty tree). The model is single-parent/linear:
// every revision has exactly one parent, so the diff base is unambiguous.
func (w *Workspace) DiffBaseForRevision(revisionID string) (object.ID, error) {
	rev, err := w.store.GetRevision(revisionID)
	if err != nil {
		return object.ID{}, err
	}
	snap, err := w.store.GetSnapshot(rev.Hash)
	if err != nil {
		return object.ID{}, err
	}
	if len(snap.Parents) == 0 {
		return object.ID{}, nil // root
	}
	parent, err := w.store.GetSnapshot(snap.Parents[0])
	if err != nil {
		return object.ID{}, err
	}
	return parent.TreeID, nil
}

// FilesChanged returns the per-file change list for a revision, expressed
// against its first-parent base (root -> empty). This is the "which files did
// this revision, and how" view.
func (w *Workspace) FilesChanged(revisionID string) ([]FileChange, error) {
	rev, err := w.store.GetRevision(revisionID)
	if err != nil {
		return nil, err
	}
	snap, err := w.store.GetSnapshot(rev.Hash)
	if err != nil {
		return nil, err
	}
	base, err := w.DiffBaseForRevision(revisionID)
	if err != nil {
		return nil, err
	}
	return w.Diff(base, snap.TreeID)
}

func (w *Workspace) diffTree(prefix string, a, b *object.Tree, out *[]FileChange) error {
	names := map[string]struct{}{}
	for _, t := range []*object.Tree{a, b} {
		for n := range t.Entries {
			names[n] = struct{}{}
		}
	}
	all := make([]string, 0, len(names))
	for n := range names {
		all = append(all, n)
	}
	sort.Strings(all)
	for _, name := range all {
		ea, aHas := a.Entries[name]
		eb, bHas := b.Entries[name]
		path := joinPath(prefix, name)
		aTree, aIsTree := w.readTreeEntryOrNil(ea, aHas)
		bTree, bIsTree := w.readTreeEntryOrNil(eb, bHas)
		switch {
		case aIsTree && bIsTree:
			if err := w.diffTree(path, aTree, bTree, out); err != nil {
				return err
			}
		case aIsTree && !bIsTree:
			w.flattenTree(path, aTree, out, StatusRemoved)
		case !aIsTree && bIsTree:
			w.flattenTree(path, bTree, out, StatusAdded)
		default:
			aBlob := aHas && ea.Kind == object.KindBlob
			bBlob := bHas && eb.Kind == object.KindBlob
			switch {
			case aBlob && bBlob && ea.ID == eb.ID:
			case aBlob && bBlob:
				*out = append(*out, FileChange{Path: path, Status: StatusModified, OldID: ea.ID, NewID: eb.ID})
			case aBlob && !bBlob:
				*out = append(*out, FileChange{Path: path, Status: StatusRemoved, OldID: ea.ID})
			case !aBlob && bBlob:
				*out = append(*out, FileChange{Path: path, Status: StatusAdded, NewID: eb.ID})
			}
		}
	}
	return nil
}

func (w *Workspace) flattenTree(prefix string, t *object.Tree, out *[]FileChange, status Status) {
	for _, e := range t.SortedEntries() {
		path := joinPath(prefix, e.Name)
		if sub, isTree := w.readTreeEntry(e); isTree {
			w.flattenTree(path, sub, out, status)
			continue
		}
		fc := FileChange{Path: path, Status: status}
		if status == StatusRemoved {
			fc.OldID = e.ID
		} else {
			fc.NewID = e.ID
		}
		*out = append(*out, fc)
	}
}
