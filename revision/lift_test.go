package revision

import (
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// Lift = the sub-repo "write" path (docs/subrepo-scopes.md §5): a change in a
// subdirectory commits to the ROOT revision, touching only that subtree.

// buildRoot commits a root tree {README.md, frontend/app.ts, backend/server.go}
// on `main` and returns the workspace + the root tip revision.
func buildRoot(t *testing.T, ws *Workspace) *store.Revision {
	t.Helper()
	root := object.NewTree()
	root.Entries["README.md"] = object.Entry{Name: "README.md", Kind: object.KindBlob, ID: mustWriteBlob(ws, []byte("root\n"))}
	fe := object.NewTree()
	fe.Entries["app.ts"] = object.Entry{Name: "app.ts", Kind: object.KindBlob, ID: mustWriteBlob(ws, []byte("v1\n"))}
	feID, _ := ws.WriteTree(fe)
	root.Entries["frontend"] = object.Entry{Name: "frontend", Kind: object.KindTree, ID: feID}
	be := object.NewTree()
	be.Entries["server.go"] = object.Entry{Name: "server.go", Kind: object.KindBlob, ID: mustWriteBlob(ws, []byte("s1\n"))}
	beID, _ := ws.WriteTree(be)
	root.Entries["backend"] = object.Entry{Name: "backend", Kind: object.KindTree, ID: beID}
	rootID, _ := ws.WriteTree(root)
	_, rev, err := ws.Commit(CommitParams{TreeID: rootID, Description: "root", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("main", store.RefBranch, rev.ID); err != nil {
		t.Fatal(err)
	}
	return rev
}

func treeEntry(t *testing.T, ws *Workspace, treeID object.ID, path string) object.Entry {
	t.Helper()
	e, err := ws.FindEntry(ws.MustTree(treeID), path)
	if err != nil {
		t.Fatalf("entry %q not found: %v", path, err)
	}
	return e
}

func blobAt(t *testing.T, ws *Workspace, treeID object.ID, path string) string {
	t.Helper()
	e := treeEntry(t, ws, treeID, path)
	b, err := ws.ReadBlob(e.ID)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Lifting a frontend change creates a NEW root revision that:
//   - changes frontend/app.ts,
//   - leaves backend/server.go and README.md byte-identical,
//   - moves the root `main` ref,
//   - records ONLY frontend/app.ts as changed (path index is O(subtree)).
func TestLiftChangesOnlySubtree(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, _ string) {
		buildRoot(t, ws)
		_, lifted, err := ws.Lift("main", "frontend", []FileChangeSpec{
			{Path: "app.ts", Content: []byte("v2-lifted\n")},
		}, "edit frontend", store.Author{Name: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		ref, _ := ws.GetRef("main")
		if ref.Target != lifted.ID {
			t.Fatalf("main = %s, want lifted %s", ref.Target, lifted.ID)
		}
		snap, _ := ws.GetSnapshot(lifted.Hash)
		if got := blobAt(t, ws, snap.TreeID, "frontend/app.ts"); got != "v2-lifted\n" {
			t.Fatalf("frontend/app.ts = %q", got)
		}
		if got := blobAt(t, ws, snap.TreeID, "backend/server.go"); got != "s1\n" {
			t.Fatalf("backend/server.go changed: %q", got)
		}
		if got := blobAt(t, ws, snap.TreeID, "README.md"); got != "root\n" {
			t.Fatalf("README.md changed: %q", got)
		}
		// The lifted revision's ChangedPaths are the paths UNDER the scope.
		if len(lifted.ChangedPaths) != 1 || lifted.ChangedPaths[0] != "frontend/app.ts" {
			t.Fatalf("lifted ChangedPaths = %v, want [frontend/app.ts]", lifted.ChangedPaths)
		}
	})
}

// Lifting into a scope whose dir does NOT exist yet creates it.
func TestLiftCreatesMissingSubtree(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, _ string) {
		buildRoot(t, ws)
		_, lifted, err := ws.Lift("main", "docs", []FileChangeSpec{
			{Path: "guide.md", Content: []byte("hello\n")},
		}, "add docs", store.Author{Name: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		snap, _ := ws.GetSnapshot(lifted.Hash)
		if got := blobAt(t, ws, snap.TreeID, "docs/guide.md"); got != "hello\n" {
			t.Fatalf("docs/guide.md = %q", got)
		}
		if got := blobAt(t, ws, snap.TreeID, "frontend/app.ts"); got != "v1\n" {
			t.Fatalf("frontend/app.ts changed: %q", got)
		}
	})
}

// Lifting a NESTED scope (frontend/ui) only replaces that subtree.
func TestLiftNestedScope(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, _ string) {
		ui := object.NewTree()
		ui.Entries["button.tsx"] = object.Entry{Name: "button.tsx", Kind: object.KindBlob, ID: mustWriteBlob(ws, []byte("b1\n"))}
		uiID, _ := ws.WriteTree(ui)
		fe := object.NewTree()
		fe.Entries["app.ts"] = object.Entry{Name: "app.ts", Kind: object.KindBlob, ID: mustWriteBlob(ws, []byte("v1\n"))}
		fe.Entries["ui"] = object.Entry{Name: "ui", Kind: object.KindTree, ID: uiID}
		feID, _ := ws.WriteTree(fe)
		root := object.NewTree()
		root.Entries["frontend"] = object.Entry{Name: "frontend", Kind: object.KindTree, ID: feID}
		rootID, _ := ws.WriteTree(root)
		_, rev, _ := ws.Commit(CommitParams{TreeID: rootID, Description: "root", Author: store.Author{Name: "t"}})
		_, _ = ws.SetRef("main", store.RefBranch, rev.ID)

		_, lifted, err := ws.Lift("main", "frontend/ui", []FileChangeSpec{
			{Path: "button.tsx", Content: []byte("b2\n")},
		}, "edit ui", store.Author{Name: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		snap, _ := ws.GetSnapshot(lifted.Hash)
		if got := blobAt(t, ws, snap.TreeID, "frontend/ui/button.tsx"); got != "b2\n" {
			t.Fatalf("frontend/ui/button.tsx = %q", got)
		}
		if got := blobAt(t, ws, snap.TreeID, "frontend/app.ts"); got != "v1\n" {
			t.Fatalf("frontend/app.ts changed: %q", got)
		}
	})
}

// Deleting a file inside a scope lifts a delete of only that file.
func TestLiftDeleteInScope(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, _ string) {
		buildRoot(t, ws)
		_, lifted, err := ws.Lift("main", "frontend", []FileChangeSpec{
			{Path: "app.ts", Delete: true},
		}, "remove app.ts", store.Author{Name: "dev"})
		if err != nil {
			t.Fatal(err)
		}
		snap, _ := ws.GetSnapshot(lifted.Hash)
		if _, err := ws.FindEntry(ws.MustTree(snap.TreeID), "frontend/app.ts"); err == nil {
			t.Fatal("frontend/app.ts should be gone")
		}
		if got := blobAt(t, ws, snap.TreeID, "backend/server.go"); got != "s1\n" {
			t.Fatalf("backend changed: %q", got)
		}
	})
}

// A root-scope lift is rejected (the root uses the normal commit path).
func TestLiftRootRejected(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, _ string) {
		buildRoot(t, ws)
		if _, _, err := ws.Lift("main", "", []FileChangeSpec{{Path: "x", Content: []byte("y")}}, "noop", store.Author{Name: "d"}); err == nil {
			t.Fatal("lift with an empty scope should be rejected")
		}
	})
}
