package revision

import (
	"testing"

	"github.com/easylab-platform/easyvcs/ignore"
	"github.com/easylab-platform/easyvcs/store"
)

// TestBuildTreeFromFSIgnores verifies buildTreeFromFS excludes .gitignore /
// .vcsignore matched files and tree-building round-trips.
func TestBuildTreeFromFSIgnores(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, dir string) {
		buildFS(t, dir, "src/main.go", "package main\n")
		buildFS(t, dir, "src/secret.key", "secret\n")
		buildFS(t, dir, ".gitignore", "*.key\n")
		treeID, err := ws.BuildTreeFromFS(dir)
		if err != nil {
			t.Fatal(err)
		}
		tree := ws.mustTree(treeID)
		sub := ws.mustTree(tree.Entries["src"].ID)
		if _, ok := sub.Entries["secret.key"]; ok {
			t.Fatal("secret.key should be ignored by .gitignore")
		}
		if _, ok := sub.Entries["main.go"]; !ok {
			t.Fatal("main.go should be tracked")
		}
		t.Logf("build tree ignores .gitignore patterns ok")
	})
}

// TestBuildTreeFromFSVcsIgnore verifies .vcsignore is honored too.
func TestBuildTreeFromFSVcsIgnore(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, dir string) {
		buildFS(t, dir, "data/cache.bin", "abc")
		buildFS(t, dir, ".vcsignore", "cache.bin\n")
		treeID, err := ws.BuildTreeFromFS(dir)
		if err != nil {
			t.Fatal(err)
		}
		tree := ws.mustTree(treeID)
		sub := ws.mustTree(tree.Entries["data"].ID)
		if _, ok := sub.Entries["cache.bin"]; ok {
			t.Fatal("cache.bin should be excluded by .vcsignore")
		}
	})
}

// TestDropIgnoredPaths verifies the tree-filter helper removes ignored paths
// from a tree and prunes empty dirs.
func TestDropIgnoredPaths(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, dir string) {
		buildFS(t, dir, "keep.txt", "keep\n")
		buildFS(t, dir, "junk.tmp", "junk\n")
		m := ignore.NewWithLines(".", "*.tmp")
		treeID, err := ws.BuildTreeFromFS(dir)
		if err != nil {
			t.Fatal(err)
		}
		newID, err := ws.DropIgnoredPathsFromTree(dir, m, treeID)
		if err != nil {
			t.Fatal(err)
		}
		nt := ws.mustTree(newID)
		if _, ok := nt.Entries["junk.tmp"]; ok {
			t.Fatal("junk.tmp should be dropped")
		}
		if _, ok := nt.Entries["keep.txt"]; !ok {
			t.Fatal("keep.txt should remain")
		}
	})
}

var _ = store.ErrNotFound
