package rr

import (
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// testAuthor is the merge/resolve identity used by tests.
func testAuthor() store.Author {
	return store.Author{Name: "tester", Email: "tester@example.com"}
}

// testUserID is the merger user id used by tests.
func testUserID() *int64 {
	id := int64(1)
	return &id
}

func newStore(t *testing.T) (*store.CentralStore, *store.Repo) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("EASYVCS_HOME", home)
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := cs.Create(store.NSRef{Name: "team" + "/" + "app"})
	if err != nil {
		t.Fatal(err)
	}
	return cs, repo
}

// treeOf builds a full tree from a path->content map.
func treeOf(t *testing.T, ws *revision.Workspace, files map[string]string) object.ID {
	t.Helper()
	tree := object.NewTree()
	for name, content := range files {
		blob, err := ws.WriteBlob([]byte(content))
		if err != nil {
			t.Fatal(err)
		}
		tree.Entries[name] = object.Entry{Name: name, Kind: object.KindBlob, ID: blob}
	}
	id, err := ws.WriteTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// commitFiles commits a full-tree snapshot on top of parents.
func commitFiles(t *testing.T, ws *revision.Workspace, parents []object.ID, files map[string]string, msg string) *store.Snapshot {
	t.Helper()
	snap, _, err := ws.Commit(revision.CommitParams{
		Parents: parents, TreeID: treeOf(t, ws, files), Description: msg,
		Author: store.Author{Name: "t", Email: "t@x"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

// commitFilesAs commits a full-tree snapshot with an explicit author.
func commitFilesAs(t *testing.T, ws *revision.Workspace, parents []object.ID, files map[string]string, msg string, author store.Author) *store.Snapshot {
	t.Helper()
	snap, _, err := ws.Commit(revision.CommitParams{
		Parents: parents, TreeID: treeOf(t, ws, files), Description: msg, Author: author,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}

func TestCreateFromSnapshotAndMerge(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)

	base := commitFiles(t, ws, nil, map[string]string{"README.md": "hello\n"}, "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}

	// A "local directory" request: a bare snapshot on top of the base, no branch.
	src := commitFiles(t, ws, []object.ID{base.RevisionHash},
		map[string]string{"README.md": "hello\nworld\n"}, "add world")

	svc := New(cs, repo)
	req, err := svc.Create(Source{Snapshot: src.RevisionHash}, "main", "add world", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if req.SourceSnapshot != src.RevisionHash.String() {
		t.Fatalf("pinned source snapshot = %s, want %s", req.SourceSnapshot, src.RevisionHash)
	}
	if req.BaseSnapshot != base.RevisionHash.String() {
		t.Fatalf("pinned base snapshot = %s, want %s", req.BaseSnapshot, base.RevisionHash)
	}

	diffs, err := svc.Diff(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(diffs) != 1 || diffs[0].Path != "README.md" {
		t.Fatalf("expected 1 diff on README.md, got %+v", diffs)
	}

	res, err := svc.ApplyMerge(req, testAuthor(), testUserID())
	if err != nil {
		t.Fatal(err)
	}
	if !res.Clean {
		t.Fatalf("expected clean merge, conflicts=%v", res.Conflicts)
	}
	mainRef, err := repo.GetRef("main")
	if err != nil {
		t.Fatal(err)
	}
	mergedRev, err := repo.GetRevision(mainRef.Target)
	if err != nil {
		t.Fatal(err)
	}
	mergedSnap, err := repo.GetSnapshot(mergedRev.Hash)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := ws.FindEntry(ws.MustTree(mergedSnap.TreeID), "README.md")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ws.ReadBlob(entry.ID)
	if string(got) != "hello\nworld\n" {
		t.Fatalf("merged content = %q", string(got))
	}
}

// TestMergeAuthorshipUsesSourceAuthor pins the landing-commit authorship: the
// merge commit is authored by the SOURCE snapshot's author (who wrote the
// change), so blame attributes the merged lines to their author — not to whoever
// pressed merge. The merger is recorded separately as rr_merge.merged_by.
func TestMergeAuthorshipUsesSourceAuthor(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)

	base := commitFiles(t, ws, nil, map[string]string{"README.md": "v1\n"}, "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	// alice writes the change.
	alice := store.Author{Name: "alice", Email: "alice@example.com"}
	src := commitFilesAs(t, ws, []object.ID{base.RevisionHash},
		map[string]string{"README.md": "v1\nalice\n"}, "alice edit", alice)

	svc := New(cs, repo)
	req, err := svc.Create(Source{Snapshot: src.RevisionHash}, "main", "alice's change", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// bob merges it.
	bobID := int64(7)
	bob := store.Author{Name: "bob", Email: "bob@example.com"}
	res, err := svc.ApplyMerge(req, bob, &bobID)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Clean {
		t.Fatalf("expected clean merge, conflicts=%v", res.Conflicts)
	}
	mergedRev, err := repo.GetRevision(res.MergedRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	mergedSnap, err := repo.GetSnapshot(mergedRev.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if mergedSnap.Author.Name != "alice" {
		t.Fatalf("landing commit author = %q, want alice (the source author)", mergedSnap.Author.Name)
	}
	pm, err := cs.GetRRMerge(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if pm.MergedBy == nil || *pm.MergedBy != bobID {
		t.Fatalf("merged_by = %v, want %d (bob)", pm.MergedBy, bobID)
	}
}

// TestMergeAuthorFallsBackToMerger covers a source snapshot with no author: the
// landing commit falls back to the merger's identity.
func TestMergeAuthorFallsBackToMerger(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)

	base := commitFiles(t, ws, nil, map[string]string{"README.md": "v1\n"}, "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	// A source snapshot with NO author.
	src := commitFilesAs(t, ws, []object.ID{base.RevisionHash},
		map[string]string{"README.md": "v1\nanon\n"}, "anon edit", store.Author{})

	svc := New(cs, repo)
	req, err := svc.Create(Source{Snapshot: src.RevisionHash}, "main", "anon change", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	bob := store.Author{Name: "bob", Email: "bob@example.com"}
	res, err := svc.ApplyMerge(req, bob, nil)
	if err != nil {
		t.Fatal(err)
	}
	mergedRev, err := repo.GetRevision(res.MergedRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	mergedSnap, err := repo.GetSnapshot(mergedRev.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if mergedSnap.Author.Name != "bob" {
		t.Fatalf("landing commit author = %q, want bob (fallback to merger)", mergedSnap.Author.Name)
	}
}

// A merge always produces a NEW revision on the target (there is no fast-forward
// case: the source is a bare snapshot). Here main advances before the merge, so
// the merge is a 3-way onto the moved tip.
func TestMergeCommitsNewRevisionOnTarget(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)

	base := commitFiles(t, ws, nil, map[string]string{"README.md": "v1\n"}, "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	// The request's source branches off the base and edits README.
	src := commitFiles(t, ws, []object.ID{base.RevisionHash},
		map[string]string{"README.md": "v1\nrequest\n"}, "request edit")

	svc := New(cs, repo)
	req, err := svc.Create(Source{Snapshot: src.RevisionHash}, "main", "request", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// Meanwhile main advances with an unrelated NEW file (README unchanged on
	// main), so the merge is clean.
	adv := commitFiles(t, ws, []object.ID{base.RevisionHash},
		map[string]string{"README.md": "v1\n", "other.txt": "x\n"}, "advance")
	if _, err := ws.SetRef("main", store.RefBranch, adv.RevisionID); err != nil {
		t.Fatal(err)
	}
	preMergeTip, _ := repo.GetRef("main")
	preMergeRev, _ := repo.GetRevision(preMergeTip.Target)

	res, err := svc.ApplyMerge(req, testAuthor(), testUserID())
	if err != nil {
		t.Fatal(err)
	}
	if res.MergedRevisionID == "" {
		t.Fatalf("expected a new revision committed on the target")
	}
	// No fast-forward: the merge produced a new target revision whose single
	// parent is the target tip at merge time.
	mergedRev, err := repo.GetRevision(res.MergedRevisionID)
	if err != nil {
		t.Fatal(err)
	}
	mergedSnap, err := repo.GetSnapshot(mergedRev.Hash)
	if err != nil {
		t.Fatal(err)
	}
	if len(mergedSnap.Parents) != 1 || mergedSnap.Parents[0] != preMergeRev.Hash {
		t.Fatalf("merged revision parent = %v, want [%s]", mergedSnap.Parents, preMergeRev.Hash)
	}
	mainRef, _ := repo.GetRef("main")
	if mainRef.Target != res.MergedRevisionID {
		t.Fatalf("main -> %s, want the merge revision %s", mainRef.Target, res.MergedRevisionID)
	}
	// The source snapshot is untouched (still present, not repointed).
	if _, err := repo.GetSnapshot(src.RevisionHash); err != nil {
		t.Fatalf("source snapshot vanished: %v", err)
	}
	paths, err := ws.CollectPaths(mergedSnap.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		seen[p] = true
	}
	if !seen["README.md"] || !seen["other.txt"] {
		t.Fatalf("merged tree missing files: %v", paths)
	}
}

func TestImmutablePinAndStale(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)

	base := commitFiles(t, ws, nil, map[string]string{"README.md": "v1\n"}, "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	srcSnap := commitFiles(t, ws, []object.ID{base.RevisionHash},
		map[string]string{"README.md": "v1\nfeat\n"}, "feat")
	if _, err := ws.SetRef("feat", store.RefBranch, srcSnap.RevisionID); err != nil {
		t.Fatal(err)
	}

	// Not stale yet: target has not moved, and the source is a pinned snapshot.
	svc := New(cs, repo)
	req, err := svc.Create(Source{Snapshot: srcSnap.RevisionHash}, "main", "feat", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	before, err := svc.Diff(req)
	if err != nil {
		t.Fatal(err)
	}

	// Advance the TARGET branch (main). The pinned source snapshot is immutable,
	// so the RR's diff must NOT drift; the RR only becomes "stale" as a hint.
	if _, err := ws.SetRef("main", store.RefBranch, srcSnap.RevisionID); err != nil {
		t.Fatal(err)
	}
	// (main now points at a different snapshot than the pinned base_snapshot)

	after, err := svc.Diff(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) || before[0].Content != after[0].Content {
		t.Fatalf("pinned diff drifted:\nbefore=%q\nafter=%q", before[0].Content, after[0].Content)
	}
	if req.SourceSnapshot != srcSnap.RevisionHash.String() {
		t.Fatalf("source pin drifted: %s", req.SourceSnapshot)
	}

	stale, err := svc.IsStale(req)
	if err != nil {
		t.Fatal(err)
	}
	if !stale {
		t.Fatalf("expected RR to be stale after the target branch moved")
	}
}

func TestConflictAndResolve(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)

	base := commitFiles(t, ws, nil, map[string]string{"f.txt": "base\n"}, "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	src := commitFiles(t, ws, []object.ID{base.RevisionHash}, map[string]string{"f.txt": "request-side\n"}, "request")

	svc := New(cs, repo)
	req, err := svc.Create(Source{Snapshot: src.RevisionHash}, "main", "conflict", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// main changes the SAME file -> genuine (file-level) conflict.
	adv := commitFiles(t, ws, []object.ID{base.RevisionHash}, map[string]string{"f.txt": "main-side\n"}, "advance")
	if _, err := ws.SetRef("main", store.RefBranch, adv.RevisionID); err != nil {
		t.Fatal(err)
	}

	res, err := svc.ApplyMerge(req, testAuthor(), testUserID())
	if err != nil {
		t.Fatal(err)
	}
	if res.Clean {
		t.Fatalf("expected a conflicted merge")
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "f.txt" {
		t.Fatalf("expected conflict at f.txt, got %v", res.Conflicts)
	}
	got, err := cs.GetRevisionRequest(repo.NamespaceID(), req.IID)
	if err != nil {
		t.Fatal(err)
	}
	// A conflict is NOT an RR state: the request stays OPEN, and the conflict is
	// derived from the pending merge.
	if got.State != store.RROpen {
		t.Fatalf("state = %s, want open (conflict is a snapshot property, not a state)", got.State)
	}
	if c := svc.PendingConflicts(got); len(c) != 1 || c[0] != "f.txt" {
		t.Fatalf("PendingConflicts = %v, want [f.txt]", c)
	}
	// Resolve genuinely (side 0 = the source's own content). After resolving the
	// only conflict, the pending snapshot is clean.
	if _, err := svc.Resolve(got, "f.txt", 0, testAuthor(), testUserID()); err != nil {
		t.Fatal(err)
	}
	got2, _ := cs.GetRevisionRequest(repo.NamespaceID(), req.IID)
	if got2.State != store.RROpen {
		t.Fatalf("state after resolve = %s, want open", got2.State)
	}
	// Merging again adopts the resolved pending snapshot and lands it on main.
	res2, err := svc.ApplyMerge(got2, testAuthor(), testUserID())
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Clean {
		t.Fatalf("merge after resolve should be clean, conflicts=%v", res2.Conflicts)
	}
	mainRef, _ := repo.GetRef("main")
	mergedRev, _ := repo.GetRevision(mainRef.Target)
	mergedSnap, _ := repo.GetSnapshot(mergedRev.Hash)
	entry, err := ws.FindEntry(ws.MustTree(mergedSnap.TreeID), "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := ws.ReadBlob(entry.ID)
	if string(data) != "request-side\n" {
		t.Fatalf("resolved content = %q, want request-side", string(data))
	}
	// The landed tree must carry no conflict objects.
	if atoms, err := ws.ConflictsInTree(mergedSnap.TreeID); err != nil || len(atoms) != 0 {
		t.Fatalf("landed tree still has conflicts: %v (err=%v)", atoms, err)
	}
}

func TestUpdateRepinsAndRecordsHistory(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)
	base := commitFiles(t, ws, nil, map[string]string{"a.txt": "1\n"}, "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	s1 := commitFiles(t, ws, []object.ID{base.RevisionHash}, map[string]string{"a.txt": "2\n"}, "s1")
	s2 := commitFiles(t, ws, []object.ID{base.RevisionHash}, map[string]string{"a.txt": "3\n"}, "s2")

	svc := New(cs, repo)
	req, err := svc.Create(Source{Snapshot: s1.RevisionHash}, "main", "rr", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := svc.Update(req, s2.RevisionHash, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.SourceSnapshot != s2.RevisionHash.String() {
		t.Fatalf("after update source = %s, want %s", updated.SourceSnapshot, s2.RevisionHash)
	}
	hist, err := cs.ListRRUpdates(req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Fatalf("expected 2 update records (create + update), got %d", len(hist))
	}
}

// TestEmptyRepoRRSnapshotOnly is the regression for opening an RR against an
// EMPTY repository (no target branch yet). base_snapshot is all-zero, which must
// be treated as the empty tree: Diff shows every source file as added and Merge
// creates the target branch. Before the fix, Diff/ComputeMerge called
// GetSnapshot(zero) and returned "snapshot 000…0 not found" (a 500 over HTTP).
func TestEmptyRepoRRSnapshotOnly(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)

	// The repo has NO main and NO revisions: only the source snapshot exists.
	src := commitFiles(t, ws, nil, map[string]string{
		"README.md": "# new project\n",
		"src/a.go":  "package a\n",
	}, "initial")

	svc := New(cs, repo)
	req, err := svc.Create(Source{Snapshot: src.RevisionHash}, "main", "initial import", "", nil)
	if err != nil {
		t.Fatalf("create RR on empty repo: %v", err)
	}
	if req.BaseSnapshot != (object.ID{}).String() {
		t.Fatalf("base_snapshot = %s, want all-zero for an empty repo", req.BaseSnapshot)
	}

	// Diff against the (empty) base: every file is added.
	diffs, err := svc.Diff(req)
	if err != nil {
		t.Fatalf("diff on empty repo RR: %v", err)
	}
	if len(diffs) != 2 {
		t.Fatalf("expected 2 added files, got %d (%+v)", len(diffs), diffs)
	}
	for _, d := range diffs {
		if d.Status != revision.StatusAdded {
			t.Fatalf("empty-repo diff should be all added, got %s for %s", d.Status, d.Path)
		}
	}

	// Merge creates the target branch (main) pointing at a new revision.
	res, err := svc.ApplyMerge(req, testAuthor(), testUserID())
	if err != nil {
		t.Fatalf("merge on empty repo RR: %v", err)
	}
	if !res.Clean {
		t.Fatalf("expected a clean merge, conflicts=%v", res.Conflicts)
	}
	if res.MergedRevisionID == "" {
		t.Fatal("expected a merge revision to be created")
	}
	mainRef, err := repo.GetRef("main")
	if err != nil {
		t.Fatalf("main should now exist: %v", err)
	}
	if mainRef.Target != res.MergedRevisionID {
		t.Fatalf("main -> %s, want the merge revision %s", mainRef.Target, res.MergedRevisionID)
	}
	mergedRev, _ := repo.GetRevision(mainRef.Target)
	mergedSnap, _ := repo.GetSnapshot(mergedRev.Hash)
	// The merge revision is a root (no parent) because the target did not exist.
	if len(mergedSnap.Parents) != 0 {
		t.Fatalf("merge onto an empty repo should be a root revision, parents=%v", mergedSnap.Parents)
	}
	entry, err := ws.FindEntry(ws.MustTree(mergedSnap.TreeID), "README.md")
	if err != nil {
		t.Fatalf("merged tree missing README.md: %v", err)
	}
	data, _ := ws.ReadBlob(entry.ID)
	if string(data) != "# new project\n" {
		t.Fatalf("merged content = %q", string(data))
	}
}

// TestMergeAllowConflict lands a CONFLICTED merge onto the target when the
// caller opts in (MergeOptions.AllowConflict): the branch moves to a tree whose
// conflicted file is a first-class conflict object (readable as marker text),
// and the RR is marked merged. The default (no opt-in) keeps it pending.
func TestMergeAllowConflict(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)

	base := commitFiles(t, ws, nil, map[string]string{"f.txt": "base\n"}, "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	src := commitFiles(t, ws, []object.ID{base.RevisionHash}, map[string]string{"f.txt": "request-side\n"}, "request")
	svc := New(cs, repo)
	req, err := svc.Create(Source{Snapshot: src.RevisionHash}, "main", "conflict", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	// Advance main on the SAME file -> genuine conflict.
	adv := commitFiles(t, ws, []object.ID{base.RevisionHash}, map[string]string{"f.txt": "main-side\n"}, "advance")
	if _, err := ws.SetRef("main", store.RefBranch, adv.RevisionID); err != nil {
		t.Fatal(err)
	}

	// Opt in: the conflicted merge lands on main.
	res, err := svc.ApplyMergeOpts(req, testAuthor(), testUserID(), MergeOptions{AllowConflict: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Clean {
		t.Fatal("expected a conflicted (non-clean) result")
	}
	if len(res.Conflicts) != 1 || res.Conflicts[0] != "f.txt" {
		t.Fatalf("expected conflict at f.txt, got %v", res.Conflicts)
	}
	// main MOVED to the merge revision and its tree carries a conflict object.
	mainRef, _ := repo.GetRef("main")
	if mainRef.Target != res.MergedRevisionID {
		t.Fatalf("main did not move to the merge revision")
	}
	mergedRev, _ := repo.GetRevision(mainRef.Target)
	mergedSnap, _ := repo.GetSnapshot(mergedRev.Hash)
	entry, err := ws.FindEntry(ws.MustTree(mergedSnap.TreeID), "f.txt")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Kind != object.KindConflict {
		t.Fatalf("landed file kind = %v, want conflict", entry.Kind)
	}
	// The RR is marked merged.
	got, _ := cs.GetRevisionRequest(repo.NamespaceID(), req.IID)
	if got.State != store.RRMerged {
		t.Fatalf("state = %s, want merged", got.State)
	}
}

// TestMergeAllowConflictAdoptsPendingResolved: a merge that was conflicted (and
// left pending) can be resolved on ONE file and then landed with AllowConflict,
// carrying the remaining conflict.
func TestMergeAllowConflictDefaultStaysPending(t *testing.T) {
	cs, repo := newStore(t)
	ws := revision.NewWorkspace(repo)
	base := commitFiles(t, ws, nil, map[string]string{"f.txt": "base\n"}, "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	src := commitFiles(t, ws, []object.ID{base.RevisionHash}, map[string]string{"f.txt": "request-side\n"}, "request")
	svc := New(cs, repo)
	req, _ := svc.Create(Source{Snapshot: src.RevisionHash}, "main", "conflict", "", nil)
	adv := commitFiles(t, ws, []object.ID{base.RevisionHash}, map[string]string{"f.txt": "main-side\n"}, "advance")
	_, _ = ws.SetRef("main", store.RefBranch, adv.RevisionID)

	// Default: conflicted merge stays pending; main does NOT move.
	if _, err := svc.ApplyMerge(req, testAuthor(), testUserID()); err != nil {
		t.Fatal(err)
	}
	mainRef, _ := repo.GetRef("main")
	if mainRef.Target != adv.RevisionID {
		t.Fatalf("default conflicted merge must NOT move main")
	}
	got, _ := cs.GetRevisionRequest(repo.NamespaceID(), req.IID)
	// The RR stays OPEN; the conflict is on the pending merge snapshot.
	if got.State != store.RROpen {
		t.Fatalf("state = %s, want open", got.State)
	}
	if c := svc.PendingConflicts(got); len(c) != 1 || c[0] != "f.txt" {
		t.Fatalf("PendingConflicts = %v, want [f.txt]", c)
	}
}
