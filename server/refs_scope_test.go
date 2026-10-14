package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// Sub-repo scope refs over the hosting API (docs/subrepo-scopes.md), end to end
// at the HTTP layer: build a repo whose main tree contains frontend/ + backend/,
// then exercise ?path= on refs/branches, and creating/deleting a scoped branch.

// hostingTestServerWithDirs builds team/app whose `main` tip contains the given
// top-level dirs (each with one file).
func hostingTestServerWithDirs(t *testing.T, dirs ...string) (*Server, *http.ServeMux, string, *store.Repo) {
	t.Helper()
	s := newTestServer(t, "alice-token")
	u, err := s.cs.GetUserByUsername("tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cs.EnsureNamespace(u.ID, "team"); err != nil {
		t.Fatal(err)
	}
	repo, err := s.cs.Create(store.RepoRef{Owner: u.ID, Namespace: "team", Name: "app"})
	if err != nil {
		t.Fatal(err)
	}
	makePublic(t, repo)
	ws := revision.NewWorkspace(repo)

	root := object.NewTree()
	// Top-level README.
	rb, _ := ws.WriteBlob([]byte("root\n"))
	root.Entries["README.md"] = object.Entry{Name: "README.md", Kind: object.KindBlob, ID: rb}
	for _, d := range dirs {
		fb, _ := ws.WriteBlob([]byte(d + " content\n"))
		sub := object.NewTree()
		sub.Entries["file.txt"] = object.Entry{Name: "file.txt", Kind: object.KindBlob, ID: fb}
		sid, _ := ws.WriteTree(sub)
		root.Entries[d] = object.Entry{Name: d, Kind: object.KindTree, ID: sid}
	}
	tid, _ := ws.WriteTree(root)
	_, rev, err := ws.Commit(revision.CommitParams{TreeID: tid, Description: "base", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("main", store.RefBranch, rev.ID); err != nil {
		t.Fatal(err)
	}
	return s, s.Router(), "alice-token", repo
}

func refNamesIn(t *testing.T, m map[string]any, key string) map[string]bool {
	t.Helper()
	arr, _ := m[key].([]any)
	out := map[string]bool{}
	for _, e := range arr {
		em, _ := e.(map[string]any)
		if n, ok := em["name"].(string); ok {
			out[n] = true
		}
	}
	return out
}

// main (a root branch) projects into frontend, so ?path=frontend lists it.
func TestRefsScopeProjectionAPI(t *testing.T) {
	_, mux, tok, _ := hostingTestServerWithDirs(t, "frontend", "backend")

	// Root refs.
	rec := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/refs", tok, nil)
	if rec.Code != 200 || !refNamesIn(t, decode(t, rec), "refs")["main"] {
		t.Fatalf("root refs = %d %s", rec.Code, rec.Body.String())
	}
	// frontend: main projects in.
	rec = hreq(mux, http.MethodGet, "/api/v1/repos/team/app/refs?path=frontend", tok, nil)
	n := refNamesIn(t, decode(t, rec), "refs")
	if !n["main"] {
		t.Fatalf("main should project into frontend: %v", n)
	}
}

// A root branch whose tip lacks the dir does NOT project there.
func TestRefsScopeNoProjectionWhenMissing(t *testing.T) {
	_, mux, tok, _ := hostingTestServerWithDirs(t, "frontend") // no backend/
	rec := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/refs?path=backend", tok, nil)
	n := refNamesIn(t, decode(t, rec), "refs")
	if n["main"] {
		t.Fatalf("main must not project into a non-existent dir: %v", n)
	}
}

// Creating a branch scoped to frontend keeps it out of the root listing.
func TestCreateScopedBranchAPI(t *testing.T) {
	_, mux, tok, _ := hostingTestServerWithDirs(t, "frontend", "backend")

	// Create a branch under frontend (path in the body).
	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/branches", tok,
		map[string]any{"name": "feature-x", "from": "main", "path": "frontend"})
	if rec.Code != 200 {
		t.Fatalf("create scoped branch = %d %s", rec.Code, rec.Body.String())
	}
	// The root listing does NOT show it.
	rec = hreq(mux, http.MethodGet, "/api/v1/repos/team/app/branches", tok, nil)
	if refNamesIn(t, decode(t, rec), "branches")["feature-x"] {
		t.Fatal("a scoped branch leaked into the root listing")
	}
	// The frontend listing shows main (projected) + feature-x (own).
	rec = hreq(mux, http.MethodGet, "/api/v1/repos/team/app/branches?path=frontend", tok, nil)
	n := refNamesIn(t, decode(t, rec), "branches")
	if !n["main"] || !n["feature-x"] {
		t.Fatalf("frontend branches = %v, want main + feature-x", n)
	}
	// A sibling scope does NOT see feature-x.
	rec = hreq(mux, http.MethodGet, "/api/v1/repos/team/app/branches?path=backend", tok, nil)
	if refNamesIn(t, decode(t, rec), "branches")["feature-x"] {
		t.Fatal("a scoped branch leaked into a sibling scope")
	}
}

// Deleting a scoped branch with ?path= removes only that scope's ref.
func TestDeleteScopedBranchAPI(t *testing.T) {
	_, mux, tok, _ := hostingTestServerWithDirs(t, "frontend", "backend")
	// Same branch name in two scopes.
	for _, p := range []string{"frontend", "backend"} {
		rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/branches", tok,
			map[string]any{"name": "shared", "from": "main", "path": p})
		if rec.Code != 200 {
			t.Fatalf("create %s: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	rec := hreq(mux, http.MethodDelete, "/api/v1/repos/team/app/branches/shared?path=frontend", tok, nil)
	if rec.Code != 200 {
		t.Fatalf("delete scoped branch = %d %s", rec.Code, rec.Body.String())
	}
	// frontend lost it; backend kept it.
	rec = hreq(mux, http.MethodGet, "/api/v1/repos/team/app/branches?path=frontend", tok, nil)
	if refNamesIn(t, decode(t, rec), "branches")["shared"] {
		t.Fatal("frontend branch was not deleted")
	}
	rec = hreq(mux, http.MethodGet, "/api/v1/repos/team/app/branches?path=backend", tok, nil)
	if !refNamesIn(t, decode(t, rec), "branches")["shared"] {
		t.Fatal("backend branch was wrongly deleted")
	}
}

// refView surfaces the scope path when non-empty.
func TestRefViewIncludesPath(t *testing.T) {
	_, mux, tok, _ := hostingTestServerWithDirs(t, "frontend")
	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/branches", tok,
		map[string]any{"name": "fe", "from": "main", "path": "frontend"})
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if m["path"] != "frontend" {
		t.Fatalf("refView path = %v, want frontend (%s)", m["path"], rec.Body.String())
	}
}

// A commit with ?path=/path in the body LIFTS the (scope-relative) file ops onto
// the root branch: the new root revision touches only that subtree.
func TestCommitWithPathLifts(t *testing.T) {
	_, mux, tok, repo := hostingTestServerWithDirs(t, "frontend", "backend")

	// A non-protected branch to lift onto (main is protected).
	if rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/branches", tok,
		map[string]any{"name": "feature", "from": "main"}); rec.Code != 200 {
		t.Fatalf("create feature = %d %s", rec.Code, rec.Body.String())
	}
	// Commit a change to frontend/app.ts (path-relative), on feature.
	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/commit", tok,
		map[string]any{
			"branch":  "feature",
			"message": "edit frontend",
			"path":    "frontend",
			"files":   []map[string]any{{"path": "app.ts", "operation": "create", "content": "v2\n"}},
		})
	if rec.Code != 200 {
		t.Fatalf("lift commit = %d %s", rec.Code, rec.Body.String())
	}
	resp := decode(t, rec)
	if resp["path"] != "frontend" {
		t.Fatalf("response path = %v", resp["path"])
	}
	// The root main tip now has frontend/app.ts; backend is untouched.
	ws := revision.NewWorkspace(repo)
	ref, err := ws.GetRef("feature")
	if err != nil {
		t.Fatal(err)
	}
	rev, _ := repo.GetRevision(ref.Target)
	snap, _ := repo.GetSnapshot(rev.Hash)
	if _, err := ws.FindEntry(ws.MustTree(snap.TreeID), "frontend/app.ts"); err != nil {
		t.Fatal("frontend/app.ts should exist after lift")
	}
	if _, err := ws.FindEntry(ws.MustTree(snap.TreeID), "backend/file.txt"); err != nil {
		t.Fatal("backend/file.txt must be preserved by the lift")
	}
	// The lifted revision changed ONLY the frontend path.
	if len(rev.ChangedPaths) != 1 || rev.ChangedPaths[0] != "frontend/app.ts" {
		t.Fatalf("lifted ChangedPaths = %v", rev.ChangedPaths)
	}
}

// A root commit (no path) still works exactly as before.
func TestCommitWithoutPathUnchanged(t *testing.T) {
	_, mux, tok, _ := hostingTestServerWithDirs(t, "frontend")
	if rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/branches", tok,
		map[string]any{"name": "feature", "from": "main"}); rec.Code != 200 {
		t.Fatalf("create feature = %d %s", rec.Code, rec.Body.String())
	}
	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/commit", tok,
		map[string]any{
			"branch":  "feature",
			"message": "root edit",
			"files":   []map[string]any{{"path": "top.txt", "operation": "create", "content": "x\n"}},
		})
	if rec.Code != 200 {
		t.Fatalf("root commit = %d %s", rec.Code, rec.Body.String())
	}
}
