// Package merge implements N-way tree merge and first-class conflicts.
//
// A merge combines two or more divergent trees relative to a common base. When
// the changes don't conflict, the result is a merged tree. When they do,
// EasyVCS records a conflict as a first-class, content-addressed object (like
// jj's Merge<T>) rather than failing the operation. The conflict object is
// stored in the tree at the conflicting path and can be surfaced, resolved, or
// propagated to descendants.
package merge

import (
	"fmt"
	"sort"

	"github.com/easylab-platform/easyvcs/object"
)

// treeOps is the minimal object access needed by a merge. It is implemented by
// revision.Workspace, keeping merge independent of the storage backend. The
// write methods return an error so a failed persistence (e.g. an oversized
// blob) is propagated rather than silently dropped.
type treeOps interface {
	ReadBlob(id object.ID) ([]byte, error)
	ReadTree(id object.ID) (*object.Tree, error)
	// ReadConflict loads a conflict object (so a nested conflict can be FLATTENED
	// into an N-way one instead of being treated as an opaque blob).
	ReadConflict(id object.ID) (*object.Conflict, error)
	// MergeText attempts a diff3-style line merge of (base, ours, theirs). It
	// returns (merged, true) when the two sides touch DISJOINT line regions (a
	// safe textual merge) and (nil, false) on overlap / binary / missing base —
	// the caller then builds a first-class conflict object.
	MergeText(base, ours, theirs []byte) ([]byte, bool)
	WriteBlob(data []byte) (object.ID, error)
	WriteTree(t *object.Tree) (object.ID, error)
	WriteConflict(c *object.Conflict) (object.ID, error)
}

// ConflictAtom is a lightweight record of a conflict location in a merged
// tree: the path and the conflict object id stored there.
type ConflictAtom struct {
	Path string
	ID   object.ID
}

// absent is a sentinel id representing "no content / deleted".
var absent object.ID

// Trees performs a recursive 3-way tree merge. It returns the merged tree
// (with conflict objects embedded at conflicting paths) and the list of
// conflict atoms that were produced. A persistence failure while writing a
// sub-tree or conflict object is propagated as an error.
func Trees(base, ours, theirs *object.Tree, ops treeOps) (*object.Tree, []ConflictAtom, error) {
	merged := object.NewTree()
	var atoms []ConflictAtom

	names := map[string]struct{}{}
	for _, t := range []*object.Tree{base, ours, theirs} {
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
		b := base.Entries[name]
		o := ours.Entries[name]
		t := theirs.Entries[name]

		entry, subAtoms, err := mergeEntry(name, b, o, t, ops)
		if err != nil {
			return nil, nil, err
		}
		atoms = append(atoms, subAtoms...)
		if entry != nil {
			merged.Entries[name] = *entry
		}
	}

	// Second pass: rename conflicts. The per-path loop above cannot see that the
	// same base blob reappeared at DIFFERENT new paths (a rename), so it would
	// leave both new paths as clean adds — and for rename/modify it would emit a
	// plain content conflict at the base path. Detect renames here and replace
	// whatever the first pass produced at the base path with one path-bearing
	// conflict.
	renameAtoms, handled, err := detectRenameConflicts(base, ours, theirs, merged, ops)
	if err != nil {
		return nil, nil, err
	}
	if len(handled) > 0 {
		kept := atoms[:0]
		for _, a := range atoms {
			if !handled[a.Path] {
				kept = append(kept, a)
			}
		}
		atoms = kept
	}
	atoms = append(atoms, renameAtoms...)
	return merged, atoms, nil
}

// blobID returns the entry's blob id and whether it is a present blob.
func blobID(e object.Entry) (object.ID, bool) {
	if e.Kind != object.KindBlob || e.ID == (object.ID{}) {
		return object.ID{}, false
	}
	return e.ID, true
}

