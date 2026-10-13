package revision

import (
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// commitTreeIn writes a tree built from explicit entries and commits it.
func commitTreeIn(t *testing.T, ws *Workspace, tree *object.Tree, desc string, parents []object.ID) (*store.Snapshot, *store.Revision) {
	t.Helper()
	tid := mustWriteTree(ws, tree)
	snap, rev, err := ws.Commit(CommitParams{
		Parents: parents, TreeID: tid, Description: desc,
		Author: store.Author{Name: "t", Email: "t@x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap, rev
}

func blobTree(ws *Workspace, path string, data []byte) *object.Tree {
	tr := object.NewTree()
	tr.Entries[path] = object.Entry{Name: path, Kind: object.KindBlob, ID: mustWriteBlob(ws, data)}
	return tr
}

func mustTreeOf(t *testing.T, ws *Workspace, rev *store.Revision) object.ID {
	t.Helper()
	snap, err := ws.GetSnapshot(rev.Hash)
	if err != nil {
		t.Fatal(err)
	}
	return snap.TreeID
}

func revByID(t *testing.T, ws *Workspace, id string) *store.Revision {
	t.Helper()
	r, err := ws.GetRevision(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestDiffContentBinaryMarked verifies a binary change is flagged and never
// line-diffed.
func TestDiffContentBinaryMarked(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, _ string) {
		bin1 := []byte{0x00, 0x01, 0x02, 0x00, 0xff}
		bin2 := []byte{0x00, 0x09, 0x02, 0x00, 0xfe}
		baseSnap, _ := commitTreeIn(t, ws, blobTree(ws, "img.bin", bin1), "base", nil)
		_, modRev := commitTreeIn(t, ws, blobTree(ws, "img.bin", bin2), "mod", []object.ID{baseSnap.RevisionHash})

		baseTree, _ := ws.DiffBaseForRevision(modRev.ID)
		diffs, err := ws.DiffContent(baseTree, mustTreeOf(t, ws, modRev))
		if err != nil {
			t.Fatal(err)
		}
		if len(diffs) != 1 {
			t.Fatalf("expected 1 file diff, got %d", len(diffs))
		}
		d := diffs[0]
		if !d.Binary {
			t.Fatal("binary file must be flagged Binary")
		}
		if d.AddedLines != 0 || d.RemovedLines != 0 {
			t.Fatalf("binary must not report line counts: +%d -%d", d.AddedLines, d.RemovedLines)
		}
		if d.Content == "" {
			t.Fatal("binary diff should carry a short note")
		}
	})
}

// TestDiffWithRenamesDetectsRename verifies exact-id rename detection.
func TestDiffWithRenamesDetectsRename(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, _ string) {
		content := []byte("same content\n")
		baseTree := blobTree(ws, "old.txt", content)
		baseSnap, _ := commitTreeIn(t, ws, baseTree, "base", nil)

		// New tree: old.txt removed, new.txt added with identical content.
		nt := object.NewTree()
		nt.Entries["new.txt"] = object.Entry{Name: "new.txt", Kind: object.KindBlob, ID: mustWriteBlob(ws, content)}
		_, newRev := commitTreeIn(t, ws, nt, "rename", []object.ID{baseSnap.RevisionHash})

		base, _ := ws.DiffBaseForRevision(newRev.ID)
		// Plain Diff sees removed+added.
		plain, err := ws.Diff(base, mustTreeOf(t, ws, newRev))
		if err != nil {
			t.Fatal(err)
		}
		if len(plain) != 2 {
			t.Fatalf("plain diff should be removed+added, got %+v", plain)
		}
		// DiffWithRenames folds them into one renamed change.
		ren, err := ws.DiffWithRenames(base, mustTreeOf(t, ws, newRev))
		if err != nil {
			t.Fatal(err)
		}
		if len(ren) != 1 || ren[0].Status != StatusRenamed {
			t.Fatalf("expected 1 rename, got %+v", ren)
		}
		if ren[0].OldPath != "old.txt" || ren[0].Path != "new.txt" {
			t.Fatalf("rename paths wrong: %+v", ren[0])
		}
	})
}

// TestResolveToContentBinary verifies a binary conflict can be resolved by
// supplying arbitrary merged bytes.
func TestResolveToContentBinary(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, _ string) {
		bin1 := []byte{0x00, 0x01, 0x02}
		bin2 := []byte{0x00, 0x09, 0x08}
		// base + two divergent children on the same path -> 3-way merge conflict.
		baseSnap, _ := commitTreeIn(t, ws, blobTree(ws, "f.bin", []byte{0x00, 0x00}), "base", nil)
		oursSnap, oursRev := commitTreeIn(t, ws, blobTree(ws, "f.bin", bin1), "ours", []object.ID{baseSnap.RevisionHash})
		_, theirsRev := commitTreeIn(t, ws, blobTree(ws, "f.bin", bin2), "theirs", []object.ID{baseSnap.RevisionHash})

		// Merge ours' tree with theirs' tree against base -> conflict object.
		mergedTree, atoms, err := ws.Merge(
			mustTreeOf(t, ws, revByID(t, ws, baseSnap.RevisionID)),
			mustTreeOf(t, ws, oursRev),
			mustTreeOf(t, ws, theirsRev),
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(atoms) != 1 {
			t.Fatalf("expected a binary conflict, got %d", len(atoms))
		}
		// Commit the conflicted tree as a new revision, then resolve it.
		_, confRev := commitTreeIn(t, ws, ws.mustTree(mergedTree), "conflicted", []object.ID{oursSnap.RevisionHash})
		resolved := []byte{0x00, 0xAA, 0xBB}
		if _, _, err := ws.ResolveToContent(confRev.ID, "f.bin", resolved); err != nil {
			t.Fatalf("ResolveToContent: %v", err)
		}
		// The resolved tree must hold the new blob.
		got, err := ws.GetRevision(confRev.ID)
		if err != nil {
			t.Fatal(err)
		}
		snap, _ := ws.GetSnapshot(got.Hash)
		entry, err := ws.findEntry(ws.mustTree(snap.TreeID), "f.bin")
		if err != nil {
			t.Fatal(err)
		}
		if entry.Kind != object.KindBlob || entry.ID != object.BlobID(resolved) {
			t.Fatalf("resolved entry wrong: %+v", entry)
		}
	})
}

// TestMergeRenameConflictAndResolveToPath builds a rename/rename conflict via
// the real merge path and resolves it by choosing one side's path.
func TestMergeRenameConflictAndResolveToPath(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, _ string) {
		content := []byte("X\n")
		baseSnap, _ := commitTreeIn(t, ws, blobTree(ws, "f", content), "base", nil)

		oursTree := object.NewTree()
		oursTree.Entries["g"] = object.Entry{Name: "g", Kind: object.KindBlob, ID: mustWriteBlob(ws, content)}
		oursSnap, oursRev := commitTreeIn(t, ws, oursTree, "ours->g", []object.ID{baseSnap.RevisionHash})

		theirsTree := object.NewTree()
		theirsTree.Entries["h"] = object.Entry{Name: "h", Kind: object.KindBlob, ID: mustWriteBlob(ws, content)}
		_, theirsRev := commitTreeIn(t, ws, theirsTree, "theirs->h", []object.ID{baseSnap.RevisionHash})

		baseRev := revByID(t, ws, baseSnap.RevisionID)
		mergedTree, atoms, err := ws.Merge(mustTreeOf(t, ws, baseRev), mustTreeOf(t, ws, oursRev), mustTreeOf(t, ws, theirsRev))
		if err != nil {
			t.Fatal(err)
		}
		if len(atoms) != 1 || atoms[0].Path != "f" {
			t.Fatalf("expected a rename conflict at f, got %+v", atoms)
		}
		_, confRev := commitTreeIn(t, ws, ws.mustTree(mergedTree), "conflicted", []object.ID{oursSnap.RevisionHash})

		if _, _, err := ws.ResolveToPath(confRev.ID, "f", "g"); err != nil {
			t.Fatalf("ResolveToPath: %v", err)
		}
		got, _ := ws.GetRevision(confRev.ID)
		snap, _ := ws.GetSnapshot(got.Hash)
		tree := ws.mustTree(snap.TreeID)
		if _, ok := tree.Entries["g"]; !ok {
			t.Fatal("g should hold the resolved content")
		}
		if _, ok := tree.Entries["f"]; ok {
			t.Fatal("f (the conflict path) should be gone after resolving to g")
		}
		if _, ok := tree.Entries["h"]; ok {
			t.Fatal("h (the losing side) should be gone")
		}
	})
}
