package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

func cmdBranch(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("branch:", err)
	}
	args := os.Args[2:]
	if len(args) < 2 {
		c.fatal("usage: branch <name> <revision> [--message <text>] [--auto-commit]")
	}
	name := args[0]
	revisionArg := args[1]
	var message string
	autoCommit := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--message", "-m", "--message=":
			if strings.HasPrefix(args[i], "--message=") {
				message = strings.TrimPrefix(args[i], "--message=")
			} else if i+1 < len(args) {
				message = args[i+1]
				i++
			}
		case "--auto-commit", "-auto-commit":
			autoCommit = true
		}
	}
	// Resolve the revision argument to a revision id (supports @ / id prefix /
	// existing branch/tag name).
	ws := revision.NewWorkspace(repo)
	revisionID, err := resolveRevisionArg(ws, repo, revisionArg)
	if err != nil {
		c.fatal("branch:", err)
	}

	// A branch always owns a distinct revision id so an in-place amend on one
	// branch never mutates a revision shared by another branch. Creating a FRESH
	// branch derives a content clone with its own id and a ForkFrom link; the
	// source revision is left untouched. Re-running `branch` for an existing
	// name is idempotent and keeps its already-private target. message comes
	// from --message (or is inherited from the source); auto_commit finalizes an
	// uncommitted fork.
	var target string
	if cur, cerr := ws.GetRef(name); cerr == nil && cur != nil && cur.Kind == store.RefBranch {
		// Branch already exists and owns its derived target: no-op.
		target = cur.Target
	} else {
		_, ch, err2 := ws.Derive(revisionID, message, autoCommit)
		if err2 != nil {
			c.fatal("branch:", err2)
		}
		target = ch.ID
	}
	r, err := ws.SetRef(name, store.RefBranch, target)
	if err != nil {
		c.fatal("branch:", err)
	}
	if target != revisionID {
		fmt.Printf("branch %s -> revision %s (forked from %s)\n", r.Name, short(r.Target), short(revisionID))
	} else {
		fmt.Printf("branch %s -> revision %s\n", r.Name, short(r.Target))
	}
}

func cmdTag(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("tag:", err)
	}
	if len(os.Args) < 4 {
		c.fatal("usage: tag <name> <revision>")
	}
	ws := revision.NewWorkspace(repo)
	// A tag is immutable: re-tagging an existing name is rejected by SetRef.
	r, err := ws.SetRef(os.Args[2], store.RefTag, os.Args[3])
	if err != nil {
		c.fatal("tag:", err)
	}
	fmt.Printf("tag %s -> revision %s\n", r.Name, short(r.Target))
}

func cmdRefs(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("refs:", err)
	}
	refs, err := revision.NewWorkspace(repo).ListRefs()
	if err != nil {
		c.fatal("refs:", err)
	}
	for _, r := range refs {
		fmt.Printf("%s  %s -> revision %s\n", r.Kind, r.Name, short(r.Target))
	}
}