// detectRenameConflicts finds rename conflicts at one directory level and
// rewrites `merged` in place. A rename conflict is a base blob that one or both
// sides moved to a DIFFERENT path rather than editing it in place:
//
//	rename/rename   base f=X, ours -> g=X, theirs -> h=X (g != h)
//	rename/modify   base f=X, ours -> g=X, theirs edits f to Y
//	modify/rename   base f=X, ours edits f to Y, theirs -> h=X
//
// For each, the new-path clean adds are removed and a single conflict object is
// placed at the base path f. Its terms carry Path so a resolver can pick a side
// (which path wins) or resolve to arbitrary content. A pure rename on one side
// with the other side unchanged is NOT a conflict and is left as-is.
//
// It returns the conflict atoms it created plus the set of base paths it
// handled, so the caller can drop any first-pass atom at those paths.
func detectRenameConflicts(base, ours, theirs, merged *object.Tree, ops treeOps) ([]ConflictAtom, map[string]bool, error) {
	addedByID := func(side *object.Tree) map[object.ID][]string {
		m := map[object.ID][]string{}
		for name, e := range side.Entries {
			if _, inBase := base.Entries[name]; inBase {
				continue
			}
			if id, ok := blobID(e); ok {
				m[id] = append(m[id], name)
			}
		}
		for _, v := range m {
			sort.Strings(v)
		}
		return m
	}
	oursAdded := addedByID(ours)
	theirsAdded := addedByID(theirs)

	baseNames := make([]string, 0, len(base.Entries))
	for n := range base.Entries {
		baseNames = append(baseNames, n)
	}
	sort.Strings(baseNames)

	var atoms []ConflictAtom
	handled := map[string]bool{}
	for _, f := range baseNames {
		x, ok := blobID(base.Entries[f])
		if !ok {
			continue // directory or absent base entry
		}
		oursID, oursHasF := blobID(ours.Entries[f])
		theirsID, theirsHasF := blobID(theirs.Entries[f])

		oursRename := ""
		if !oursHasF {
			oursRename = firstNonBase(oursAdded[x], f)
		}
		theirsRename := ""
		if !theirsHasF {
			theirsRename = firstNonBase(theirsAdded[x], f)
		}

		var c *object.Conflict
		var consumed []string
		switch {
		case oursRename != "" && theirsRename != "" && oursRename != theirsRename:
			c = &object.Conflict{
				Removes: []object.Term{{ID: x, Label: "base", Path: f}},
				Adds: []object.Term{
					{ID: x, Label: "ours", Path: oursRename},
					{ID: x, Label: "theirs", Path: theirsRename},
				},
			}
			consumed = []string{oursRename, theirsRename}
		case oursRename != "" && theirsHasF && theirsID != x:
			c = &object.Conflict{
				Removes: []object.Term{{ID: x, Label: "base", Path: f}},
				Adds: []object.Term{
					{ID: x, Label: "ours", Path: oursRename},
					{ID: theirsID, Label: "theirs", Path: f},
				},
			}
			consumed = []string{oursRename}
		case theirsRename != "" && oursHasF && oursID != x:
			c = &object.Conflict{
				Removes: []object.Term{{ID: x, Label: "base", Path: f}},
				Adds: []object.Term{
					{ID: oursID, Label: "ours", Path: f},
					{ID: x, Label: "theirs", Path: theirsRename},
				},
			}
			consumed = []string{theirsRename}
		}
		if c == nil {
			continue
		}
		cid, err := ops.WriteConflict(c)
		if err != nil {
			return nil, nil, err
		}
		merged.Entries[f] = object.Entry{Name: f, Kind: object.KindConflict, ID: cid}
		for _, p := range consumed {
			delete(merged.Entries, p)
		}
		atoms = append(atoms, ConflictAtom{Path: f, ID: cid})
		handled[f] = true
	}
	return atoms, handled, nil
}

// firstNonBase returns the first path in cands that is not the base path.
func firstNonBase(cands []string, base string) string {
	for _, p := range cands {
		if p != base {
			return p
		}
	}
	return ""
}

func blankID() object.ID { var z object.ID; return z }

// present reports whether an entry actually holds content, i.e. its blob id is
// non-zero. A missing (zero-value) Entry has Kind==KindBlob and ID==0, which
// otherwise would be mistaken for a present empty blob. We treat a non-zero id
// as present, and the zero id as "absent" (deleted/not added).
func present(e object.Entry) bool { return e.ID != (object.ID{}) }

func isTree(e object.Entry) bool { return e.Kind == object.KindTree }

