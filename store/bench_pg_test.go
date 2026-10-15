package store

import (
	"fmt"
	"os"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// pgBenchStore opens a fresh Postgres-backed store on the test DSN, or skips.
func pgBenchStore(tb testing.TB) *CentralStore {
	tb.Helper()
	dsn := os.Getenv("EASYVCS_TEST_PG_DSN")
	if dsn == "" {
		tb.Skip("EASYVCS_TEST_PG_DSN not set; skipping postgres benchmarks")
	}
	cs, err := OpenDriver(DriverConfig{Kind: KindPostgres, DSN: dsn})
	if err != nil {
		tb.Fatalf("open postgres: %v", err)
	}
	tb.Cleanup(func() { _ = cs.Close() })
	return cs
}

// pgBenchRepo creates a uniquely-named namespace so parallel/repeat runs do not
// collide on the shared Postgres instance.
func pgBenchRepo(tb testing.TB, cs *CentralStore) *Repo {
	tb.Helper()
	name := "bench-" + randSuffix()
	repo, err := cs.Create(NSRef{Name: name})
	if err != nil {
		tb.Fatal(err)
	}
	return repo
}

// BenchmarkPGCommitSyncIndex / Async measure the commit path on POSTGRES with
// the index written inline vs deferred. Unlike SQLite (single connection), the
// async worker runs on its OWN connection, so the commit no longer waits for the
// index write at all.
func BenchmarkPGCommitSyncIndex(b *testing.B)  { pgCommit(b, false) }
func BenchmarkPGCommitAsyncIndex(b *testing.B) { pgCommit(b, true) }

func pgCommit(b *testing.B, async bool) {
	cs := pgBenchStore(b)
	if async {
		cs.WithAsyncIndex()
	}
	repo := pgBenchRepo(b, cs)
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
		_ = repo.PutRevision(&Revision{ID: revID, Hash: snap.RevisionHash, Created: now(), ChangedPaths: []string{"pkg/sub/a.txt"}})
		parent = snap.RevisionHash
	}
}

// BenchmarkPGRevisionsTouching measures the path-index query on Postgres.
func BenchmarkPGRevisionsTouching(b *testing.B) {
	cs := pgBenchStore(b)
	repo := pgBenchRepo(b, cs)
	var parent object.ID
	for i := 0; i < 2000; i++ {
		revID := fmt.Sprintf("rev%05d", i)
		snap := &Snapshot{RevisionID: revID, TreeID: object.BlobID([]byte(fmt.Sprintf("t%d", i))), Description: "c"}
		if parent != (object.ID{}) {
			snap.Parents = []object.ID{parent}
		}
		snap.RevisionHash = SnapshotHashFor(snap)
		_ = repo.PutSnapshot(snap)
		// DISTINCT path per revision (matches the SQLite bench): the query then
		// returns ONE row, isolating the index lookup from result-set size.
		_ = repo.PutRevision(&Revision{ID: revID, Hash: snap.RevisionHash, Created: now(), ChangedPaths: []string{fmt.Sprintf("f%04d.txt", i)}})
		parent = snap.RevisionHash
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ids, err := repo.RevisionsTouching("f1000.txt")
		if err != nil || len(ids) == 0 {
			b.Fatalf("touching: %v %d", err, len(ids))
		}
	}
}
