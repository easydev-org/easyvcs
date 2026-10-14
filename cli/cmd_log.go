package main

import (
	"fmt"
	"os"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
)

func cmdLog(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("log:", err)
	}
	ws := revision.NewWorkspace(repo)
	revs, err := ws.Log()
	if err != nil {
		c.fatal("log:", err)
	}
	for _, ch := range revs {
		snap, err := ws.GetSnapshot(ch.Hash)
		if err != nil {
			fmt.Fprintf(os.Stderr, "log: %v\n", err)
			continue
		}
		fmt.Printf("%s  %s\n", short(ch.ID), short(snap.RevisionHash.String()))
		fmt.Printf("    %s\n", snap.Description)
	}
}

// cmdLogScope prints the revisions that touched a SUBDIRECTORY (its projected
// history), newest first — powered by the path index, so it is O(revisions
// under the path), not O(all). docs/subrepo-scopes.md §6/§8.
func cmdLogScope(c *ctx, scope string) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("log:", err)
	}
	ws := revision.NewWorkspace(repo)
	ids, err := repo.RevisionsTouching(scope)
	if err != nil {
		c.fatal("log:", err)
	}
	for _, id := range ids {
		rev, err := repo.GetRevision(id)
		if err != nil {
			continue
		}
		snap, err := ws.GetSnapshot(rev.Hash)
		if err != nil {
			continue
		}
		fmt.Printf("%s  %s\n", short(rev.ID), short(snap.RevisionHash.String()))
		fmt.Printf("    %s\n", snap.Description)
	}
}

// cmdLogPath reports the revisions that modified a given path, along with the
// count. It walks the DAG from the current revision upward and filters using
// each revision's ChangedPaths (cheap), then compares blob hashes to confirm
// an actual content change.
func cmdLogPath(c *ctx, path string, countOnly bool) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("log:", err)
	}
	ws := revision.NewWorkspace(repo)
	edits, err := ws.FileHistory("", path)
	if err != nil {
		c.fatal("log:", err)
	}
	if countOnly {
		fmt.Println(len(edits))
		return
	}
	for _, e := range edits {
		fmt.Printf("%s  %s\n", short(e.RevisionID), short(e.RevisionHash.String()))
		fmt.Printf("    %s\n", e.Timestamp.Format("2006-01-02 15:04:05"))
	}
}

func cmdShow(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("show:", err)
	}
	if len(os.Args) < 3 {
		c.fatal("usage: show <sha>")
	}
	id, err := object.HexToID(os.Args[2])
	if err != nil {
		c.fatal("show:", err)
	}
	snap, err := repo.GetSnapshot(id)
	if err != nil {
		c.fatal("show:", err)
	}
	fmt.Printf("snapshot  %s\n", snap.RevisionHash)
	fmt.Printf("revision  %s\n", snap.RevisionID)
	fmt.Printf("tree      %s\n", snap.TreeID)
	for _, p := range snap.Parents {
		fmt.Printf("parent    %s\n", p)
	}
	fmt.Printf("author    %s\n", snap.Author)
	fmt.Printf("description %s\n", snap.Description)
}
