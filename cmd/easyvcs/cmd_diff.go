package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
)

func cmdDiff(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("diff:", err)
	}
	if len(os.Args) < 4 {
		c.fatal("usage: diff <shaA> <shaB>")
	}
	a, err := object.HexToID(os.Args[2])
	if err != nil {
		c.fatal("diff:", err)
	}
	b, err := object.HexToID(os.Args[3])
	if err != nil {
		c.fatal("diff:", err)
	}
	ws := revision.NewWorkspace(repo)
	aTree := snapshotOrTree(ws, a)
	bTree := snapshotOrTree(ws, b)
	fileDiffs, err := ws.DiffContent(aTree, bTree)
	if err != nil {
		c.fatal("diff:", err)
	}
	for _, fd := range fileDiffs {
		prefix := "M"
		switch fd.Status {
		case revision.StatusAdded:
			prefix = "A"
		case revision.StatusRemoved:
			prefix = "D"
		}
		fmt.Printf("%s %s\n", prefix, fd.Path)
	}
	// Print full unified diffs if content is available (non-empty).
	for _, fd := range fileDiffs {
		if fd.Content != "" {
			fmt.Println("--- " + fd.Path)
			fmt.Println("+++ " + fd.Path)
			fmt.Println(fd.Content)
		}
	}
}

func cmdMerge(c *ctx) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("merge:", err)
	}
	fs := flag.NewFlagSet("merge", flag.ExitOnError)
	var base, ours, theirs string
	fs.StringVar(&base, "base", "", "base tree sha")
	fs.StringVar(&ours, "ours", "", "ours tree sha")
	fs.StringVar(&theirs, "theirs", "", "theirs tree sha")
	if err := fs.Parse(os.Args[2:]); err != nil {
		c.fatal("merge:", err)
	}
	if base == "" || ours == "" || theirs == "" {
		c.fatal("usage: merge --base <sha> --ours <sha> --theirs <sha>")
	}
	b, _ := object.HexToID(base)
	o, _ := object.HexToID(ours)
	t, _ := object.HexToID(theirs)
	ws := revision.NewWorkspace(repo)
	mergedID, conflicts, err := ws.Merge(b, o, t)
	if err != nil {
		c.fatal("merge:", err)
	}
	fmt.Printf("merged tree %s\n", mergedID.String())
	for _, cf := range conflicts {
		fmt.Fprintf(os.Stderr, "conflict at %s\n", cf.Path)
	}
}
