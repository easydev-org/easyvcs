package gitserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
	"github.com/go-git/go-git/v5/plumbing/storer"
)

// TestGitClone serves an easyvcs repo over smart HTTP and clones it with the
// REAL git CLI, proving a standard `git clone` (and thus git-as-a-package
// consumers) works.
func TestGitClone(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	t.Setenv("EASYVCS_HOME", t.TempDir())
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	repo, _ := cs.Create(store.NSRef{Name: "acme" + "/" + "lib"})
	ws := revision.NewWorkspace(repo)
	_, r1, err := ws.CommitFromChanges(object.ID{}, []revision.FileChangeSpec{{Path: "go.mod", Content: []byte("module acme/lib\n")}}, "init", store.Author{Name: "t", Email: "t@x"}, "")
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := ws.GetSnapshot(r1.Hash)
	_, r2, err := ws.CommitFromChanges(s1.RevisionHash, []revision.FileChangeSpec{{Path: "lib.go", Content: []byte("package lib\n")}}, "add lib.go", store.Author{Name: "t", Email: "t@x"}, "")
	if err != nil {
		t.Fatal(err)
	}
	// main points at the TIP (r2).
	if _, err := ws.SetRef("main", store.RefBranch, r2.ID); err != nil {
		t.Fatal(err)
	}

	sto, err := Build(ws, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(sto)
	mux := http.NewServeMux()
	mux.HandleFunc("/repo.git/info/refs", srv.InfoRefs)
	mux.HandleFunc("/repo.git/git-upload-pack", srv.UploadPack)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	dst := filepath.Join(t.TempDir(), "clone")
	out, err := exec.Command("git", "clone", ts.URL+"/repo.git", dst).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone failed: %v\n%s", err, out)
	}
	// The clone must contain both files at the tip.
	for _, f := range []string{"go.mod", "lib.go"} {
		if _, err := os.Stat(filepath.Join(dst, f)); err != nil {
			t.Fatalf("cloned repo missing %s: %v", f, err)
		}
	}
	logOut, _ := exec.Command("git", "-C", dst, "log", "--oneline").CombinedOutput()
	if n := len(strings.Split(strings.TrimSpace(string(logOut)), "\n")); n != 2 {
		t.Fatalf("cloned history has %d commits, want 2:\n%s", n, logOut)
	}
}

