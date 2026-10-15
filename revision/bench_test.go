package revision

import (
	"fmt"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// BenchmarkCommitFromChanges measures the REAL commit path (the one the CLI and
// the server /commit use): collect blobs + rewrite the tree + write the batch in
// one transaction. Each iteration changes ONE file on top of the previous tip.
func BenchmarkCommitFromChanges(b *testing.B) {
	home := b.TempDir()
	b.Setenv("EASYVCS_HOME", home)
	cs, err := store.OpenDefault()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cs.Close() })
	repo, _ := cs.Create(store.NSRef{Name: "commitbench"})
	ws := NewWorkspace(repo)

	var parent object.ID
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap, _, err := ws.CommitFromChanges(parent,
			[]FileChangeSpec{{Path: "a.txt", Content: []byte(fmt.Sprintf("content-%d", i))}},
			"c", store.Author{Name: "t"}, "")
		if err != nil {
			b.Fatal(err)
		}
		parent = snap.RevisionHash
	}
}

// BenchmarkCommitFromChangesManyFiles measures a commit touching N files, so the
// per-file write cost (now batched) is visible.
func BenchmarkCommitFromChangesManyFiles(b *testing.B) {
	home := b.TempDir()
	b.Setenv("EASYVCS_HOME", home)
	cs, err := store.OpenDefault()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cs.Close() })
	repo, _ := cs.Create(store.NSRef{Name: "commitbench2"})
	ws := NewWorkspace(repo)

	var parent object.ID
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		changes := make([]FileChangeSpec, 20)
		for j := range changes {
			changes[j] = FileChangeSpec{Path: fmt.Sprintf("d%d/f%d.txt", j%4, j), Content: []byte(fmt.Sprintf("c-%d-%d", i, j))}
		}
		snap, _, err := ws.CommitFromChanges(parent, changes, "c", store.Author{Name: "t"}, "")
		if err != nil {
			b.Fatal(err)
		}
		parent = snap.RevisionHash
	}
}

// benchCommitN times N commits on a fresh store with/without the async index,
// reporting ns/op. Shared by the sync/async variants below.
func benchCommitN(b *testing.B, async bool) {
	home := b.TempDir()
	b.Setenv("EASYVCS_HOME", home)
	cs, err := store.OpenDefault()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = cs.Close() })
	if async {
		cs.WithAsyncIndex()
	}
	repo, _ := cs.Create(store.NSRef{Name: "idxbench"})
	ws := NewWorkspace(repo)
	var parent object.ID
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		snap, _, err := ws.CommitFromChanges(parent,
			[]FileChangeSpec{{Path: fmt.Sprintf("pkg/sub/f%d.txt", i), Content: []byte(fmt.Sprintf("c-%d", i))}},
			"c", store.Author{Name: "t"}, "")
		if err != nil {
			b.Fatal(err)
		}
		parent = snap.RevisionHash
	}
}

// BenchmarkCommitSyncIndex is the commit path with the index written inline.
func BenchmarkCommitSyncIndex(b *testing.B) { benchCommitN(b, false) }

// BenchmarkCommitAsyncIndex defers the index to a background worker.
func BenchmarkCommitAsyncIndex(b *testing.B) { benchCommitN(b, true) }
