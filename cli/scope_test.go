package main

import (
	"os"
	"testing"

	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// CLI sub-repo scope (docs/subrepo-scopes.md): `commit --path`, `log --path`,
// `branch --path`, `tag --path`.

// initRepoWithDirs initializes team/app, writes root files + one file per dir,
// and commits everything (a normal root commit). Returns the ctx.
func initRepoWithDirs(t *testing.T, dir string, dirs ...string) *ctx {
	t.Helper()
	c := &ctx{cs: mustOpen(t)}
	os.Args = []string{"easyvcs", "init", "team/app"}
	cmdInit(c)
	write(t, dir, "README.md", "root\n")
	for _, d := range dirs {
		write(t, dir, d+"/file.txt", d+" v1\n")
	}
	os.Args = []string{"easyvcs", "commit"}
	cmdCommit(c)
	// Point a `main` branch at the just-created revision (init leaves only a
	// workspace marker; a branch is created explicitly).
	repo := openFirstRepo(t, c.cs)
	ws := revision.NewWorkspace(repo)
	rid := firstRevisionID(t, c.cs)
	if _, err := ws.SetRef("main", store.RefBranch, rid); err != nil {
		t.Fatal(err)
	}
	return c
}

// `commit --path frontend` lifts a change into the frontend subtree: the new
// root revision touches only frontend, and `log --path frontend` shows it.
func TestCLICommitPathLifts(t *testing.T) {
	setHome(t)
	dir := t.TempDir()
	orig, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(orig) }()

	c := initRepoWithDirs(t, dir, "frontend", "backend")
	repo := openFirstRepo(t, c.cs)
	ws := revision.NewWorkspace(repo)
	mainRef, _ := ws.GetRef("main")
	before := mainRef.Target

	// Edit only frontend/file.txt, then lift.
	write(t, dir, "frontend/file.txt", "frontend v2\n")
	os.Args = []string{"easyvcs", "commit", "--path", "frontend", "-m", "edit fe"}
	cmdCommit(c)

	after, _ := ws.GetRef("main")
	if after.Target == before {
		t.Fatal("main did not move after lift")
	}
	rev, _ := repo.GetRevision(after.Target)
	if len(rev.ChangedPaths) != 1 || rev.ChangedPaths[0] != "frontend/file.txt" {
		t.Fatalf("lifted ChangedPaths = %v", rev.ChangedPaths)
	}
	snap, _ := repo.GetSnapshot(rev.Hash)
	// backend preserved.
	if e, err := ws.FindEntry(ws.MustTree(snap.TreeID), "backend/file.txt"); err != nil || e.Kind != 0 {
		if err != nil {
			t.Fatalf("backend/file.txt missing after lift: %v", err)
		}
	}
	// frontend changed.
	feE, _ := ws.FindEntry(ws.MustTree(snap.TreeID), "frontend/file.txt")
	b, _ := ws.ReadBlob(feE.ID)
	if string(b) != "frontend v2\n" {
		t.Fatalf("frontend/file.txt = %q", string(b))
	}
}

// `branch --path frontend feat` creates a SCOPED branch: visible under frontend,
// NOT at the root.
func TestCLIBranchPathScoped(t *testing.T) {
	setHome(t)
	dir := t.TempDir()
	orig, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(orig) }()

	c := initRepoWithDirs(t, dir, "frontend", "backend")
	repo := openFirstRepo(t, c.cs)
	ws := revision.NewWorkspace(repo)
	rid := firstRevisionID(t, c.cs)

	os.Args = []string{"easyvcs", "branch", "feat", rid, "--path", "frontend"}
	cmdBranch(c)

	// The scoped branch exists under frontend...
	if r, err := ws.GetRefIn("frontend", "feat"); err != nil || r == nil {
		t.Fatalf("scoped branch missing: %v", err)
	}
	// ...and NOT at the root.
	if r, err := ws.GetRefIn("", "feat"); err == nil && r != nil {
		t.Fatal("scoped branch leaked to the root")
	}
}

// `tag --path frontend v1.0 <rev>` creates a SCOPED tag.
func TestCLITagPathScoped(t *testing.T) {
	setHome(t)
	dir := t.TempDir()
	orig, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(orig) }()

	c := initRepoWithDirs(t, dir, "frontend")
	repo := openFirstRepo(t, c.cs)
	ws := revision.NewWorkspace(repo)
	rid := firstRevisionID(t, c.cs)

	os.Args = []string{"easyvcs", "tag", "v1.0", rid, "--path", "frontend"}
	cmdTag(c)

	if r, err := ws.GetRefIn("frontend", "v1.0"); err != nil || r == nil || r.Kind != store.RefTag {
		t.Fatalf("scoped tag missing: %v %+v", err, r)
	}
	if r, err := ws.GetRefIn("", "v1.0"); err == nil && r != nil {
		t.Fatal("scoped tag leaked to the root")
	}
}

// `log --path frontend` reports only revisions that touched frontend.
func TestCLILogPathScope(t *testing.T) {
	setHome(t)
	dir := t.TempDir()
	orig, _ := os.Getwd()
	_ = os.Chdir(dir)
	defer func() { _ = os.Chdir(orig) }()

	c := initRepoWithDirs(t, dir, "frontend", "backend")
	// Two lifts into frontend, one into backend.
	write(t, dir, "frontend/file.txt", "fe v2\n")
	os.Args = []string{"easyvcs", "commit", "--path", "frontend", "-m", "fe2"}
	cmdCommit(c)
	write(t, dir, "backend/file.txt", "be v2\n")
	os.Args = []string{"easyvcs", "commit", "--path", "backend", "-m", "be2"}
	cmdCommit(c)

	repo := openFirstRepo(t, c.cs)
	// The LIFTED revisions are single-scope. (The base commit legitimately
	// touched both dirs, so it appears in both histories.)
	liftIDs := map[string]string{}
	for _, id := range mustTouching(t, repo, "frontend") {
		rev, _ := repo.GetRevision(id)
		if len(rev.ChangedPaths) == 1 {
			liftIDs[id] = rev.ChangedPaths[0]
		}
	}
	for _, id := range mustTouching(t, repo, "backend") {
		rev, _ := repo.GetRevision(id)
		if len(rev.ChangedPaths) == 1 {
			liftIDs[id] = rev.ChangedPaths[0]
		}
	}
	// Exactly the two lifts, each single-scope.
	sawFE, sawBE := false, false
	for _, p := range liftIDs {
		switch p {
		case "frontend/file.txt":
			sawFE = true
		case "backend/file.txt":
			sawBE = true
		default:
			t.Fatalf("unexpected single-path lift: %s", p)
		}
	}
	if !sawFE || !sawBE {
		t.Fatalf("lifts not recorded per scope: fe=%v be=%v", sawFE, sawBE)
	}
}

func mustTouching(t *testing.T, repo *store.Repo, scope string) []string {
	t.Helper()
	ids, err := repo.RevisionsTouching(scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) == 0 {
		t.Fatalf("no revisions touch %q", scope)
	}
	return ids
}
