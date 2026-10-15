package revision

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
)

// Materialize writes a tree's contents into a directory, rendering conflicts
// as marker files.
func (w *Workspace) Materialize(treeID object.ID, dest string) error {
	return w.materializeTree(w.mustTree(treeID), dest)
}

func (w *Workspace) materializeTree(t *object.Tree, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, e := range t.SortedEntries() {
		full := filepath.Join(dir, e.Name)
		if sub, isTree := w.readTreeEntry(e); isTree {
			if err := w.materializeTree(sub, full); err != nil {
				return err
			}
			continue
		}
		data, err := w.store.ReadObject(e.ID)
		if err != nil {
			return err
		}
		if data.Kind == object.KindConflict {
			text, err := w.renderConflict(e.ID)
			if err != nil {
				return err
			}
			if err := os.WriteFile(full, []byte(text), 0o644); err != nil {
				return err
			}
			continue
		}
		if data.Kind != object.KindBlob {
			if err := os.MkdirAll(full, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.WriteFile(full, data.Blob, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RenderConflict returns the conflict-marker text for a first-class conflict
// object (the same rendering Materialize and /archive use). It lets a read path
// (e.g. the server's /raw and /blob) surface a conflicted file as marker text
// instead of refusing it.
func (w *Workspace) RenderConflict(conflictID object.ID) (string, error) {
	return w.renderConflict(conflictID)
}

// renderConflict produces conflict-marker text for a conflict object.
func (w *Workspace) renderConflict(conflictID object.ID) (string, error) {
	o, err := w.store.ReadObject(conflictID)
	if err != nil {
		return "", err
	}
	if o.Kind != object.KindConflict || o.Conflict == nil {
		return "", fmt.Errorf("object %s is not a conflict", conflictID)
	}
	c := o.Conflict
	var b strings.Builder
	fmt.Fprintf(&b, "<<<<<<< conflict (%d terms)\n", c.Numeric())
	if len(c.Adds) > 0 {
		first, err := w.blobText(c.Adds[0].ID)
		if err != nil {
			return "", err
		}
		b.WriteString(first)
		b.WriteString("\n")
	}
	for i := 1; i < len(c.Adds); i++ {
		label := c.Adds[i].Label
		b.WriteString("%%%%%%% side #" + fmt.Sprint(i) + " (" + label + ")\n")
		text, err := w.blobText(c.Adds[i].ID)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	for _, term := range c.Removes {
		b.WriteString("%%%%%%% diff from: " + term.Label + "\n")
		text, err := w.blobText(term.ID)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
		b.WriteString("\n")
	}
	b.WriteString(">>>>>>> conflict ends\n")
	return b.String(), nil
}

// blobText returns text content of a blob, or "" for the absent id.
func (w *Workspace) blobText(id object.ID) (string, error) {
	if id == (object.ID{}) {
		return "", nil
	}
	o, err := w.store.ReadObject(id)
	if err != nil {
		return "", err
	}
	switch o.Kind {
	case object.KindBlob:
		// A binary blob cannot be shown as text; emit a short placeholder rather
		// than raw NUL bytes (which would corrupt the marker stream).
		if IsBinary(o.Blob) {
			return fmt.Sprintf("<<binary blob %s, %d bytes>>", id.String()[:12], len(o.Blob)), nil
		}
		return string(o.Blob), nil
	case object.KindConflict:
		// A NESTED conflict (a term that is itself a conflict): render it
		// recursively so the marker text stays complete.
		return w.renderConflict(id)
	case object.KindTree:
		// A directory/file conflict term: show the directory's file listing.
		t, err := w.ReadTree(id)
		if err != nil {
			return "", err
		}
		names := make([]string, 0, len(t.Entries))
		for _, e := range t.SortedEntries() {
			names = append(names, e.Name)
		}
		return fmt.Sprintf("<<directory with %d entries: %s>>", len(names), strings.Join(names, ", ")), nil
	default:
		return "", fmt.Errorf("object %s is not a blob or conflict", id)
	}
}
