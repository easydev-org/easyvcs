package rr

import (
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// A scoped RR (docs/subrepo-scopes.md §7.3) limits its diff and merge to one
// subtree: a change under `path` merges without touching the rest of the repo.

// commitNested commits a snapshot whose tree contains the given full paths
// ("frontend/app.ts"), on top of parents (which may be nil). It uses
// CommitFromChanges so nested paths are materialized correctly.
func commitNested(t *testing.T, ws *revision.Workspace, parents []object.ID, files map[string]string, msg string) *store.Snapshot {
	t.Helper()
	var parentHash object.ID
	if len(parents) > 0 {
		parentHash = parents[0]
	}
	changes := make([]revision.FileChangeSpec, 0, len(files))
	for p, c := range files {
		changes = append(changes, revision.FileChangeSpec{Path: p, Content: []byte(c)})
	}
	snap, _, err := ws.CommitFromChanges(parentHash, changes, msg, store.Author{Name: "t", Email: "t@x"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestScopedRRDiffIsLimitedToPath(t *testing.T) {
	cs, repo := newStore(t)
	_ = cs
	ws := revision.NewWorkspace(repo)
	svc := New(cs, repo)

	base := commitNested(t, ws, nil, map[string]string{
		"frontend/app.ts": "v1\n",
		"backend/srv.go":  "s1\n",
	}, "base")
	_, _ = ws.SetRef("main", store.RefBranch, base.RevisionID)

	// A source that changes frontend AND backend.
	src := commitNested(t, ws, []object.ID{base.RevisionHash}, map[string]string{
		"frontend/app.ts": "v2\n",
		"backend/srv.go":  "s2\n",
	}, "both")

	// A scoped RR to frontend: the diff shows ONLY frontend/app.ts.
	req, err := svc.CreateScoped(Source{Snapshot: src.RevisionHash}, "main", "frontend", "fe change", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.Path != "frontend" {
		t.Fatalf("RR path = %q", req.Path)
	}
	diff, err := svc.Diff(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) != 1 {
		t.Fatalf("scoped diff = %d files, want 1: %+v", len(diff), diff)
	}
	if diff[0].Path != "app.ts" {
		t.Fatalf("diff path = %q, want app.ts (relative to the scope)", diff[0].Path)
	}

	// An UNSCOPED RR sees BOTH files.
	reqAll, _ := svc.Create(Source{Snapshot: src.RevisionHash}, "main", "all", "", nil)
	diffAll, _ := svc.Diff(reqAll)
	if len(diffAll) != 2 {
		t.Fatalf("unscoped diff = %d files, want 2", len(diffAll))
	}
}

func TestScopedRRMergeOnlyTouchesSubtree(t *testing.T) {
	cs, repo := newStore(t)
	_ = cs
	ws := revision.NewWorkspace(repo)
	svc := New(cs, repo)

	base := commitNested(t, ws, nil, map[string]string{
		"frontend/app.ts": "v1\n",
		"backend/srv.go":  "s1\n",
	}, "base")
	_, _ = ws.SetRef("main", store.RefBranch, base.RevisionID)

	// Source changes only frontend (a proper scoped change).
	src := commitNested(t, ws, []object.ID{base.RevisionHash}, map[string]string{
		"frontend/app.ts": "v2\n",
		"backend/srv.go":  "s1\n",
	}, "frontend only")
	req, _ := svc.CreateScoped(Source{Snapshot: src.RevisionHash}, "main", "frontend", "fe", "", nil)

	res, err := svc.ApplyMerge(req, testAuthor(), testUserID())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Clean {
		t.Fatalf("scoped merge should be clean: %+v", res.Conflicts)
	}
	// main now has frontend/app.ts = v2, backend untouched.
	ref, _ := ws.GetRef("main")
	rev, _ := repo.GetRevision(ref.Target)
	snap, _ := repo.GetSnapshot(rev.Hash)
	root := ws.MustTree(snap.TreeID)
	feE, _ := ws.FindEntry(root, "frontend/app.ts")
	if got := mustBlob(t, ws, feE.ID); got != "v2\n" {
		t.Fatalf("frontend/app.ts = %q", got)
	}
	beE, _ := ws.FindEntry(root, "backend/srv.go")
	if got := mustBlob(t, ws, beE.ID); got != "s1\n" {
		t.Fatalf("backend/srv.go changed: %q", got)
	}
}

func mustBlob(t *testing.T, ws *revision.Workspace, id object.ID) string {
	t.Helper()
	b, err := ws.ReadBlob(id)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestSequentialMergeNoRebase pins that two RRs opened against the SAME base can
// be merged back-to-back WITHOUT rebasing the second: each merge is a 3-way
// merge of (pinned base, source, CURRENT target tip), so the first merge moving
// the target does not invalidate the second (when they touch different files).
// This is the easyvcs advantage over a git MR, whose diff is pinned to a fixed
// base and drifts once the target advances.
func TestSequentialMergeNoRebase(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)
	svc := New(cs, repo)

	base := commitNested(t, ws, nil, map[string]string{"a.txt": "a0\n", "b.txt": "b0\n"}, "base")
	_, _ = ws.SetRef("main", store.RefBranch, base.RevisionID)

	// Two sources off the SAME base, touching DIFFERENT files.
	s1 := commitNested(t, ws, []object.ID{base.RevisionHash}, map[string]string{"a.txt": "a1\n", "b.txt": "b0\n"}, "s1")
	s2 := commitNested(t, ws, []object.ID{base.RevisionHash}, map[string]string{"a.txt": "a0\n", "b.txt": "b1\n"}, "s2")

	rr1, _ := svc.Create(Source{Snapshot: s1.RevisionHash}, "main", "mr1", "", nil)
	rr2, _ := svc.Create(Source{Snapshot: s2.RevisionHash}, "main", "mr2", "", nil)

	if r, err := svc.ApplyMerge(rr1, testAuthor(), testUserID()); err != nil || !r.Clean {
		t.Fatalf("merge1: %v clean=%v", err, r.Clean)
	}
	// The SECOND merge needs NO rebase.
	if r, err := svc.ApplyMerge(rr2, testAuthor(), testUserID()); err != nil || !r.Clean {
		t.Fatalf("merge2 (no rebase): %v clean=%v", err, r.Clean)
	}
	// Both changes are present on main.
	ref, _ := ws.GetRef("main")
	rev, _ := repo.GetRevision(ref.Target)
	snap, _ := repo.GetSnapshot(rev.Hash)
	root := ws.MustTree(snap.TreeID)
	ae, _ := ws.FindEntry(root, "a.txt")
	ab, _ := ws.ReadBlob(ae.ID)
	be, _ := ws.FindEntry(root, "b.txt")
	bb, _ := ws.ReadBlob(be.ID)
	if string(ab) != "a1\n" || string(bb) != "b1\n" {
		t.Fatalf("both changes must land: a=%q b=%q", string(ab), string(bb))
	}
}

// TestMergeAfterTargetMovesHeavily pins the "base is pinned, target is dynamic"
// contract: an RR opened against an OLD target tip still merges correctly after
// the target has moved a lot — additions/deletions on the target are preserved,
// non-overlapping edits to the same file combine, and ONLY a genuine overlap
// conflicts.
func TestMergeAfterTargetMovesHeavily(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)
	svc := New(cs, repo)
	mk := func(parent object.ID, files map[string]string, msg string) *store.Revision {
		var ch []revision.FileChangeSpec
		for p, c := range files {
			ch = append(ch, revision.FileChangeSpec{Path: p, Content: []byte(c)})
		}
		_, r, err := ws.CommitFromChanges(parent, ch, msg, store.Author{Name: "t"}, "")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	base := mk(object.ID{}, map[string]string{"a.txt": "1\n2\n3\n4\n5\n", "b.txt": "b\n"}, "base")
	_, _ = ws.SetRef("main", store.RefBranch, base.ID)

	// feature: edit a.txt line 5, add d.txt.
	feat := mk(base.Hash, map[string]string{"a.txt": "1\n2\n3\n4\n5 FEATURE\n", "d.txt": "D\n"}, "feat")
	rrReq, err := svc.Create(Source{Snapshot: feat.Hash}, "main", "feat", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// main moves a LOT: edit a.txt line 1 (disjoint), rework b.txt, add e.txt, delete b? no — add c.
	// Apply the main edits one commit at a time off the running tip.
	tip := base
	tip = mk(tip.Hash, map[string]string{"a.txt": "1 MAIN\n2\n3\n4\n5\n", "b.txt": "b\n", "c.txt": "C\n"}, "main: line1 + c")
	tip = mk(tip.Hash, map[string]string{"b.txt": "B REWORKED\n"}, "main: rework b")
	tip = mk(tip.Hash, map[string]string{"e.txt": "E\n"}, "main: add e")
	_, _ = ws.SetRef("main", store.RefBranch, tip.ID)

	res, err := svc.ApplyMerge(rrReq, testAuthor(), testUserID())
	if err != nil || !res.Clean {
		t.Fatalf("merge should be clean: err=%v clean=%v conflicts=%v", err, res.Clean, res.Conflicts)
	}
	ref, _ := ws.GetRef("main")
	rev, _ := repo.GetRevision(ref.Target)
	snap, _ := repo.GetSnapshot(rev.Hash)
	root := ws.MustTree(snap.TreeID)
	// main's additions survive.
	for _, f := range []string{"c.txt", "e.txt", "d.txt"} {
		if _, err := ws.FindEntry(root, f); err != nil {
			t.Fatalf("%s should survive the merge", f)
		}
	}
	// a.txt combines BOTH sides (main line1 + feature line5).
	ae, _ := ws.FindEntry(root, "a.txt")
	ab, _ := ws.ReadBlob(ae.ID)
	if string(ab) != "1 MAIN\n2\n3\n4\n5 FEATURE\n" {
		t.Fatalf("a.txt not line-merged: %q", string(ab))
	}
	// b.txt keeps main's rework.
	be, _ := ws.FindEntry(root, "b.txt")
	bb, _ := ws.ReadBlob(be.ID)
	if string(bb) != "B REWORKED\n" {
		t.Fatalf("b.txt rework lost: %q", string(bb))
	}
}

// TestMergeFromOlderBaseDoesNotDeleteNewTargetFiles pins the anti-wipe property
// for the "stale base" case: an RR whose source was branched from an OLDER
// target tip (so it LACKS files the target later added) must NOT delete those
// files — the merge base is the RR's recorded base, so the target's later
// additions are additions, not deletions.
func TestMergeFromOlderBaseDoesNotDeleteNewTargetFiles(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)
	svc := New(cs, repo)
	mk := func(parent object.ID, files map[string]string, msg string) *store.Revision {
		var ch []revision.FileChangeSpec
		for p, c := range files {
			ch = append(ch, revision.FileChangeSpec{Path: p, Content: []byte(c)})
		}
		_, r, err := ws.CommitFromChanges(parent, ch, msg, store.Author{Name: "t"}, "")
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	c1 := mk(object.ID{}, map[string]string{"a.txt": "A\n"}, "c1")
	c2 := mk(c1.Hash, map[string]string{"b.txt": "B\n"}, "c2")
	_, _ = ws.SetRef("main", store.RefBranch, c2.ID)

	// Branch `old` from c2 (has a,b), then main adds c.txt.
	// Create the RR from c2 itself (a valid snapshot with a,b).
	rrReq, err := svc.Create(Source{Snapshot: c2.Hash}, "main", "propose old", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// main adds c.txt AFTER the RR was opened.
	tip := mk(c2.Hash, map[string]string{"c.txt": "C\n"}, "main: add c")
	_, _ = ws.SetRef("main", store.RefBranch, tip.ID)

	res, err := svc.ApplyMerge(rrReq, testAuthor(), testUserID())
	if err != nil || !res.Clean {
		t.Fatalf("merge should be clean: %v %v", err, res.Clean)
	}
	ref, _ := ws.GetRef("main")
	rev, _ := repo.GetRevision(ref.Target)
	snap, _ := repo.GetSnapshot(rev.Hash)
	root := ws.MustTree(snap.TreeID)
	if _, err := ws.FindEntry(root, "c.txt"); err != nil {
		t.Fatalf("c.txt (added on the target after the RR opened) was DELETED by the merge")
	}
}