func mergeEntry(name string, base, ours, theirs object.Entry, ops treeOps) (*object.Entry, []ConflictAtom, error) {
	// Directory/file (D/F) conflict: one side made this path a DIRECTORY while
	// the other kept it a FILE (both present). This is NOT a tree merge — doing
	// so would silently DROP the file side. Emit a conflict carrying both the
	// tree and the blob as terms, so the user chooses which shape wins.
	oursDir := ours.Kind == object.KindTree
	theirsDir := theirs.Kind == object.KindTree
	oursFile := present(ours) && !oursDir
	theirsFile := present(theirs) && !theirsDir
	if (oursDir && theirsFile) || (oursFile && theirsDir) {
		return dfConflict(name, base, ours, theirs, ops)
	}

	baseTree, baseIsTree := entryAsTree(base, ops)
	oursTree, oursIsTree := entryAsTree(ours, ops)
	theirsTree, theirsIsTree := entryAsTree(theirs, ops)

	// If any side is a tree, merge as trees (missing sides are empty subtrees).
	if baseIsTree || oursIsTree || theirsIsTree {
		bt, ot, tt := baseTree, oursTree, theirsTree
		if bt == nil {
			bt = object.NewTree()
		}
		if ot == nil {
			ot = object.NewTree()
		}
		if tt == nil {
			tt = object.NewTree()
		}
		sub, subAtoms, err := Trees(bt, ot, tt, ops)
		if err != nil {
			return nil, nil, err
		}
		for i := range subAtoms {
			subAtoms[i].Path = joinPath(name, subAtoms[i].Path)
		}
		if len(sub.Entries) == 0 {
			return nil, subAtoms, nil
		}
		subID, err := ops.WriteTree(sub)
		if err != nil {
			return nil, nil, err
		}
		return &object.Entry{Name: name, Kind: object.KindTree, ID: subID}, subAtoms, nil
	}

	// Determine presence of each side as a blob (or absent), by non-zero id.
	baseP := present(base)
	oursP := present(ours)
	theirsP := present(theirs)

	// Values of the three sides (absent -> zero id).
	bv, ov, tv := blankID(), blankID(), blankID()
	if baseP {
		bv = base.ID
	}
	if oursP {
		ov = ours.ID
	}
	if theirsP {
		tv = theirs.ID
	}

	// Resolve automatically where possible.
	// Both sides identical.
	if oursP && theirsP && ours.ID == theirs.ID {
		return &ours, nil, nil
	}
	// One side unchanged from base.
	if baseP && oursP && theirsP {
		if ours.ID == base.ID {
			return &theirs, nil, nil
		}
		if theirs.ID == base.ID {
			return &ours, nil, nil
		}
		// Both sides changed the SAME blob. Attempt a LINE-LEVEL 3-way merge:
		// disjoint line edits combine cleanly into one blob (so the common case
		// — two authors editing different lines of one file — needs no conflict).
		// Overlap / binary => fall through to a first-class conflict object.
		if merged, ok := mergeBlobText(base.ID, ours.ID, theirs.ID, ops); ok {
			id, werr := ops.WriteBlob(merged)
			if werr != nil {
				return nil, nil, werr
			}
			return &object.Entry{Name: name, Kind: object.KindBlob, ID: id}, nil, nil
		}
	}
	// One side unchanged from base, the other deleted -> take the deletion
	// (a non-conflicting 3-way). This is what makes rebase/drop/revert
	// re-apply a diff correctly:
	//   - a path that is IN the base and unchanged by the rebased side (ours)
	//     but absent on the new base (theirs) is dropped;
	//   - a path that a reverted side (ours) removed while the onto tip
	//     (theirs) still holds the base version is dropped too.
	if baseP {
		if oursP && !theirsP && ours.ID == base.ID {
			return nil, nil, nil
		}
		if theirsP && !oursP && theirs.ID == base.ID {
			return nil, nil, nil
		}
	}
	// Added only on one side.
	if oursP && !theirsP && !baseP {
		return &ours, nil, nil
	}
	if !oursP && theirsP && !baseP {
		return &theirs, nil, nil
	}
	// Deleted on both sides.
	if !oursP && !theirsP && baseP {
		return nil, nil, nil
	}
	// Otherwise this is a genuine conflict (N-way). Build a conflict object
	// whose removes = base (if any) and adds = the present ours/theirs terms
	// (if present). Deletion is represented by an absent term.
	//
	// A side that is ITSELF a conflict is FLATTENED: its terms are spliced in
	// rather than treating the nested conflict object as an opaque blob, so a
	// conflict-of-conflict becomes a single N-way conflict (e.g. C1=[-base,
	// +ours,+theirs] merged with theirs2 => [-base,+ours,+theirs,+theirs2]).
	c := &object.Conflict{Removes: []object.Term{}, Adds: []object.Term{}}
	if br, ba, ok := conflictTerms(base, ops); ok {
		c.Removes = append(c.Removes, br...)
		c.Adds = append(c.Adds, ba...)
	} else if baseP {
		c.Removes = append(c.Removes, object.Term{ID: bv, Label: "base"})
	}
	if or_, oa, ok := conflictTerms(ours, ops); ok {
		c.Removes = append(c.Removes, or_...)
		c.Adds = append(c.Adds, oa...)
	} else if oursP {
		c.Adds = append(c.Adds, object.Term{ID: ov, Label: "ours"})
	}
	if tr, ta, ok := conflictTerms(theirs, ops); ok {
		c.Removes = append(c.Removes, tr...)
		c.Adds = append(c.Adds, ta...)
	} else if theirsP {
		c.Adds = append(c.Adds, object.Term{ID: tv, Label: "theirs"})
	}
	// Absent side needs an explicit term so resolve can produce a deletion. If a
	// side is absent, add an absent (zero-id) positive term with that label.
	if !oursP && !theirsP {
		c.Adds = append(c.Adds, object.Term{ID: absent, Label: "theirs"})
	}
	if !oursP && theirsP {
		c.Adds = append(c.Adds, object.Term{ID: absent, Label: "ours"})
	}
	if !theirsP && oursP {
		c.Adds = append(c.Adds, object.Term{ID: absent, Label: "theirs"})
	}
	// Deduplicate: a flattened nested conflict can repeat the base (C1 already
	// carries the outer base) and, when the same content appears on two sides,
	// the adds. Keep first-seen order.
	c.Removes = dedupeTerms(c.Removes, true)
	c.Adds = dedupeTerms(c.Adds, false)
	// Recompute id after adding absent-side terms.
	conflictID, err := ops.WriteConflict(c)
	if err != nil {
		return nil, nil, err
	}
	return &object.Entry{Name: name, Kind: object.KindConflict, ID: conflictID}, []ConflictAtom{{Path: name, ID: conflictID}}, nil
}

