package revision

import (
	"strings"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// Disjoint line edits on the two sides merge cleanly (no conflict).
func TestLineMergeDisjoint(t *testing.T) {
	base := []string{"a", "b", "c", "d"}
	ours := []string{"A", "b", "c", "d"}   // changed line 1
	theirs := []string{"a", "b", "c", "D"} // changed line 4
	merged, clean := lineMerge3(base, ours, theirs)
	if !clean {
		t.Fatal("disjoint edits must merge cleanly")
	}
	if got := strings.Join(merged, ","); got != "A,b,c,D" {
		t.Fatalf("merged = %q, want A,b,c,D", got)
	}
}

// Overlapping edits on the same line are a conflict.
func TestLineMergeOverlap(t *testing.T) {
	base := []string{"a", "b", "c"}
	ours := []string{"a", "OURS", "c"}
	theirs := []string{"a", "THEIRS", "c"}
	if _, clean := lineMerge3(base, ours, theirs); clean {
		t.Fatal("overlapping edits must conflict")
	}
}

// Insertions at DIFFERENT positions merge; at the SAME position conflict.
func TestLineMergeInsertions(t *testing.T) {
	base := []string{"a", "b"}
	ours := []string{"a", "o1", "b"}   // insert after a
	theirs := []string{"a", "b", "t1"} // insert after b
	merged, clean := lineMerge3(base, ours, theirs)
	if !clean {
		t.Fatal("insertions at different positions must merge")
	}
	if got := strings.Join(merged, ","); got != "a,o1,b,t1" {
		t.Fatalf("merged = %q", got)
	}
	// Both insert at the same spot -> conflict.
	if _, clean := lineMerge3(base, []string{"a", "o", "b"}, []string{"a", "t", "b"}); clean {
		t.Fatal("insertions at the same position must conflict")
	}
}

// One side unchanged -> the other wins, clean.
func TestLineMergeOneSideUnchanged(t *testing.T) {
	base := []string{"a", "b"}
	if m, c := lineMerge3(base, base, []string{"a", "B"}); !c || strings.Join(m, ",") != "a,B" {
		t.Fatalf("unchanged side: merged=%v clean=%v", m, c)
	}
}

// mergeFileText refuses binary input (returns not-clean).
func TestMergeFileTextBinary(t *testing.T) {
	if _, ok := mergeFileText([]byte{0, 1, 2}, []byte{0, 3}, []byte{0, 4}); ok {
		t.Fatal("binary content must not text-merge")
	}
}

// End to end: two commits editing DIFFERENT lines of one file merge with NO
// conflict (line-level merge), and the merged blob has both edits.
func TestMergeDisjointLinesNoConflict(t *testing.T) {
	repo, _ := newTestRepo(t)
	ws := NewWorkspace(repo)
	mk := func(parent object.ID, content, msg string) *store.Revision {
		_, r, err := ws.CommitFromChanges(parent, []FileChangeSpec{{Path: "f.txt", Content: []byte(content)}}, msg, store.Author{Name: "t"}, "")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	base := mk(object.ID{}, "line1\nline2\nline3\n", "base")
	ours := mk(base.Hash, "OURS1\nline2\nline3\n", "ours")
	theirs := mk(base.Hash, "line1\nline2\nTHEIRS3\n", "theirs")
	bs, _ := ws.GetSnapshot(base.Hash)
	os1, _ := ws.GetSnapshot(ours.Hash)
	ts, _ := ws.GetSnapshot(theirs.Hash)

	m, atoms, err := ws.Merge(bs.TreeID, os1.TreeID, ts.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 0 {
		t.Fatalf("disjoint line edits must NOT conflict, got atoms=%v", atoms)
	}
	mt := ws.MustTree(m)
	e := mt.Entries["f.txt"]
	if e.Kind != object.KindBlob {
		t.Fatalf("merged entry kind = %v, want blob", e.Kind)
	}
	b, _ := ws.ReadBlob(e.ID)
	if string(b) != "OURS1\nline2\nTHEIRS3\n" {
		t.Fatalf("merged content = %q", string(b))
	}
}

// Overlapping edits on the SAME line still produce a first-class conflict.
func TestMergeOverlappingLinesConflict(t *testing.T) {
	repo, _ := newTestRepo(t)
	ws := NewWorkspace(repo)
	mk := func(parent object.ID, content, msg string) *store.Revision {
		_, r, err := ws.CommitFromChanges(parent, []FileChangeSpec{{Path: "f.txt", Content: []byte(content)}}, msg, store.Author{Name: "t"}, "")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	base := mk(object.ID{}, "a\nb\nc\n", "base")
	ours := mk(base.Hash, "a\nOURS\nc\n", "ours")
	theirs := mk(base.Hash, "a\nTHEIRS\nc\n", "theirs")
	bs, _ := ws.GetSnapshot(base.Hash)
	os1, _ := ws.GetSnapshot(ours.Hash)
	ts, _ := ws.GetSnapshot(theirs.Hash)
	m, atoms, err := ws.Merge(bs.TreeID, os1.TreeID, ts.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	if len(atoms) != 1 {
		t.Fatalf("overlapping edits must conflict, got atoms=%v", atoms)
	}
	mt := ws.MustTree(m)
	if e := mt.Entries["f.txt"]; e.Kind != object.KindConflict {
		t.Fatalf("entry kind = %v, want conflict", e.Kind)
	}
}
