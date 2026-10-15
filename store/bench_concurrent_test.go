package store

import (
	"fmt"
	"sync"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// commitOne writes a single commit to repo (helper for the concurrency benches).
func commitOne(tb testing.TB, repo *Repo, i int) {
	tree := object.NewTree()
	blob := []byte(fmt.Sprintf("c-%d", i))
	tree.Entries["a.txt"] = object.Entry{Name: "a.txt", Kind: object.KindBlob, ID: object.BlobID(blob)}
	treeObj := &object.Object{Kind: object.KindTree, Tree: tree}
	_ = repo.WriteObject(treeObj)
	_ = repo.WriteObject(&object.Object{Kind: object.KindBlob, Blob: blob})
	revID := fmt.Sprintf("r%08d", i)
	snap := &Snapshot{RevisionID: revID, TreeID: treeObj.ID(), Description: "c"}
	snap.RevisionHash = SnapshotHashFor(snap)
	_ = repo.PutSnapshot(snap)
	_ = repo.PutRevision(&Revision{ID: revID, Hash: snap.RevisionHash, Created: now(), ChangedPaths: []string{"a.txt"}})
}

// pgConcurrentStore opens a Postgres store or skips.
func pgConcurrentStore(tb testing.TB) *CentralStore { return pgBenchStore(tb) }

// BenchmarkPGConcurrentDistinctNS is the Postgres counterpart: distinct
// namespaces use separate connections, so they parallelise.
func BenchmarkPGConcurrentDistinctNS(b *testing.B) {
	cs := pgConcurrentStore(b)
	concurrentDistinct(b, cs)
}

// BenchmarkPGConcurrentSameNS serialises on the SAME namespace (one repo lock).
func BenchmarkPGConcurrentSameNS(b *testing.B) {
	cs := pgConcurrentStore(b)
	concurrentSame(b, cs)
}

func concurrentDistinct(b *testing.B, cs *CentralStore) {
	const workers = 8
	repos := make([]*Repo, workers)
	for i := range repos {
		r, err := cs.Create(NSRef{Name: fmt.Sprintf("pgconc-%d-%s", i, randSuffix())})
		if err != nil {
			b.Fatal(err)
		}
		repos[i] = r
	}
	b.ResetTimer()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < b.N/workers; i++ {
				commitOne(b, repos[w], i)
			}
		}(w)
	}
	wg.Wait()
}

func concurrentSame(b *testing.B, cs *CentralStore) {
	repo, _ := cs.Create(NSRef{Name: "pgconc-same-" + randSuffix()})
	var mu sync.Mutex
	b.ResetTimer()
	var wg sync.WaitGroup
	for w := 0; w < 8; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < b.N/8; i++ {
				mu.Lock()
				commitOne(b, repo, i)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
}

// BenchmarkConcurrentDistinctNS commits in parallel to DISTINCT namespaces.
// Distinct namespaces share the global object store but have separate metadata,
// so on a multi-connection backend they should scale; on SQLite the single
// connection serialises them (the DB is the bottleneck, not the app).
func BenchmarkConcurrentDistinctNS(b *testing.B) {
	cs := newBenchCentral(b)
	const workers = 8
	repos := make([]*Repo, workers)
	for i := range repos {
		r, err := cs.Create(NSRef{Name: fmt.Sprintf("conc-%d-%s", i, randSuffix())})
		if err != nil {
			b.Fatal(err)
		}
		repos[i] = r
	}
	b.ResetTimer()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < b.N/workers; i++ {
				commitOne(b, repos[w], i)
			}
		}(w)
	}
	wg.Wait()
}

// BenchmarkConcurrentSameNS commits in parallel to the SAME namespace. Writes
// are serialised by the per-repo lock (VCS semantics: linear history), so this
// measures the lock's overhead, not parallelism.
func BenchmarkConcurrentSameNS(b *testing.B) {
	cs := newBenchCentral(b)
	repo, _ := cs.Create(NSRef{Name: "conc-same-" + randSuffix()})
	const workers = 8
	var mu sync.Mutex
	b.ResetTimer()
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < b.N/workers; i++ {
				mu.Lock()
				commitOne(b, repo, i)
				mu.Unlock()
			}
		}(w)
	}
	wg.Wait()
}
