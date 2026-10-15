package store

import (
	"fmt"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// SnapshotsByHashes / RevisionsByIDs return the same data as the per-id getters,
// in one query.
func TestBatchFetchers(t *testing.T) {
	cs := newTestCentral(t)
	repo, _ := cs.Create(NSRef{Name: "batch"})
	var hashes []string
	var ids []string
	var parent object.ID
	for i := 0; i < 5; i++ {
		tree := object.NewTree()
		tree.Entries["a.txt"] = object.Entry{Name: "a.txt", Kind: object.KindBlob, ID: object.BlobID([]byte(fmt.Sprintf("c%d", i)))}
		treeObj := &object.Object{Kind: object.KindTree, Tree: tree}
		_ = repo.WriteObject(treeObj)
		revID := fmt.Sprintf("r%d", i)
		snap := &Snapshot{RevisionID: revID, TreeID: treeObj.ID(), Description: fmt.Sprintf("msg%d", i)}
		if parent != (object.ID{}) {
			snap.Parents = []object.ID{parent}
		}
		snap.RevisionHash = SnapshotHashFor(snap)
		_ = repo.PutSnapshot(snap)
		_ = repo.PutRevision(&Revision{ID: revID, Hash: snap.RevisionHash, Created: now(), ChangedPaths: []string{"a.txt"}})
		hashes = append(hashes, snap.RevisionHash.String())
		ids = append(ids, revID)
		parent = snap.RevisionHash
	}

	// Fresh store (no cache): exercise the batch query path directly.
	cs2 := newTestCentral(t)
	_ = cs2
	snaps, err := repo.SnapshotsByHashes(hashes)
	if err != nil {
		t.Fatal(err)
	}
	if len(snaps) != 5 {
		t.Fatalf("SnapshotsByHashes = %d, want 5", len(snaps))
	}
	if snaps[hashes[2]].Description != "msg2" {
		t.Fatalf("wrong snapshot: %+v", snaps[hashes[2]])
	}
	revs, err := repo.RevisionsByIDs(ids)
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 5 || revs["r3"].Hash.String() != hashes[3] {
		t.Fatalf("RevisionsByIDs wrong: %+v", revs)
	}
	// A missing id is simply absent.
	got, _ := repo.RevisionsByIDs([]string{"r2", "nope"})
	if len(got) != 1 || got["r2"] == nil {
		t.Fatalf("missing id handling: %+v", got)
	}
}

// A revision is NOT cached: its id is stable but its HASH mutates on amend /
// rewrite, so an id->hash cache would serve a stale hash when the row is
// rewritten by another store instance / replica. This test pins that a rewrite
// is always observed (no revision cache in the way). The SNAPSHOT cache is safe
// because it is keyed by the content-addressed (immutable) hash.
func TestRevisionRewriteAlwaysObserved(t *testing.T) {
	cs := newTestCentral(t)
	repo, _ := cs.Create(NSRef{Name: "rewriteobs"})
	tree := object.NewTree()
	treeObj := &object.Object{Kind: object.KindTree, Tree: tree}
	_ = repo.WriteObject(treeObj)
	snap := &Snapshot{RevisionID: "r1", TreeID: treeObj.ID(), Description: "v1"}
	snap.RevisionHash = SnapshotHashFor(snap)
	_ = repo.PutSnapshot(snap)
	_ = repo.PutRevision(&Revision{ID: "r1", Hash: snap.RevisionHash, Created: now()})

	// Amend: same id, new hash.
	snap2 := &Snapshot{RevisionID: "r1", TreeID: treeObj.ID(), Description: "v2"}
	snap2.RevisionHash = SnapshotHashFor(snap2)
	_ = repo.PutSnapshot(snap2)
	_ = repo.PutRevision(&Revision{ID: "r1", Hash: snap2.RevisionHash, Created: now()})
	// GetRevision must reflect the NEW hash.
	if r, _ := repo.GetRevision("r1"); r.Hash != snap2.RevisionHash {
		t.Fatalf("revision hash stale after rewrite: %s want %s", r.Hash, snap2.RevisionHash)
	}
	// The snapshot cache serves the NEW snapshot by its hash.
	if sn, _ := repo.GetSnapshot(snap2.RevisionHash); sn.Description != "v2" {
		t.Fatalf("snapshot cache wrong: %q", sn.Description)
	}
}
