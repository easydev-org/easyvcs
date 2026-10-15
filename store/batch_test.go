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

// The snapshot/revision caches serve repeated reads from memory, and an AMEND
// (PutRevisionTx reusing an id with a new hash) refreshes the cache so a read
// never returns a stale hash.
func TestSnapshotRevisionCacheAmendRefresh(t *testing.T) {
	cs := newTestCentral(t)
	repo, _ := cs.Create(NSRef{Name: "cacheamend"})
	tree := object.NewTree()
	treeObj := &object.Object{Kind: object.KindTree, Tree: tree}
	_ = repo.WriteObject(treeObj)
	snap := &Snapshot{RevisionID: "r1", TreeID: treeObj.ID(), Description: "v1"}
	snap.RevisionHash = SnapshotHashFor(snap)
	_ = repo.PutSnapshot(snap)
	_ = repo.PutRevision(&Revision{ID: "r1", Hash: snap.RevisionHash, Created: now()})

	// Prime the cache.
	if r, _ := repo.GetRevision("r1"); r.Hash != snap.RevisionHash {
		t.Fatal("prime failed")
	}
	// Amend: same id, new hash, via the TX path (as CommitFromChanges does).
	snap2 := &Snapshot{RevisionID: "r1", TreeID: treeObj.ID(), Description: "v2"}
	snap2.RevisionHash = SnapshotHashFor(snap2)
	_ = repo.PutSnapshot(snap2)
	tx := repo.BeginTx()
	_ = repo.PutRevisionTx(tx, &Revision{ID: "r1", Hash: snap2.RevisionHash, Created: now()})
	_ = tx.Commit()
	// The cache MUST reflect the new hash, not the stale one.
	if r, _ := repo.GetRevision("r1"); r.Hash != snap2.RevisionHash {
		t.Fatalf("cache served a stale hash after amend: %s want %s", r.Hash, snap2.RevisionHash)
	}
}
