package revision

import (
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// TestGCRetainsOpenRRSnapshots verifies that a pinned snapshot of an open
// Revision Request is a GC root: even with NO branch pointing at it, its
// snapshot, tree, and blobs must survive a GC pass.
func TestGCRetainsOpenRRSnapshots(t *testing.T) {
	home := t.TempDir()
	t.Setenv("EASYVCS_HOME", home)
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	_ = cs.SetWAL()
	repo, err := cs.Create(store.NSRef{Name: "team" + "/" + "app"})
	if err != nil {
		t.Fatal(err)
	}
	ws := NewWorkspace(repo)

	// main: a base commit.
	base := commitOne(t, ws, nil, "README.md", "base\n", "base")
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}

	// A request-only snapshot (no branch points at it): a distinct tree/blob.
	blobID, err := ws.WriteBlob([]byte("request-only content\n"))
	if err != nil {
		t.Fatal(err)
	}
	tree := object.NewTree()
	tree.Entries["only-in-rr.txt"] = object.Entry{Name: "only-in-rr.txt", Kind: object.KindBlob, ID: blobID}
	treeID, err := ws.WriteTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	src, _, err := ws.Commit(CommitParams{
		Parents: []object.ID{base.RevisionHash}, TreeID: treeID, Description: "rr-only",
		Author: store.Author{Name: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Register an RR pinning that snapshot (no branch behind it).
	if _, err := cs.CreateRevisionRequest(repo.NamespaceID(), &store.RevisionRequest{
		SourceSnapshot: src.RevisionHash.String(),
		BaseSnapshot:   base.RevisionHash.String(), TargetBranch: "main",
		State: store.RROpen, Title: "rr-only",
	}); err != nil {
		t.Fatal(err)
	}

	// GC (dry-run then real).
	if _, err := GCRun(cs, GCOptions{DryRun: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := GCRun(cs, GCOptions{}); err != nil {
		t.Fatal(err)
	}

	// The pinned snapshot, its tree, and its blob must still exist.
	if _, err := repo.GetSnapshot(src.RevisionHash); err != nil {
		t.Fatalf("pinned RR snapshot was pruned: %v", err)
	}
	if _, err := repo.ReadObject(treeID); err != nil {
		t.Fatalf("pinned RR tree was pruned: %v", err)
	}
	if _, err := repo.ReadObject(blobID); err != nil {
		t.Fatalf("pinned RR blob was pruned: %v", err)
	}
}

func commitOne(t *testing.T, ws *Workspace, parents []object.ID, name, content, msg string) *store.Snapshot {
	t.Helper()
	blob, err := ws.WriteBlob([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	tree := object.NewTree()
	tree.Entries[name] = object.Entry{Name: name, Kind: object.KindBlob, ID: blob}
	treeID, err := ws.WriteTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	snap, _, err := ws.Commit(CommitParams{
		Parents: parents, TreeID: treeID, Description: msg,
		Author: store.Author{Name: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return snap
}
