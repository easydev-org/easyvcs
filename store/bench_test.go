package store

import (
	"bytes"
	"fmt"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// newBenchCentral is newTestCentral for benchmarks (testing.TB).
func newBenchCentral(tb testing.TB) *CentralStore {
	tb.Helper()
	home := tb.TempDir()
	tb.Setenv("EASYVCS_HOME", home)
	cs, err := OpenDefault()
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() { _ = cs.Close() })
	return cs
}

// benchRepo builds a repo with n commits, each adding one ~1KB file, so the
// store has n revisions/snapshots/objects. Used by the store-level benchmarks.
func benchRepo(tb testing.TB, n int) *Repo {
	tb.Helper()
	cs := newBenchCentral(tb)
	repo, err := cs.Create(NSRef{Name: "bench"})
	if err != nil {
		tb.Fatal(err)
	}
	var parent object.ID
	for i := 0; i < n; i++ {
		payload := bytes.Repeat([]byte{byte('A' + i%26)}, 1024)
		tree := object.NewTree()
		blobID := object.BlobID(payload)
		tree.Entries[fmt.Sprintf("f%04d.txt", i)] = object.Entry{Name: fmt.Sprintf("f%04d.txt", i), Kind: object.KindBlob, ID: blobID}
		treeObj := &object.Object{Kind: object.KindTree, Tree: tree}
		_ = repo.WriteObject(treeObj)
		_ = repo.WriteObject(&object.Object{Kind: object.KindBlob, Blob: payload})
		revID := fmt.Sprintf("rev%05d", i)
		snap := &Snapshot{RevisionID: revID, TreeID: treeObj.ID(), Description: "c"}
		if parent != (object.ID{}) {
			snap.Parents = []object.ID{parent}
		}
		snap.RevisionHash = SnapshotHashFor(snap)
		_ = repo.PutSnapshot(snap)
		_ = repo.PutRevision(&Revision{ID: revID, Hash: snap.RevisionHash, Created: now(), ChangedPaths: []string{fmt.Sprintf("f%04d.txt", i)}})
		parent = snap.RevisionHash
	}
	return repo
}

// BenchmarkCommit measures the write path: one commit per iteration, each
// changing a single file (the common case). It includes the path-index write.
func BenchmarkCommit(b *testing.B) {
	cs := newBenchCentral(b)
	repo, _ := cs.Create(NSRef{Name: "commitbench"})
	var parent object.ID
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tree := object.NewTree()
		blob := []byte(fmt.Sprintf("content-%d", i))
		tree.Entries["a.txt"] = object.Entry{Name: "a.txt", Kind: object.KindBlob, ID: object.BlobID(blob)}
		treeObj := &object.Object{Kind: object.KindTree, Tree: tree}
		_ = repo.WriteObject(treeObj)
		_ = repo.WriteObject(&object.Object{Kind: object.KindBlob, Blob: blob})
		revID := fmt.Sprintf("r%08d", i)
		snap := &Snapshot{RevisionID: revID, TreeID: treeObj.ID(), Description: "c"}
		if parent != (object.ID{}) {
			snap.Parents = []object.ID{parent}
		}
		snap.RevisionHash = SnapshotHashFor(snap)
		_ = repo.PutSnapshot(snap)
		_ = repo.PutRevision(&Revision{ID: revID, Hash: snap.RevisionHash, Created: now(), ChangedPaths: []string{"a.txt"}})
		parent = snap.RevisionHash
	}
}

// BenchmarkRevisionsTouching measures the path index query — the operation that
// makes sub-scope history O(subtree). With n commits the query must stay fast.
func BenchmarkRevisionsTouching(b *testing.B) {
	repo := benchRepo(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ids, err := repo.RevisionsTouching("f1000.txt")
		if err != nil || len(ids) == 0 {
			b.Fatalf("touching: %v %d", err, len(ids))
		}
	}
}

// BenchmarkListRevisions measures listing every revision (the log endpoint's
// data source), to size the N+1 snapshot lookups separately.
func BenchmarkListRevisions(b *testing.B) {
	repo := benchRepo(b, 2000)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := repo.ListRevisions(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkObjectBloom measures building the object Bloom filter (advertise's
// cost). At 2000 objects it must be well under a millisecond.
func BenchmarkObjectBloom(b *testing.B) {
	cs := newBenchCentral(b)
	repo, _ := cs.Create(NSRef{Name: "bloombench"})
	for i := 0; i < 2000; i++ {
		_ = repo.WriteObject(&object.Object{Kind: object.KindBlob, Blob: []byte(fmt.Sprintf("obj-%d", i))})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := cs.ObjectBloom(); err != nil {
			b.Fatal(err)
		}
	}
}
