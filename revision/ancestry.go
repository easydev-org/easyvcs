package revision

import (
	"github.com/easylab-platform/easyvcs/merge"
	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// DescendantsOf returns the revision ids (in ancestor-first order) that are
// reachable from revID by following the FIRST parent chain downwards. In the
// linear/single-parent model this is simply the chain of commits that come
// after revID. The result excludes revID itself.
func (w *Workspace) DescendantsOf(revID string) ([]string, error) {
	rev, err := w.store.GetRevision(revID)
	if err != nil {
		return nil, err
	}
	snap, err := w.store.GetSnapshot(rev.Hash)
	if err != nil {
		return nil, err
	}
	// Build parent-hash -> child revisions (first-parent edges only).
	children := map[string][]string{}
	revs, err := w.store.ListRevisions()
	if err != nil {
		return nil, err
	}
	for _, r := range revs {
		if r.Hash == rev.Hash {
			continue
		}
		rs, err := w.store.GetSnapshot(r.Hash)
		if err != nil {
			continue
		}
		if len(rs.Parents) > 0 {
			children[rs.Parents[0].String()] = append(children[rs.Parents[0].String()], r.ID)
		}
	}
	// BFS from rev's snapshot hash, collecting in topological (ancestor-first)
	// order so the descendants can be re-parented sequentially.
	var out []string
	seen := map[string]bool{}
	frontier := []string{snap.RevisionHash.String()}
	for len(frontier) > 0 {
		var next []string
		for _, h := range frontier {
			for _, cid := range children[h] {
				if seen[cid] {
					continue
				}
				seen[cid] = true
				out = append(out, cid)
				cs, err := w.store.GetSnapshot(revisionHashOf(cid, w))
				if err != nil {
					continue
				}
				next = append(next, cs.RevisionHash.String())
			}
		}
		frontier = next
	}
	return out, nil
}

// descendantsFromSnapshot returns all revisions reachable by following parent
// edges forward from the given snapshot hash. The revision that owns the seed
// snapshot is excluded.
func (w *Workspace) descendantsFromSnapshot(seed object.ID) ([]*store.Revision, error) {
	all, err := w.store.ListRevisions()
	if err != nil {
		return nil, err
	}
	snapOwner := map[string]string{}
	for _, rv := range all {
		snapOwner[rv.Hash.String()] = rv.ID
	}
	owner := snapOwner[seed.String()]
	visited := map[string]bool{}
	if owner != "" {
		visited[owner] = true
	}
	queue := []object.ID{seed}
	seenSnaps := map[object.ID]bool{seed: true}
	var out []*store.Revision
	for len(queue) > 0 {
		snapID := queue[0]
		queue = queue[1:]
		for _, cand := range all {
			candSnap, err := w.store.GetSnapshot(cand.Hash)
			if err != nil {
				continue
			}
			for _, p := range candSnap.Parents {
				if p == snapID {
					if !visited[cand.ID] {
						visited[cand.ID] = true
						out = append(out, cand)
					}
					if !seenSnaps[candSnap.RevisionHash] {
						seenSnaps[candSnap.RevisionHash] = true
						queue = append(queue, candSnap.RevisionHash)
					}
					break
				}
			}
		}
	}
	return out, nil
}

// ConflictsInTree returns the conflict atoms embedded in a tree.
func (w *Workspace) ConflictsInTree(treeID object.ID) ([]merge.ConflictAtom, error) {
	var out []merge.ConflictAtom
	if err := w.collectConflicts("", w.mustTree(treeID), &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (w *Workspace) collectConflicts(prefix string, t *object.Tree, out *[]merge.ConflictAtom) error {
	for _, e := range t.SortedEntries() {
		path := joinPath(prefix, e.Name)
		if e.Kind == object.KindConflict {
			*out = append(*out, merge.ConflictAtom{Path: path, ID: e.ID})
			continue
		}
		if e.Kind == object.KindTree {
			sub, err := w.ReadTree(e.ID)
			if err != nil {
				continue
			}
			if err := w.collectConflicts(path, sub, out); err != nil {
				return err
			}
		}
	}
	return nil
}
