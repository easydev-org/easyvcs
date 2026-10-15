package revision

import (
	"strings"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

func ckCommit(t *testing.T, ws *Workspace, parent object.ID, ch []FileChangeSpec, msg string) *store.Revision {
	t.Helper()
	_, r, err := ws.CommitFromChanges(parent, ch, msg, store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A conflict-of-conflict FLATTENS to a single N-way conflict (not an opaque
// nested blob): C1=[-base,+ours,+theirs] merged with theirs2 becomes
// [-base,+ours,+theirs,+theirs2].
func TestNestedConflictFlattens(t *testing.T) {
	repo, _ := newTestRepo(t)
	ws := NewWorkspace(repo)
	base := ckCommit(t, ws, object.ID{}, []FileChangeSpec{{Path: "f.txt", Content: []byte("base\n")}}, "base")
	ours := ckCommit(t, ws, base.Hash, []FileChangeSpec{{Path: "f.txt", Content: []byte("ours\n")}}, "ours")
	theirs := ckCommit(t, ws, base.Hash, []FileChangeSpec{{Path: "f.txt", Content: []byte("theirs\n")}}, "theirs")
	bs, _ := ws.GetSnapshot(base.Hash)
	os1, _ := ws.GetSnapshot(ours.Hash)
	ts, _ := ws.GetSnapshot(theirs.Hash)
	m1, _, err := ws.Merge(bs.TreeID, os1.TreeID, ts.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	_, c1, _ := ws.Commit(CommitParams{Parents: []object.ID{base.Hash}, TreeID: m1, Description: "c1"})
	c1s, _ := ws.GetSnapshot(c1.Hash)
	th2 := ckCommit(t, ws, base.Hash, []FileChangeSpec{{Path: "f.txt", Content: []byte("theirs2\n")}}, "theirs2")
	th2s, _ := ws.GetSnapshot(th2.Hash)

	m2, atoms, err := ws.Merge(bs.TreeID, c1s.TreeID, th2s.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 1 {
		t.Fatalf("atoms = %d, want 1", len(atoms))
	}
	mt := ws.MustTree(m2)
	e := mt.Entries["f.txt"]
	o, _ := repo.ReadObject(e.ID)
	if e.Kind != object.KindConflict || o.Conflict == nil {
		t.Fatalf("expected a conflict, got kind=%v", e.Kind)
	}
	if o.Conflict.Numeric() != 4 {
		t.Fatalf("nested conflict not flattened: numeric=%d, want 4", o.Conflict.Numeric())
	}
	// The render must succeed (a nested term is rendered recursively).
	txt, err := ws.RenderConflict(e.ID)
	if err != nil {
		t.Fatalf("render nested conflict: %v", err)
	}
	for _, want := range []string{"ours", "theirs", "theirs2", "base"} {
		if !strings.Contains(txt, want) {
			t.Fatalf("render missing %q:\n%s", want, txt)
		}
	}
}

// A directory/file conflict is DETECTED (not silently dropped): one side makes
// `x` a directory, the other edits `x` as a file.
func TestDirFileConflictDetected(t *testing.T) {
	repo, _ := newTestRepo(t)
	ws := NewWorkspace(repo)
	base := ckCommit(t, ws, object.ID{}, []FileChangeSpec{{Path: "x", Content: []byte("file\n")}}, "base")
	ours := ckCommit(t, ws, base.Hash, []FileChangeSpec{{Path: "x", Delete: true}, {Path: "x/y", Content: []byte("nested\n")}}, "ours-dir")
	theirs := ckCommit(t, ws, base.Hash, []FileChangeSpec{{Path: "x", Content: []byte("MODIFIED\n")}}, "theirs-mod")
	bs, _ := ws.GetSnapshot(base.Hash)
	os1, _ := ws.GetSnapshot(ours.Hash)
	ts, _ := ws.GetSnapshot(theirs.Hash)

	m, atoms, err := ws.Merge(bs.TreeID, os1.TreeID, ts.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 1 || atoms[0].Path != "x" {
		t.Fatalf("dir/file conflict not detected: atoms=%v", atoms)
	}
	mt := ws.MustTree(m)
	if e := mt.Entries["x"]; e.Kind != object.KindConflict {
		t.Fatalf("x kind = %v, want conflict", e.Kind)
	}
	// Render shows BOTH the directory and the file content (nothing lost).
	txt, err := ws.RenderConflict(mt.Entries["x"].ID)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(txt, "MODIFIED") || !strings.Contains(txt, "directory") {
		t.Fatalf("dir/file render lost a side:\n%s", txt)
	}
	_ = repo
}

// A binary conflict renders a placeholder, not raw NUL bytes.
func TestBinaryConflictRendersPlaceholder(t *testing.T) {
	repo, _ := newTestRepo(t)
	ws := NewWorkspace(repo)
	bin := func(seed byte) []byte { return []byte{0, 1, 2, seed, 0xff, 0, 9, 0} }
	base := ckCommit(t, ws, object.ID{}, []FileChangeSpec{{Path: "b.bin", Content: bin(0)}}, "base")
	ours := ckCommit(t, ws, base.Hash, []FileChangeSpec{{Path: "b.bin", Content: bin(1)}}, "ours")
	theirs := ckCommit(t, ws, base.Hash, []FileChangeSpec{{Path: "b.bin", Content: bin(2)}}, "theirs")
	bs, _ := ws.GetSnapshot(base.Hash)
	os1, _ := ws.GetSnapshot(ours.Hash)
	ts, _ := ws.GetSnapshot(theirs.Hash)
	m, _, err := ws.Merge(bs.TreeID, os1.TreeID, ts.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	mt := ws.MustTree(m)
	txt, err := ws.RenderConflict(mt.Entries["b.bin"].ID)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(txt, 0) {
		t.Fatal("binary conflict render contains NUL bytes")
	}
	if !strings.Contains(txt, "binary blob") {
		t.Fatalf("binary conflict should use a placeholder:\n%q", txt)
	}
	_ = repo
}

// ResolveToContent writes custom merged bytes (the binary escape hatch) and
// clears the conflict.
func TestResolveToContent(t *testing.T) {
	repo, _ := newTestRepo(t)
	ws := NewWorkspace(repo)
	bin := func(s byte) []byte { return []byte{0, 1, 2, s, 0xff} }
	base := ckCommit(t, ws, object.ID{}, []FileChangeSpec{{Path: "b.bin", Content: bin(1)}}, "base")
	ours := ckCommit(t, ws, base.Hash, []FileChangeSpec{{Path: "b.bin", Content: bin(2)}}, "ours")
	theirs := ckCommit(t, ws, base.Hash, []FileChangeSpec{{Path: "b.bin", Content: bin(3)}}, "theirs")
	bs, _ := ws.GetSnapshot(base.Hash)
	os1, _ := ws.GetSnapshot(ours.Hash)
	ts, _ := ws.GetSnapshot(theirs.Hash)
	m, _, err := ws.Merge(bs.TreeID, os1.TreeID, ts.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	_, cr, _ := ws.Commit(CommitParams{Parents: []object.ID{base.Hash}, TreeID: m, Description: "conflict"})

	want := []byte{9, 9, 9, 9, 9}
	ns, _, err := ws.ResolveToContent(cr.ID, "b.bin", want)
	if err != nil {
		t.Fatalf("ResolveToContent: %v", err)
	}
	snap, _ := ws.GetSnapshot(ns.RevisionHash)
	root := ws.MustTree(snap.TreeID)
	e := root.Entries["b.bin"]
	if e.Kind != object.KindBlob {
		t.Fatalf("after content-resolve kind = %v, want blob", e.Kind)
	}
	got, _ := ws.ReadBlob(e.ID)
	if string(got) != string(want) {
		t.Fatalf("resolved bytes = %v, want %v", got, want)
	}
	if rem, _ := ws.ConflictsInTree(snap.TreeID); len(rem) != 0 {
		t.Fatalf("conflicts remain: %v", rem)
	}
	_ = repo
}
