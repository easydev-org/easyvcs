package store

import (
	"testing"
)

// TestChangedPathDirs covers the prefix computation (path itself + ancestors).
func TestChangedPathDirs(t *testing.T) {
	got := changedPathDirs([]string{"a/b/c.txt", "top.txt", "x/y/"})
	for _, want := range []string{"", "a", "a/b", "a/b/c.txt", "top.txt", "x", "x/y"} {
		if !got[want] {
			t.Fatalf("changedPathDirs missing %q (got %v)", want, got)
		}
	}
	if got["a/b/c"] {
		t.Fatalf("unexpected prefix a/b/c in %v", got)
	}
}

// TestRevisionsTouching verifies the path index: only revisions that changed
// something under a directory are returned (and the lazy backfill works).
func TestRevisionsTouching(t *testing.T) {
	cs := newTestCentral(t)
	repo, err := cs.Create(RepoRef{Namespace: "idx", Name: "r"})
	if err != nil {
		t.Fatal(err)
	}
	put := func(id string, changed []string) {
		t.Helper()
		if err := repo.PutRevision(&Revision{ID: id, ChangedPaths: changed}); err != nil {
			t.Fatal(err)
		}
	}
	put("r1", []string{"pkg-a/a.go"})     // touches pkg-a
	put("r2", []string{"README.md"})      // touches only root
	put("r3", []string{"pkg-a/sub/b.go"}) // touches pkg-a/sub + pkg-a
	put("r4", []string{"pkg-b/c.go"})     // other dir

	got, err := repo.RevisionsTouching("pkg-a")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"r1": true, "r3": true}
	if len(got) != len(want) {
		t.Fatalf("pkg-a revisions = %v, want r1,r3", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Fatalf("unexpected %q under pkg-a", id)
		}
	}

	// The root ("") touches everything.
	all, _ := repo.RevisionsTouching("")
	if len(all) != 4 {
		t.Fatalf("root revisions = %d, want 4", len(all))
	}

	// pkg-a/sub is a nested sub-repo: only r3.
	sub, _ := repo.RevisionsTouching("pkg-a/sub")
	if len(sub) != 1 || sub[0] != "r3" {
		t.Fatalf("pkg-a/sub revisions = %v, want [r3]", sub)
	}
}
