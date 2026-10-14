package store

import (
	"fmt"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// Round-2 scope tests: the projection rule driven by REAL tip trees (hasDir),
// migration, multi-repo isolation, and scale. In-memory SQLite throughout.

// writeTree stores a tree object at the given path -> content map and returns
// its id. Paths may be nested ("a/b"); a missing intermediate dir is created.
func writeTree(t *testing.T, repo *Repo, files map[string]string) object.ID {
	t.Helper()
	// Build a nested tree from the flat path->content map.
	dirs := map[string]*object.Tree{"": object.NewTree()}
	getDir := func(p string) *object.Tree {
		if d, ok := dirs[p]; ok {
			return d
		}
		d := object.NewTree()
		dirs[p] = d
		return d
	}
	for path, content := range files {
		blob := &object.Object{Kind: object.KindBlob, Blob: []byte(content)}
		if err := repo.WriteObject(blob); err != nil {
			t.Fatal(err)
		}
		// Split into dir + base.
		dir, base := "", path
		for i := len(path) - 1; i >= 0; i-- {
			if path[i] == '/' {
				dir, base = path[:i], path[i+1:]
				break
			}
		}
		getDir(dir).Entries[base] = object.Entry{Name: base, ID: blob.ID(), Kind: object.KindBlob}
	}
	// Link child dirs into parents, deepest first.
	// Collect dir paths by depth (descending).
	paths := make([]string, 0, len(dirs))
	for p := range dirs {
		if p != "" {
			paths = append(paths, p)
		}
	}
	// simple: repeatedly wire until stable (small maps in tests).
	for pass := 0; pass < 5; pass++ {
		for _, p := range paths {
			d := dirs[p]
			for _, o := range dirs {
				for name, e := range o.Entries {
					if e.Kind == object.KindTree && e.ID == d.ID() {
						_ = name
					}
				}
			}
		}
	}
	// Wire each dir into its parent.
	wire := func(child string) {
		parent, base := "", child
		for i := len(child) - 1; i >= 0; i-- {
			if child[i] == '/' {
				parent, base = child[:i], child[i+1:]
				break
			}
		}
		getDir(parent).Entries[base] = object.Entry{Name: base, ID: getDir(child).ID(), Kind: object.KindTree}
	}
	// Children before parents: sort by length descending.
	for i := 0; i < len(paths); i++ {
		for j := i + 1; j < len(paths); j++ {
			if len(paths[j]) > len(paths[i]) {
				paths[i], paths[j] = paths[j], paths[i]
			}
		}
	}
	for _, p := range paths {
		writeObjectTree(t, repo, dirs[p])
		wire(p)
	}
	root := dirs[""]
	writeObjectTree(t, repo, root)
	return root.ID()
}

func writeObjectTree(t *testing.T, repo *Repo, tr *object.Tree) {
	t.Helper()
	if err := repo.WriteObject(&object.Object{Kind: object.KindTree, Tree: tr}); err != nil {
		t.Fatal(err)
	}
}

// snapshotWith writes a tree from files and a snapshot pointing at it; returns
// the snapshot + a Revision already stored (so a ref target resolves).
func snapshotWith(t *testing.T, repo *Repo, revID string, files map[string]string) {
	t.Helper()
	tree := writeTree(t, repo, files)
	snap := &Snapshot{RevisionID: revID, TreeID: tree, Author: Author{Name: "a"}}
	snap.RevisionHash = SnapshotHashFor(snap)
	if err := repo.PutSnapshot(snap); err != nil {
		t.Fatal(err)
	}
	if err := repo.PutRevision(&Revision{ID: revID, Hash: snap.RevisionHash, Created: now()}); err != nil {
		t.Fatal(err)
	}
}

// hasDirIn builds a hasDir closure using a REAL tip-tree lookup: it reports
// whether the dir exists at the ref's target revision.
func hasDirIn(t *testing.T, repo *Repo, dir string) func(string) bool {
	t.Helper()
	return func(target string) bool {
		rev, err := repo.GetRevision(target)
		if err != nil {
			return false
		}
		snap, err := repo.GetSnapshot(rev.Hash)
		if err != nil {
			return false
		}
		obj, err := repo.ReadObject(snap.TreeID)
		if err != nil || obj.Tree == nil {
			return false
		}
		// Walk the path segments.
		cur := obj.Tree
		for _, seg := range splitPath(dir) {
			e, ok := cur.Entries[seg]
			if !ok || e.Kind != object.KindTree {
				return false
			}
			next, err := repo.ReadObject(e.ID)
			if err != nil || next.Tree == nil {
				return false
			}
			cur = next.Tree
		}
		return true
	}
}

func splitPath(p string) []string {
	var out []string
	cur := ""
	for _, c := range p {
		if c == '/' {
			if cur != "" {
				out = append(out, cur)
				cur = ""
			}
			continue
		}
		cur += string(c)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// End-to-end (store-level): a root branch projects into a sub-scope iff its tip
// tree really contains that directory — with REAL trees, not a stub.
func TestProjectionWithRealTrees(t *testing.T) {
	repo := newScopedRepo(t)
	snapshotWith(t, repo, "rev-full", map[string]string{
		"README.md":         "root",
		"frontend/app.ts":   "fe",
		"backend/server.go": "be",
	})
	snapshotWith(t, repo, "rev-docs", map[string]string{"README.md": "only docs"})

	_ = repo.PutRef(&Ref{Name: "main", Kind: RefBranch, Target: "rev-full"})
	_ = repo.PutRef(&Ref{Name: "docs", Kind: RefBranch, Target: "rev-docs"})

	// frontend: main projects in (tip has frontend/), docs does NOT.
	fe, err := repo.ListRefsIn("frontend", hasDirIn(t, repo, "frontend"))
	if err != nil {
		t.Fatal(err)
	}
	n := refNames(fe)
	if !n["main"] {
		t.Fatal("main should project into frontend (real tree has it)")
	}
	if n["docs"] {
		t.Fatal("docs should NOT project into frontend (its tip lacks frontend/)")
	}

	// backend: main projects in, docs does NOT.
	be, _ := repo.ListRefsIn("backend", hasDirIn(t, repo, "backend"))
	if bn := refNames(be); !bn["main"] || bn["docs"] {
		t.Fatalf("backend projection wrong: %v", bn)
	}

	// nested: frontend/ui does NOT exist -> main does not project there.
	nested, _ := repo.ListRefsIn("frontend/ui", hasDirIn(t, repo, "frontend/ui"))
	if nn := refNames(nested); nn["main"] {
		t.Fatal("main must not project into a non-existent nested dir")
	}
}

// A sub-scope ref with a real target is visible only in its scope, even though
// its target revision is a normal root revision.
func TestSubRefWithRealRevision(t *testing.T) {
	repo := newScopedRepo(t)
	snapshotWith(t, repo, "rev-fe", map[string]string{"frontend/a.ts": "x"})
	_ = repo.PutRef(&Ref{Name: "main", Kind: RefBranch, Target: "rev-fe"})
	// A branch pinned to the SAME revision but scoped to frontend.
	_ = repo.PutRef(&Ref{Path: "frontend", Name: "feature-x", Kind: RefBranch, Target: "rev-fe"})

	root, _ := repo.ListRefsIn("", nil)
	if refNames(root)["feature-x"] {
		t.Fatal("scoped ref leaked to root")
	}
	fe, _ := repo.ListRefsIn("frontend", hasDirIn(t, repo, "frontend"))
	fn := refNames(fe)
	if !fn["main"] || !fn["feature-x"] {
		t.Fatalf("frontend should see both: %v", fn)
	}
}

// Paths are normalized: "frontend/", "/frontend", " frontend " all name the
// same scope.
func TestPathNormalization(t *testing.T) {
	repo := newScopedRepo(t)
	_ = repo.PutRef(&Ref{Path: "/frontend/", Name: "b", Kind: RefBranch, Target: "t"})
	for _, q := range []string{"frontend", "/frontend", "frontend/", " frontend "} {
		if got, err := repo.GetRefIn(q, "b"); err != nil || got.Target != "t" {
			t.Fatalf("GetRefIn(%q) = %+v, %v", q, got, err)
		}
	}
}

// Two different REPOSITORIES never share scope state (repo_id is part of the
// key throughout).
func TestScopeRepoIsolation(t *testing.T) {
	cs := newTestCentral(t)
	r1, _ := cs.Create(RepoRef{Namespace: "n", Name: "r1"})
	r2, _ := cs.Create(RepoRef{Namespace: "n", Name: "r2"})
	_ = r1.PutRef(&Ref{Path: "frontend", Name: "main", Kind: RefBranch, Target: "one"})
	_ = r2.PutRef(&Ref{Path: "frontend", Name: "main", Kind: RefBranch, Target: "two"})
	if g, _ := r1.GetRefIn("frontend", "main"); g.Target != "one" {
		t.Fatalf("r1 scoped ref = %v", g)
	}
	if g, _ := r2.GetRefIn("frontend", "main"); g.Target != "two" {
		t.Fatalf("r2 scoped ref = %v", g)
	}
}

// Scale: many scopes don't bleed into each other, and a scope's list is bounded
// by its own subtree.
func TestScopeScale(t *testing.T) {
	repo := newScopedRepo(t)
	const n = 50
	for i := 0; i < n; i++ {
		_ = repo.PutRef(&Ref{Path: fmt.Sprintf("svc-%02d", i), Name: "main", Kind: RefBranch, Target: fmt.Sprintf("t%d", i)})
	}
	_ = repo.PutRef(&Ref{Name: "root-main", Kind: RefBranch, Target: "root"})

	// Root sees ONLY its own ref (no sub-scope leak).
	root, _ := repo.ListRefsIn("", nil)
	if len(root) != 1 || root[0].Name != "root-main" {
		t.Fatalf("root list = %d refs, want 1", len(root))
	}
	// Each sub-scope sees root-main + its own.
	for i := 0; i < n; i++ {
		scope := fmt.Sprintf("svc-%02d", i)
		got, _ := repo.ListRefsIn(scope, func(string) bool { return true })
		if len(got) != 2 {
			t.Fatalf("%s = %d refs, want 2", scope, len(got))
		}
	}
	// ListOwnRefs of each sub-scope is exactly 1.
	own, _ := repo.ListOwnRefs("svc-25")
	if len(own) != 1 || own[0].Target != "t25" {
		t.Fatalf("svc-25 own = %+v", own)
	}
}

// A tag lives in a scope too (the same projection rule).
func TestScopedTags(t *testing.T) {
	repo := newScopedRepo(t)
	_ = repo.PutRef(&Ref{Name: "v1.0", Kind: RefTag, Target: "r"})
	_ = repo.PutRef(&Ref{Path: "frontend", Name: "v1.2-fe", Kind: RefTag, Target: "r"})
	fe, _ := repo.ListRefsIn("frontend", func(string) bool { return true })
	fn := refNames(fe)
	if !fn["v1.0"] || !fn["v1.2-fe"] {
		t.Fatalf("scoped tags wrong: %v", fn)
	}
	if refNames(mustList(t, repo, ""))["v1.2-fe"] {
		t.Fatal("a scoped tag leaked to the root")
	}
}

func mustList(t *testing.T, repo *Repo, path string) []*Ref {
	t.Helper()
	r, err := repo.ListRefsIn(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
