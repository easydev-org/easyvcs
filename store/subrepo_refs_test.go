package store

import (
	"testing"
)

// Scope-aware refs (docs/subrepo-scopes.md). All tests run against an in-memory
// SQLite store — the projection rule is pure store-layer data logic, so it is
// exercised here rather than end-to-end.

func newScopedRepo(t *testing.T) *Repo {
	t.Helper()
	cs := newTestCentral(t)
	repo, err := cs.Create(NSRef{Name: "n-r"})
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

// A ref created without a Path is a ROOT ref (back-compat with the pre-scope
// API): PutRef/GetRef/DeleteRef keep working with "".
func TestRootRefBackCompat(t *testing.T) {
	repo := newScopedRepo(t)
	if err := repo.PutRef(&Ref{Name: "main", Kind: RefBranch, Target: "r1"}); err != nil {
		t.Fatal(err)
	}
	got, err := repo.GetRef("main")
	if err != nil || got.Target != "r1" || got.Path != "" {
		t.Fatalf("root ref = %+v, %v", got, err)
	}
	// The scoped accessor sees the same row.
	if g, err := repo.GetRefIn("", "main"); err != nil || g.Target != "r1" {
		t.Fatalf("GetRefIn(\"\") = %+v, %v", g, err)
	}
}

// Two scopes can each define a ref of the SAME name without collision
// (PK is (repo, path, name)).
func TestSameNameDifferentScopes(t *testing.T) {
	repo := newScopedRepo(t)
	for _, tc := range []struct{ path, target string }{
		{"", "root-tip"},
		{"frontend", "fe-tip"},
		{"backend", "be-tip"},
	} {
		if err := repo.PutRef(&Ref{Path: tc.path, Name: "main", Kind: RefBranch, Target: tc.target}); err != nil {
			t.Fatalf("put %q: %v", tc.path, err)
		}
	}
	for _, tc := range []struct{ path, target string }{
		{"", "root-tip"},
		{"frontend", "fe-tip"},
		{"backend", "be-tip"},
	} {
		got, err := repo.GetRefIn(tc.path, "main")
		if err != nil || got.Target != tc.target {
			t.Fatalf("get %q = %+v, %v; want %s", tc.path, got, err, tc.target)
		}
	}
}

// ListOwnRefs lists only the refs DEFINED at a scope (no projection).
func TestListOwnRefs(t *testing.T) {
	repo := newScopedRepo(t)
	_ = repo.PutRef(&Ref{Name: "main", Kind: RefBranch, Target: "t"})
	_ = repo.PutRef(&Ref{Name: "v1", Kind: RefTag, Target: "t"})
	_ = repo.PutRef(&Ref{Path: "frontend", Name: "feature-x", Kind: RefBranch, Target: "f"})
	_ = repo.PutRef(&Ref{Path: "frontend/ui", Name: "deep", Kind: RefBranch, Target: "d"})

	root, err := repo.ListOwnRefs("")
	if err != nil || len(root) != 2 {
		t.Fatalf("root own refs = %d, want 2 (%v)", len(root), err)
	}
	fe, err := repo.ListOwnRefs("frontend")
	if err != nil || len(fe) != 1 || fe[0].Name != "feature-x" {
		t.Fatalf("frontend own refs = %+v, %v", fe, err)
	}
	// ListOwnRefs("frontend") does NOT include the deeper "frontend/ui" ref.
	for _, r := range fe {
		if r.Name == "deep" {
			t.Fatal("ListOwnRefs leaked a deeper-scope ref")
		}
	}
}

// ── projection rule (docs/subrepo-scopes.md §4) ──────────────────────────────

// All root refs project into a sub-scope whose tip contains the dir; refs whose
// tip lacks the dir do NOT project.
func TestProjectionRootRefsDown(t *testing.T) {
	repo := newScopedRepo(t)
	_ = repo.PutRef(&Ref{Name: "main", Kind: RefBranch, Target: "has-fe"})
	_ = repo.PutRef(&Ref{Name: "docs-only", Kind: RefBranch, Target: "no-fe"})

	// hasDir reports whether the dir "frontend" exists at the target.
	hasFe := func(target string) bool { return target == "has-fe" }
	got, err := repo.ListRefsIn("frontend", hasFe)
	if err != nil {
		t.Fatal(err)
	}
	names := refNames(got)
	if !names["main"] {
		t.Fatal("root 'main' should project into frontend (its tip has the dir)")
	}
	if names["docs-only"] {
		t.Fatal("root 'docs-only' should NOT project (its tip lacks the dir)")
	}
}

// The root scope sees every root ref (projection is a no-op at "").
func TestProjectionRootScope(t *testing.T) {
	repo := newScopedRepo(t)
	_ = repo.PutRef(&Ref{Name: "main", Kind: RefBranch, Target: "a"})
	_ = repo.PutRef(&Ref{Name: "dev", Kind: RefBranch, Target: "b"})
	got, err := repo.ListRefsIn("", nil) // root: hasDir irrelevant
	if err != nil || len(got) != 2 {
		t.Fatalf("root scope refs = %d, want 2 (%v)", len(got), err)
	}
}

// Sub-scope refs DO NOT project UP to the root, nor to a SIBLING scope.
func TestProjectionSubRefsStayDown(t *testing.T) {
	repo := newScopedRepo(t)
	_ = repo.PutRef(&Ref{Name: "main", Kind: RefBranch, Target: "root"})
	_ = repo.PutRef(&Ref{Path: "frontend", Name: "feature-x", Kind: RefBranch, Target: "fe"})
	_ = repo.PutRef(&Ref{Path: "backend", Name: "hotfix", Kind: RefBranch, Target: "be"})

	// Root: only the root's own ref.
	root, _ := repo.ListRefsIn("", nil)
	if n := refNames(root); n["feature-x"] || n["hotfix"] {
		t.Fatalf("a sub-scope ref leaked UP to the root: %v", n)
	}
	// frontend: sees root main + its own feature-x, NOT backend's hotfix.
	fe, _ := repo.ListRefsIn("frontend", func(string) bool { return true })
	fn := refNames(fe)
	if !fn["main"] || !fn["feature-x"] {
		t.Fatalf("frontend should see main + feature-x: %v", fn)
	}
	if fn["hotfix"] {
		t.Fatal("a sibling scope's ref leaked into frontend")
	}
	// backend: sees root main + its own hotfix, NOT frontend's feature-x.
	be, _ := repo.ListRefsIn("backend", func(string) bool { return true })
	bn := refNames(be)
	if !bn["main"] || !bn["hotfix"] {
		t.Fatalf("backend should see main + hotfix: %v", bn)
	}
	if bn["feature-x"] {
		t.Fatal("a sibling scope's ref leaked into backend")
	}
}

// A sub-scope Q sees refs defined at Q AND at deeper scopes (Q/...), but not at
// ancestors of Q.
func TestProjectionNestedScopes(t *testing.T) {
	repo := newScopedRepo(t)
	_ = repo.PutRef(&Ref{Path: "frontend", Name: "fe-main", Kind: RefBranch, Target: "a"})
	_ = repo.PutRef(&Ref{Path: "frontend/ui", Name: "ui-main", Kind: RefBranch, Target: "b"})

	// At frontend: sees fe-main (own) + ui-main (deeper).
	fe, _ := repo.ListRefsIn("frontend", nil)
	fn := refNames(fe)
	if !fn["fe-main"] || !fn["ui-main"] {
		t.Fatalf("frontend should see its own + deeper refs: %v", fn)
	}
	// At frontend/ui: sees ui-main (own) + fe-main? NO — fe-main is an ANCESTOR
	// scope, which does not project down. Only '' (root) projects down.
	ui, _ := repo.ListRefsIn("frontend/ui", func(string) bool { return false })
	un := refNames(ui)
	if !un["ui-main"] {
		t.Fatal("frontend/ui should see its own ui-main")
	}
	if un["fe-main"] {
		t.Fatal("an ANCESTOR-scope ref (frontend) must not project into frontend/ui")
	}
}

// Deleting a ref is scope-precise: deleting the sub-scope ref never touches the
// same-named root ref, and vice versa.
func TestDeleteIsScopePrecise(t *testing.T) {
	repo := newScopedRepo(t)
	_ = repo.PutRef(&Ref{Name: "main", Kind: RefBranch, Target: "root"})
	_ = repo.PutRef(&Ref{Path: "frontend", Name: "main", Kind: RefBranch, Target: "fe"})

	if err := repo.DeleteRefIn("frontend", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetRefIn("frontend", "main"); err == nil {
		t.Fatal("sub-scope ref should be gone")
	}
	if got, err := repo.GetRef("main"); err != nil || got.Target != "root" {
		t.Fatalf("root ref must be untouched: %+v, %v", got, err)
	}
	// Deleting a missing sub-scope ref reports not-found.
	if err := repo.DeleteRefIn("frontend", "main"); err == nil {
		t.Fatal("deleting a missing ref should report not-found")
	}
}

// PutRef at the same (scope, name) is an upsert (a branch moves).
func TestPutRefUpsert(t *testing.T) {
	repo := newScopedRepo(t)
	_ = repo.PutRef(&Ref{Path: "frontend", Name: "main", Kind: RefBranch, Target: "v1"})
	_ = repo.PutRef(&Ref{Path: "frontend", Name: "main", Kind: RefBranch, Target: "v2"})
	got, _ := repo.GetRefIn("frontend", "main")
	if got.Target != "v2" {
		t.Fatalf("upsert target = %s, want v2", got.Target)
	}
	all, _ := repo.ListOwnRefs("frontend")
	if len(all) != 1 {
		t.Fatalf("upsert created a duplicate row: %d", len(all))
	}
}

// refNames turns a ref slice into a name set.
func refNames(refs []*Ref) map[string]bool {
	m := make(map[string]bool, len(refs))
	for _, r := range refs {
		m[r.Name] = true
	}
	return m
}