// dfConflict builds a directory/file conflict: the terms are the whole entries
// (a tree id and a blob id, or two trees vs a blob), so a resolver can pick a
// shape. Paths are empty (the conflict lives at `name`).
func dfConflict(name string, base, ours, theirs object.Entry, ops treeOps) (*object.Entry, []ConflictAtom, error) {
	c := &object.Conflict{Removes: []object.Term{}, Adds: []object.Term{}}
	if present(base) {
		c.Removes = append(c.Removes, object.Term{ID: base.ID, Label: "base"})
	}
	if present(ours) {
		c.Adds = append(c.Adds, object.Term{ID: ours.ID, Label: "ours"})
	}
	if present(theirs) {
		c.Adds = append(c.Adds, object.Term{ID: theirs.ID, Label: "theirs"})
	}
	c.Removes = dedupeTerms(c.Removes, true)
	c.Adds = dedupeTerms(c.Adds, false)
	cid, err := ops.WriteConflict(c)
	if err != nil {
		return nil, nil, err
	}
	return &object.Entry{Name: name, Kind: object.KindConflict, ID: cid}, []ConflictAtom{{Path: name, ID: cid}}, nil
}

// dedupeTerms removes duplicate terms, keeping first-seen order. Removes are
// deduped by ID alone (the base is the same regardless of label); adds by
// (ID, label) so two genuinely different sides that happen to share content are
// both kept.
func dedupeTerms(terms []object.Term, byIDOnly bool) []object.Term {
	seen := map[string]bool{}
	out := terms[:0]
	for _, t := range terms {
		key := t.ID.String()
		if !byIDOnly {
			key += "\x00" + t.Label
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	return out
}

// mergeBlobText loads three blobs and attempts a textual 3-way merge. Any read
// failure or an overlap returns ok=false (the caller makes a conflict).
func mergeBlobText(base, ours, theirs object.ID, ops treeOps) ([]byte, bool) {
	b, err := ops.ReadBlob(base)
	if err != nil {
		return nil, false
	}
	o, err := ops.ReadBlob(ours)
	if err != nil {
		return nil, false
	}
	t, err := ops.ReadBlob(theirs)
	if err != nil {
		return nil, false
	}
	return ops.MergeText(b, o, t)
}

// conflictTerms returns the flattened terms of an entry that is a conflict
// object. ok is false for a plain blob/tree/absent entry (the caller then uses
// the entry's own id as a single term).
func conflictTerms(e object.Entry, ops treeOps) (removes, adds []object.Term, ok bool) {
	if e.Kind != object.KindConflict || !present(e) {
		return nil, nil, false
	}
	c, err := ops.ReadConflict(e.ID)
	if err != nil || c == nil {
		return nil, nil, false
	}
	return c.Removes, c.Adds, true
}

func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return fmt.Sprintf("%s/%s", parent, child)
}

func entryAsTree(e object.Entry, ops treeOps) (*object.Tree, bool) {
	if e.Kind != object.KindTree {
		return nil, false
	}
	t, err := ops.ReadTree(e.ID)
	if err != nil {
		return nil, false
	}
	return t, true
}
