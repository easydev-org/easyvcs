package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

func cmdCheckout(c *ctx) {
	repo, marker, err := c.loadRepo()
	if err != nil {
		c.fatal("checkout:", err)
	}
	if len(os.Args) < 3 {
		c.fatal("usage: checkout <sha> [DEST]")
	}
	id, err := object.HexToID(os.Args[2])
	if err != nil {
		c.fatal("checkout:", err)
	}
	snap, err := repo.GetSnapshot(id)
	if err != nil {
		c.fatal("checkout: unknown snapshot:", err)
	}
	ws := revision.NewWorkspace(repo)
	dest := ""
	if len(os.Args) > 3 {
		dest = os.Args[3]
		if !filepath.IsAbs(dest) {
			wd, _ := os.Getwd()
			dest = filepath.Join(wd, dest)
		}
	} else {
		dest = "."
	}
	if err := ws.Materialize(snap.TreeID, dest); err != nil {
		c.fatal("checkout:", err)
	}
	// Checkout repoints the workspace's current_change only (does not touch db).
	marker.CurrentRevision = snap.RevisionID
	if err := store.WriteMarker(".", marker); err != nil {
		c.fatal("checkout:", err)
	}
	if err := c.cs.PutWorkspace(&store.WorkspaceRow{Path: ".", Repo: marker.Repo, CurrentRevision: snap.RevisionID, Branch: marker.Branch}); err != nil {
		c.fatal("checkout:", err)
	}
	fmt.Printf("checked out snapshot %s (revision %s)\n", short(id.String()), short(snap.RevisionID))
}

func cmdRebase(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("rebase:", err)
	}
	args := os.Args[2:]
	var positional []string
	var ontoIDs []object.ID
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--onto", "-onto":
			if i+1 < len(args) {
				id, err := object.HexToID(args[i+1])
				if err != nil {
					c.fatal("rebase:", err)
				}
				ontoIDs = append(ontoIDs, id)
				i++
			}
		default:
			positional = append(positional, args[i])
		}
	}
	if len(positional) < 1 || len(ontoIDs) == 0 {
		c.fatal("usage: rebase <revision> [--onto <parent-sha>]...")
	}
	ws := revision.NewWorkspace(repo)
	ns, ch, err := ws.Rebase(positional[0], ontoIDs)
	if err != nil {
		c.fatal("rebase:", err)
	}
	fmt.Printf("rebased revision %s (id unchanged) -> snapshot %s\n", short(ch.ID), short(ns.RevisionHash.String()))
	// Report conflicts embedded in the new tree, if any.
	atoms, err := ws.ConflictsInTree(ns.TreeID)
	if err == nil && len(atoms) > 0 {
		for _, a := range atoms {
			fmt.Fprintf(os.Stderr, "conflict at %s (resolve <revision> %s)\n", a.Path, a.Path)
		}
	}
}

func cmdResolve(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("resolve:", err)
	}
	if len(os.Args) < 4 {
		c.fatal("usage: resolve <revision> <path> [--side N]")
	}
	revisionID := os.Args[2]
	path := os.Args[3]
	side := 0
	for i := 4; i < len(os.Args); i++ {
		switch os.Args[i] {
		case "--side", "-side":
			if i+1 < len(os.Args) {
				if _, serr := fmt.Sscanf(os.Args[i+1], "%d", &side); serr != nil {
					c.fatalf("invalid --side %q (expected a number)", os.Args[i+1])
				}
				i++
			}
		}
	}
	ws := revision.NewWorkspace(repo)
	ns, ch, err := ws.Resolve(revisionID, path, side)
	if err != nil {
		c.fatal("resolve:", err)
	}
	fmt.Printf("resolved conflict %s in revision %s -> snapshot %s\n", path, short(ch.ID), short(ns.RevisionHash.String()))
}

func cmdSquash(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("squash:", err)
	}
	if len(os.Args) < 3 {
		c.fatal("usage: squash <revision>")
	}
	ws := revision.NewWorkspace(repo)
	parentSnap, parentCh, err := ws.Squash(os.Args[2])
	if err != nil {
		c.fatal("squash:", err)
	}
	fmt.Printf("squashed into revision %s -> snapshot %s\n", short(parentCh.ID), short(parentSnap.RevisionHash.String()))
}

func cmdGC(c *ctx) {
	dry := false
	for _, a := range os.Args[2:] {
		if a == "--dry-run" || a == "-dry-run" || a == "-n" {
			dry = true
		}
	}
	res, err := revision.GCRun(c.cs, revision.GCOptions{DryRun: dry})
	if err != nil {
		c.fatal("gc:", err)
	}
	if dry {
		fmt.Printf("gc (dry-run): %d object(s) present, %d would be pruned\n", res.Total, res.Swept)
	} else {
		fmt.Printf("gc: %d object(s) scanned, pruned %d\n", res.Total, res.Swept)
	}
}

func cmdVerify(c *ctx) {
	res, err := revision.Verify(c.cs)
	if err != nil {
		c.fatal("verify:", err)
	}
	fmt.Printf("verify: repos=%d revisions=%d snapshots=%d objects=%d missing_objects=%d broken_snapshots=%d\n",
		res.Repos, res.Revisions, res.Snapshots, res.Objects, res.MissingObjects, res.BrokenSnapshots)
	if res.MissingObjects > 0 || res.BrokenSnapshots > 0 {
		c.fatal("verify: FAILED — dangling objects or broken snapshots found")
	}
}
