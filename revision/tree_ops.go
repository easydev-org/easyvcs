package revision

import (
	"fmt"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// joinPath joins path components with forward slashes.
func joinPath(parent, child string) string {
	if parent == "" {
		return child
	}
	return parent + "/" + child
}

// writeTree stores a tree object and returns its id. A persistence failure
// (e.g. an oversized object) is returned as an error so the caller can decide
// to fail the commit rather than silently reference a non-persisted id.
func (w *Workspace) writeTree(t *object.Tree) (object.ID, error) {
	o := &object.Object{Kind: object.KindTree, Tree: t}
	if err := w.store.WriteObject(o); err != nil {
		return object.ID{}, err
	}
	return o.ID(), nil
}

// WriteTree stores a tree object and returns its id (exported).
func (w *Workspace) WriteTree(t *object.Tree) (object.ID, error) { return w.writeTree(t) }

// MustTree reads a tree by id, returning an empty tree on error (exported).
func (w *Workspace) MustTree(id object.ID) *object.Tree { return w.mustTree(id) }

// FindEntry walks a tree down to the entry at path (exported).
func (w *Workspace) FindEntry(t *object.Tree, path string) (object.Entry, error) {
	return w.findEntry(t, path)
}

// mustTree reads a tree or falls back to an empty tree.
func (w *Workspace) mustTree(id object.ID) *object.Tree {
	t, err := w.ReadTree(id)
	if err != nil {
		return object.NewTree()
	}
	return t
}

// ReadBlob reads a blob's contents by id.
func (w *Workspace) ReadBlob(id object.ID) ([]byte, error) {
	o, err := w.store.ReadObject(id)
	if err != nil {
		return nil, err
	}
	if o.Kind != object.KindBlob {
		return nil, fmt.Errorf("object %s is not a blob", id)
	}
	return o.Blob, nil
}

// WriteBlob writes a blob object and returns its id. A persistence failure is
// returned as an error rather than silently returning an unreferenced id.
func (w *Workspace) WriteBlob(data []byte) (object.ID, error) {
	o := &object.Object{Kind: object.KindBlob, Blob: data}
	if err := w.store.WriteObject(o); err != nil {
		return object.ID{}, err
	}
	return o.ID(), nil
}

// treeOps adapter satisfies merge.treeOps.

// WriteConflict implements merge.treeOps.
func (w *Workspace) WriteConflict(c *object.Conflict) (object.ID, error) {
	o := &object.Object{Kind: object.KindConflict, Conflict: c}
	if err := w.store.WriteObject(o); err != nil {
		return object.ID{}, err
	}
	return o.ID(), nil
}

// SetRef creates or updates a branch/tag pointing to a revision.
//
// A tag is immutable: creating a tag with a name that already exists returns an
// error (delete it first via DeleteRef). Branches remain re-pointable.
func (w *Workspace) SetRef(name string, kind store.RefKind, revisionID string) (*store.Ref, error) {
	if kind == store.RefTag {
		if existing, err := w.store.GetRef(name); err == nil && existing != nil {
			return nil, fmt.Errorf("tag %q already exists; delete it before re-tagging", name)
		}
	}
	r := &store.Ref{Name: name, Kind: kind, Target: revisionID}
	if err := w.store.PutRef(r); err != nil {
		return nil, err
	}
	return r, nil
}

// DeleteRef removes a ref.
func (w *Workspace) DeleteRef(name string) error { return w.store.DeleteRef(name) }

// GetRef returns a ref by name.
func (w *Workspace) GetRef(name string) (*store.Ref, error) { return w.store.GetRef(name) }

// ListRefs returns all refs.
func (w *Workspace) ListRefs() ([]*store.Ref, error) { return w.store.ListRefs() }

func (w *Workspace) readTreeEntry(e object.Entry) (*object.Tree, bool) {
	if e.Kind != object.KindTree {
		return nil, false
	}
	t, err := w.ReadTree(e.ID)
	if err != nil {
		return nil, false
	}
	return t, true
}

func (w *Workspace) readTreeEntryOrNil(e object.Entry, present bool) (*object.Tree, bool) {
	if !present {
		return nil, false
	}
	return w.readTreeEntry(e)
}

// findEntry walks a tree down to the entry at path.
func (w *Workspace) findEntry(t *object.Tree, path string) (object.Entry, error) {
	var components []string
	cur := path
	for {
		idx := strings.IndexByte(cur, '/')
		if idx < 0 {
			components = append(components, cur)
			break
		}
		components = append(components, cur[:idx])
		cur = cur[idx+1:]
	}
	entry, ok := t.Entries[components[0]]
	if !ok {
		return object.Entry{}, fmt.Errorf("no entry at path %s", path)
	}
	for i := 1; i < len(components); i++ {
		if entry.Kind != object.KindTree {
			return object.Entry{}, fmt.Errorf("path %s traverses a non-tree", path)
		}
		sub, err := w.ReadTree(entry.ID)
		if err != nil {
			return object.Entry{}, err
		}
		entry, ok = sub.Entries[components[i]]
		if !ok {
			return object.Entry{}, fmt.Errorf("no entry at path %s", path)
		}
	}
	return entry, nil
}

// resolvePath replaces the entry at path in a tree with a blob (or removes it).
func (w *Workspace) resolvePath(t *object.Tree, path string, chosen object.ID, remove bool) (object.ID, error) {
	var components []string
	cur := path
	for {
		idx := strings.IndexByte(cur, '/')
		if idx < 0 {
			components = append(components, cur)
			break
		}
		components = append(components, cur[:idx])
		cur = cur[idx+1:]
	}
	var rewrite func(t *object.Tree, idx int) (*object.Tree, error)
	rewrite = func(t *object.Tree, idx int) (*object.Tree, error) {
		if idx == len(components) {
			return t, nil
		}
		name := components[idx]
		child, ok := t.Entries[name]
		clone := object.NewTree()
		for _, e := range t.SortedEntries() {
			clone.Entries[e.Name] = e
		}
		if idx == len(components)-1 {
			if remove {
				delete(clone.Entries, name)
			} else {
				clone.Entries[name] = object.Entry{Name: name, Kind: object.KindBlob, ID: chosen}
			}
			return clone, nil
		}
		// Intermediate dir: recurse into existing child subtree, or create a new
		// empty subtree if the path doesn't exist yet (for adding nested files).
		// The subtree read is served by the shared pool (no open transaction at
		// this point), so it is safe under a single-connection sqlite pool.
		var childTree *object.Tree
		if ok && child.Kind == object.KindTree {
			childTree = w.mustTree(child.ID)
		} else {
			childTree = object.NewTree()
		}
		newChild, err := rewrite(childTree, idx+1)
		if err != nil {
			return nil, err
		}
		if len(newChild.Entries) == 0 && remove {
			delete(clone.Entries, name)
		} else {
			newChildID, err := w.writeTree(newChild)
			if err != nil {
				return nil, err
			}
			clone.Entries[name] = object.Entry{Name: name, Kind: object.KindTree, ID: newChildID}
		}
		return clone, nil
	}
	newTree, err := rewrite(t, 0)
	if err != nil {
		return object.ID{}, err
	}
	return w.writeTree(newTree)
}
