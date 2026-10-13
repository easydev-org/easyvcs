package merge

import (
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// These tests cover rename conflicts, which the per-path merge loop cannot see:
// the same base blob reappearing at DIFFERENT new paths on the two sides.

// rename/rename: base f=X, ours -> g=X, theirs -> h=X. Must be an explicit
// conflict at f (not two clean adds).
func TestMergeRenameRenameConflict(t *testing.T) {
	ops := &fakeTreeOps{}
	base := object.NewTree()
	base.Entries["f"] = blobEntry("f", []byte("X"))

	ours := object.NewTree()
	ours.Entries["g"] = blobEntry("g", []byte("X")) // f -> g, same content

	theirs := object.NewTree()
	theirs.Entries["h"] = blobEntry("h", []byte("X")) // f -> h, same content

	merged, atoms, err := Trees(base, ours, theirs, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 1 || atoms[0].Path != "f" {
		t.Fatalf("expected 1 conflict at f, got %+v", atoms)
	}
	if merged.Entries["f"].Kind != object.KindConflict {
		t.Fatalf("f must hold a conflict, got kind %v", merged.Entries["f"].Kind)
	}
	// The two clean adds must be gone (consumed by the conflict).
	if _, ok := merged.Entries["g"]; ok {
		t.Fatal("g should be consumed by the rename conflict")
	}
	if _, ok := merged.Entries["h"]; ok {
		t.Fatal("h should be consumed by the rename conflict")
	}
}

// rename/modify: base f=X, ours -> g=X, theirs edits f to Y. Explicit conflict
// at f whose terms carry the two paths.
func TestMergeRenameModifyConflict(t *testing.T) {
	ops := &fakeTreeOps{}
	base := object.NewTree()
	base.Entries["f"] = blobEntry("f", []byte("X"))

	ours := object.NewTree()
	ours.Entries["g"] = blobEntry("g", []byte("X"))

	theirs := object.NewTree()
	theirs.Entries["f"] = blobEntry("f", []byte("Y"))

	merged, atoms, err := Trees(base, ours, theirs, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 1 || atoms[0].Path != "f" {
		t.Fatalf("expected 1 conflict at f, got %+v", atoms)
	}
	if _, ok := merged.Entries["g"]; ok {
		t.Fatal("g should be consumed by the rename conflict")
	}
	if merged.Entries["f"].Kind != object.KindConflict {
		t.Fatalf("f must hold a conflict, got kind %v", merged.Entries["f"].Kind)
	}
}

// modify/rename (the mirror): base f=X, ours edits f to Y, theirs -> h=X.
func TestMergeModifyRenameConflict(t *testing.T) {
	ops := &fakeTreeOps{}
	base := object.NewTree()
	base.Entries["f"] = blobEntry("f", []byte("X"))

	ours := object.NewTree()
	ours.Entries["f"] = blobEntry("f", []byte("Y"))

	theirs := object.NewTree()
	theirs.Entries["h"] = blobEntry("h", []byte("X"))

	merged, atoms, err := Trees(base, ours, theirs, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 1 || atoms[0].Path != "f" {
		t.Fatalf("expected 1 conflict at f, got %+v", atoms)
	}
	if _, ok := merged.Entries["h"]; ok {
		t.Fatal("h should be consumed by the rename conflict")
	}
}

// A pure rename on ONE side, with the other side untouched, is NOT a conflict.
func TestMergePureOneSidedRenameIsNotConflict(t *testing.T) {
	ops := &fakeTreeOps{}
	base := object.NewTree()
	base.Entries["f"] = blobEntry("f", []byte("X"))

	ours := object.NewTree()
	ours.Entries["g"] = blobEntry("g", []byte("X")) // f -> g

	theirs := object.NewTree()
	theirs.Entries["f"] = blobEntry("f", []byte("X")) // unchanged

	merged, atoms, err := Trees(base, ours, theirs, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 0 {
		t.Fatalf("a one-sided rename must not conflict, got %+v", atoms)
	}
	if _, ok := merged.Entries["g"]; !ok {
		t.Fatal("g should be present (the rename won)")
	}
	if _, ok := merged.Entries["f"]; ok {
		t.Fatal("f should be gone (renamed away)")
	}
}

// A rename where BOTH sides renamed to the SAME path is not a conflict.
func TestMergeSameRenameIsNotConflict(t *testing.T) {
	ops := &fakeTreeOps{}
	base := object.NewTree()
	base.Entries["f"] = blobEntry("f", []byte("X"))

	ours := object.NewTree()
	ours.Entries["g"] = blobEntry("g", []byte("X"))
	theirs := object.NewTree()
	theirs.Entries["g"] = blobEntry("g", []byte("X"))

	merged, atoms, err := Trees(base, ours, theirs, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 0 {
		t.Fatalf("identical rename must not conflict, got %+v", atoms)
	}
	if _, ok := merged.Entries["g"]; !ok {
		t.Fatal("g should be present")
	}
}

// A plain in-place modify on both sides (no rename) must still behave as before:
// one content conflict at f, and the conflict terms carry no paths.
func TestMergePlainConflictHasNoPaths(t *testing.T) {
	ops := &fakeTreeOps{}
	base := object.NewTree()
	base.Entries["f"] = blobEntry("f", []byte("base"))
	ours := object.NewTree()
	ours.Entries["f"] = blobEntry("f", []byte("ours"))
	theirs := object.NewTree()
	theirs.Entries["f"] = blobEntry("f", []byte("theirs"))

	merged, atoms, err := Trees(base, ours, theirs, ops)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 1 || atoms[0].Path != "f" {
		t.Fatalf("expected 1 conflict at f, got %+v", atoms)
	}
	if merged.Entries["f"].Kind != object.KindConflict {
		t.Fatalf("f must hold a conflict")
	}
}
