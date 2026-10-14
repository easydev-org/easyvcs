package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

func cmdCommit(c *ctx) {
	repo, marker, err := c.loadRepo()
	if err != nil {
		c.fatal("commit:", err)
	}
	ws := revision.NewWorkspace(repo)
	dir := resolveWorkingDir()

	// --new creates a fresh revision; otherwise commit AMENDS onto the current
	// revision (the workspace's existing revision), keeping revision_id stable.
	// This matches the jj revision model: edit repeatedly, "new" to split.
	isNew := false
	for _, a := range os.Args[2:] {
		if a == "--new" || a == "-new" {
			isNew = true
		}
	}

	// Resolve the parent snapshot hash (current revision). Empty for a root
	// commit.
	var parentHash object.ID
	if marker.CurrentRevision != "" {
		cur, err := ws.GetRevision(marker.CurrentRevision)
		if err == nil {
			parentHash = cur.Hash
		}
	}

	// Read-only scan of the working directory: compute the diff against the
	// parent tree WITHOUT persisting the whole tree. Only the changed paths are
	// produced as file changes.
	changes, err := ws.ComputeChangesFromDir(parentHash, dir)
	if err != nil {
		c.fatal("commit:", err)
	}

	// Sub-repo scope: `commit --path <scope>` LIFTS the scope-relative changes
	// onto the ROOT branch (docs/subrepo-scopes.md §5). Only the subtree at
	// <scope> is touched; the rest of the repo is untouched. The working-dir
	// changes are filtered to that subtree and rewritten scope-relative.
	if scope := scopeFlag(os.Args[2:]); scope != "" {
		scoped := make([]revision.FileChangeSpec, 0, len(changes))
		for _, ch := range changes {
			rel, ok := stripScope(scope, ch.Path)
			if !ok {
				continue
			}
			scoped = append(scoped, revision.FileChangeSpec{Path: rel, Content: ch.Content, Delete: ch.Delete})
		}
		if len(scoped) == 0 {
			c.fatal("commit:", fmt.Errorf("no changes under %q to lift", scope))
		}
		branch := marker.Branch
		if branch == "" {
			branch = "main"
		}
		msg := commitMessage(os.Args[2:], "commit")
		snap, ch, err := ws.Lift(branch, scope, scoped, msg, authorIdentity(os.Args[2:]))
		if err != nil {
			c.fatal("commit:", err)
		}
		fmt.Printf("lifted %d change(s) into %s -> revision %s -> snapshot %s\n", len(scoped), scope, short(ch.ID), short(snap.RevisionHash.String()))
		marker.CurrentRevision = ch.ID
		if err := store.WriteMarker(".", marker); err != nil {
			c.fatal("commit:", err)
		}
		if err := c.cs.PutWorkspace(&store.WorkspaceRow{Path: ".", Repo: marker.Repo, CurrentRevision: ch.ID, Branch: marker.Branch}); err != nil {
			c.fatal("commit: record workspace:", err)
		}
		return
	}

	// Atomic commit from the computed changes (same code path as the server
	// /files API). Default amends onto the current revision (revision_id stable);
	// --new creates a fresh revision chain from the current tip.
	revisionID := ""
	if !isNew && marker.CurrentRevision != "" {
		revisionID = marker.CurrentRevision
	}
	snap, ch, err := ws.CommitFromChanges(
		parentHash, changes, "commit",
		authorIdentity(os.Args[2:]), revisionID,
	)
	if err != nil {
		c.fatal("commit:", err)
	}
	if !isNew && marker.CurrentRevision != "" {
		fmt.Printf("amended revision %s -> snapshot %s\n", short(ch.ID), short(snap.RevisionHash.String()))
	} else {
		fmt.Printf("revision %s -> snapshot %s\n", short(ch.ID), short(snap.RevisionHash.String()))
	}
	// Update the workspace pointer to the current revision.
	marker.CurrentRevision = ch.ID
	if err := store.WriteMarker(".", marker); err != nil {
		c.fatal("commit:", err)
	}
	if err := c.cs.PutWorkspace(&store.WorkspaceRow{Path: ".", Repo: marker.Repo, CurrentRevision: ch.ID, Branch: marker.Branch}); err != nil {
		c.fatal("commit: record workspace:", err)
	}
}

func cmdAmend(c *ctx) {
	repo, marker, err := c.loadRepo()
	if err != nil {
		c.fatal("amend:", err)
	}
	ws := revision.NewWorkspace(repo)
	dir := resolveWorkingDir()
	revisionID := marker.CurrentRevision
	cur, err := repo.GetRevision(revisionID)
	if err != nil {
		c.fatal("amend: no current revision:", err)
	}
	curSnap, err := repo.GetSnapshot(cur.Hash)
	if err != nil {
		c.fatal("amend:", err)
	}
	// Compute the diff against the current snapshot's tree using a read-only
	// scan (no full-tree persistence). The amend rewrites the current revision.
	changes, err := ws.ComputeChangesFromDir(cur.Hash, dir)
	if err != nil {
		c.fatal("amend:", err)
	}
	ns, ch, err := ws.CommitFromChanges(
		cur.Hash, changes,
		curSnap.Description, curSnap.Author, revisionID,
	)
	if err != nil {
		c.fatal("amend:", err)
	}
	// Amend keeps the same revision_id; repoint it to the new hash.
	if err := repo.UpdateRevisionHash(ch.ID, ns.RevisionHash); err != nil {
		c.fatal("amend:", err)
	}
	fmt.Printf("amended revision %s -> snapshot %s\n", short(ch.ID), short(ns.RevisionHash.String()))
}

func cmdMessage(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("message:", err)
	}
	args := os.Args[2:]
	if len(args) < 2 {
		c.fatal("usage: message <revision> <text>")
	}
	ws := revision.NewWorkspace(repo)
	revisionID, err := resolveRevisionArg(ws, repo, args[0])
	if err != nil {
		c.fatal("message:", err)
	}
	ns, ch, err := ws.SetDescription(revisionID, args[1])
	if err != nil {
		c.fatal("message:", err)
	}
	fmt.Printf("updated message on revision %s (id unchanged) -> snapshot %s\n", short(ch.ID), short(ns.RevisionHash.String()))
}

// resolveRevisionArg resolves a user-supplied revision expression (exact id, id
// prefix, branch/tag name, or "@") to a revision id.
func resolveRevisionArg(ws *revision.Workspace, repo *store.Repo, arg string) (string, error) {
	if arg == "" || arg == "@" {
		revs, err := ws.Log()
		if err != nil {
			return "", err
		}
		if len(revs) == 0 {
			return "", fmt.Errorf("no revisions")
		}
		return revs[0].ID, nil
	}
	if rf, err := ws.GetRef(arg); err == nil && rf != nil {
		return rf.Target, nil
	}
	revs, err := ws.Log()
	if err != nil {
		return "", err
	}
	for _, rv := range revs {
		if rv.ID == arg {
			return rv.ID, nil
		}
	}
	for _, rv := range revs {
		if strings.HasPrefix(rv.ID, arg) {
			return rv.ID, nil
		}
	}
	return "", fmt.Errorf("unknown revision %q", arg)
}