// TestGitCloneSubdir verifies a monorepo SUBDIRECTORY is served as its own git
// repository (its history = the commits that touch that path), so a subdir can
// be consumed as an independent package.
func TestGitCloneSubdir(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	t.Setenv("EASYVCS_HOME", t.TempDir())
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	repo, _ := cs.Create(store.NSRef{Name: "acme" + "/" + "mono"})
	ws := revision.NewWorkspace(repo)
	// commit 1: only top-level (no pkg-a yet)
	_, r1, _ := ws.CommitFromChanges(object.ID{}, []revision.FileChangeSpec{{Path: "README.md", Content: []byte("mono\n")}}, "init", store.Author{Name: "t"}, "")
	s1, _ := ws.GetSnapshot(r1.Hash)
	// commit 2: add pkg-a/go.mod
	_, r2, _ := ws.CommitFromChanges(s1.RevisionHash, []revision.FileChangeSpec{{Path: "pkg-a/go.mod", Content: []byte("module acme/mono/pkg-a\n")}}, "add pkg-a", store.Author{Name: "t"}, "")
	s2, _ := ws.GetSnapshot(r2.Hash)
	// commit 3: add pkg-a/a.go
	_, r3, _ := ws.CommitFromChanges(s2.RevisionHash, []revision.FileChangeSpec{{Path: "pkg-a/a.go", Content: []byte("package a\n")}}, "add a.go", store.Author{Name: "t"}, "")
	if _, err := ws.SetRef("main", store.RefBranch, r3.ID); err != nil {
		t.Fatal(err)
	}

	sto, err := Build(ws, repo, "pkg-a")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(sto)
	mux := http.NewServeMux()
	mux.HandleFunc("/pkg-a/info/refs", srv.InfoRefs)
	mux.HandleFunc("/pkg-a/git-upload-pack", srv.UploadPack)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	dst := filepath.Join(t.TempDir(), "clone")
	out, err := exec.Command("git", "clone", ts.URL+"/pkg-a", dst).CombinedOutput()
	if err != nil {
		t.Fatalf("git clone subdir failed: %v\n%s", err, out)
	}
	// The subdir clone's ROOT is pkg-a's contents (a.go, go.mod), NOT README.md.
	for _, f := range []string{"go.mod", "a.go"} {
		if _, err := os.Stat(filepath.Join(dst, f)); err != nil {
			t.Fatalf("subdir clone missing %s: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dst, "README.md")); err == nil {
		t.Fatal("subdir clone should NOT contain the monorepo's README.md")
	}
	// History = only the 2 commits that touch pkg-a (commit 1 is skipped).
	logOut, _ := exec.Command("git", "-C", dst, "log", "--oneline").CombinedOutput()
	if n := len(strings.Split(strings.TrimSpace(string(logOut)), "\n")); n != 2 {
		t.Fatalf("subdir history has %d commits, want 2:\n%s", n, logOut)
	}
}

// TestSubdirRefScopeNoLeak verifies the projection rule at the git layer
// (docs/subrepo-scopes.md §9): a branch scoped to pkg-a is NOT offered in the
// repo root's clone, and a branch scoped to pkg-b is NOT offered in pkg-a's
// clone. Root branches still project into the subdirs whose tip has them.
func TestSubdirRefScopeNoLeak(t *testing.T) {
	t.Setenv("EASYVCS_HOME", t.TempDir())
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	repo, _ := cs.Create(store.NSRef{Name: "acme" + "/" + "mono"})
	ws := revision.NewWorkspace(repo)
	_, rev, _ := ws.CommitFromChanges(object.ID{},
		[]revision.FileChangeSpec{
			{Path: "pkg-a/a.go", Content: []byte("package a\n")},
			{Path: "pkg-b/b.go", Content: []byte("package b\n")},
		}, "init", store.Author{Name: "t"}, "")
	// Root branch main + two scoped branches.
	_, _ = ws.SetRef("main", store.RefBranch, rev.ID)
	_, _ = ws.SetRefIn("pkg-a", "feat-a", store.RefBranch, rev.ID)
	_, _ = ws.SetRefIn("pkg-b", "feat-b", store.RefBranch, rev.ID)

	// Root clone: sees only `main` (no feat-a / feat-b).
	rootSto, err := Build(ws, repo, "")
	if err != nil {
		t.Fatal(err)
	}
	rootRefs := refNamesOf(t, rootSto)
	if !rootRefs["main"] {
		t.Fatalf("root clone should have main: %v", rootRefs)
	}
	if rootRefs["feat-a"] || rootRefs["feat-b"] {
		t.Fatalf("a scoped branch leaked into the root clone: %v", rootRefs)
	}

	// pkg-a clone: main (projected) + feat-a (own), NOT feat-b.
	aSto, err := Build(ws, repo, "pkg-a")
	if err != nil {
		t.Fatal(err)
	}
	aRefs := refNamesOf(t, aSto)
	if !aRefs["main"] || !aRefs["feat-a"] {
		t.Fatalf("pkg-a clone = %v, want main + feat-a", aRefs)
	}
	if aRefs["feat-b"] {
		t.Fatalf("a sibling scope's branch leaked into pkg-a: %v", aRefs)
	}
}

// refNamesOf lists the short branch names in a storer.
func refNamesOf(t *testing.T, sto storer.Storer) map[string]bool {
	t.Helper()
	it, err := sto.IterReferences()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for {
		ref, err := it.Next()
		if err != nil {
			break
		}
		if ref.Name().IsBranch() {
			out[ref.Name().Short()] = true
		}
	}
	it.Close()
	return out
}

// TestGitCloneDirFileOrder pins the git tree-order fix: a tree containing BOTH
// "a.txt" and a directory "a/" must be served in git's tree order (a directory
// compares as if its name had a trailing "/", so "a/" < "a.txt"), otherwise
// go-git's Tree.Encode REJECTS the out-of-order tree ("entries in tree are not
// sorted") and `git clone` 500s. Plain byte order would put "a.txt" before "a".
func TestGitCloneDirFileOrder(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	t.Setenv("EASYVCS_HOME", t.TempDir())
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	repo, _ := cs.Create(store.NSRef{Name: "order"})
	ws := revision.NewWorkspace(repo)
	// A file "a.txt" AND a directory "a" with a child: "a.txt" < "a" byte-wise,
	// but git order needs "a/" < "a.txt".
	_, r1, err := ws.CommitFromChanges(object.ID{}, []revision.FileChangeSpec{
		{Path: "a.txt", Content: []byte("file\n")},
		{Path: "a/b.txt", Content: []byte("nested\n")},
	}, "init", store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("main", store.RefBranch, r1.ID); err != nil {
		t.Fatal(err)
	}

	sto, err := Build(ws, repo, "")
	if err != nil {
		t.Fatalf("Build (tree encode) failed on a dir/file-order tree: %v", err)
	}
	srv := New(sto)
	mux := http.NewServeMux()
	mux.HandleFunc("/repo.git/info/refs", srv.InfoRefs)
	mux.HandleFunc("/repo.git/git-upload-pack", srv.UploadPack)
	ts := httptest.NewServer(mux)
	defer ts.Close()

	dst := filepath.Join(t.TempDir(), "clone")
	if out, err := exec.Command("git", "clone", ts.URL+"/repo.git", dst).CombinedOutput(); err != nil {
		t.Fatalf("git clone failed: %v\n%s", err, out)
	}
	for _, f := range []string{"a.txt", "a/b.txt"} {
		if _, err := os.Stat(filepath.Join(dst, f)); err != nil {
			t.Fatalf("cloned repo missing %s: %v", f, err)
		}
	}
}
