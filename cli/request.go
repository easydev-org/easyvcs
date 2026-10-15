package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
	"github.com/easylab-platform/easyvcs/transfer"
)

// cmdRequest dispatches the `request` (Revision Request) subcommands. A request
// pins a snapshot; its subject is content-addressed and immutable, so the review
// never drifts when a branch moves.
func cmdRequest(c *ctx) {
	args := os.Args[2:]
	if len(args) == 0 {
		c.fatal("usage: request create|list|show|diff|update|merge ...")
	}
	switch args[0] {
	case "create":
		cmdRequestCreate(c, args[1:])
	case "list", "ls":
		cmdRequestList(c, args[1:])
	case "show":
		cmdRequestShow(c, args[1:])
	case "diff":
		cmdRequestDiff(c, args[1:])
	case "update":
		cmdRequestUpdate(c, args[1:])
	case "merge":
		cmdRequestMerge(c, args[1:])
	default:
		c.fatalf("unknown request subcommand %q", args[0])
	}
}

// parseInto parses `--into ns/name@branch`.
func parseInto(s string) (ns, name, branch string, err error) {
	branch = "main"
	at := strings.LastIndex(s, "@")
	if at >= 0 {
		branch = s[at+1:]
		s = s[:at]
	}
	i := strings.Index(s, "/")
	if i < 0 {
		return "", "", "", fmt.Errorf("expected ns/name[@branch], got %q", s)
	}
	return s[:i], s[i+1:], branch, nil
}

// requestFlags pulls --key value pairs plus the positional --from/--into/--title.
type requestFlags struct {
	from, into, title, body, remote, snapshot string
	remoteOnly                                bool
}

func parseRequestFlags(args []string) requestFlags {
	var f requestFlags
	for i := 0; i < len(args); i++ {
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch args[i] {
		case "--from", "-from":
			f.from = next()
		case "--into", "-into":
			f.into = next()
		case "--title", "-title":
			f.title = next()
		case "--body", "-body":
			f.body = next()
		case "--remote", "-remote":
			f.remote = next()
		case "--snapshot", "-snapshot":
			f.snapshot = next()
		}
	}
	return f
}

// cmdRequestCreate implements "from a local directory": it builds a tree from
// the directory, commits a snapshot on top of the target tip, pushes that
// snapshot (as a bundle) to the server, and opens an RR pinned to it.
func cmdRequestCreate(c *ctx, args []string) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("request create:", err)
	}
	f := parseRequestFlags(args)
	if f.from == "" || f.into == "" || f.title == "" {
		c.fatal("usage: request create --from <dir> --into <ns/name>@<branch> --title <t> [--body <b>] [--remote origin]")
	}
	ns, name, branch, err := parseInto(f.into)
	if err != nil {
		c.fatal("request create:", err)
	}
	// The local repo must match the target ns/name so the pushed bundle lands
	// in the right repository.
	if repo.Name != ns+"/"+name {
		c.fatalf("request create: local workspace is %s, but --into is %s/%s (attach or clone the target first)", repo, ns, name)
	}
	ws := revision.NewWorkspace(repo)

	// 1) Build a tree from the local directory (no workdir persistence).
	treeID, err := ws.BuildTreeFromFS(f.from)
	if err != nil {
		c.fatal("request create: build tree:", err)
	}

	// 2) Resolve the base: the local branch tip (the target branch's snapshot).
	var parents []object.ID
	var baseSnapshot object.ID
	if ref, rerr := ws.GetRef(branch); rerr == nil && ref != nil {
		if rev, verr := ws.GetRevision(ref.Target); verr == nil {
			parents = []object.ID{rev.Hash}
			baseSnapshot = rev.Hash
		}
	}

	// 3) Commit a snapshot for the request. If --snapshot is given, reuse an
	// existing snapshot instead of committing a new one.
	var srcSnapshot object.ID
	if f.snapshot != "" {
		id, herr := object.HexToID(f.snapshot)
		if herr != nil {
			c.fatal("request create: bad --snapshot:", herr)
		}
		srcSnapshot = id
	} else {
		snap, _, cerr := ws.Commit(revision.CommitParams{
			Parents:     parents,
			TreeID:      treeID,
			Description: f.title,
			Author:      authorIdentity(os.Args[2:]),
		})
		if cerr != nil {
			c.fatal("request create: commit snapshot:", cerr)
		}
		srcSnapshot = snap.RevisionHash
	}

	// 4) Push the pinned snapshot's bundle to the remote (so the server has the
	// objects), then open the RR.
	remote := f.remote
	if remote == "" {
		remote = "origin"
	}
	if err := pushRequestBundle(repo, remote, srcSnapshot, baseSnapshot); err != nil {
		c.fatal("request create: push:", err)
	}
	req := map[string]any{
		"source_snapshot": srcSnapshot.String(),
		"target_branch":   branch,
		"title":           f.title,
		"body":            f.body,
	}
	resp, err := requestPost(repo, remote, "/rr", req)
	if err != nil {
		c.fatal("request create:", err)
	}
	fmt.Printf("opened revision request: %s\n", resp)
}

// pushRequestBundle pushes just the snapshot (and its reachable objects) to the
// remote so the server can build the RR without a branch.
func pushRequestBundle(repo *store.Repo, remoteName string, srcSnapshot, baseSnapshot object.ID) error {
	rem, err := repo.GetRemote(remoteName)
	if err != nil {
		return fmt.Errorf("remote %q not found: %w", remoteName, err)
	}
	b := &transfer.Bundle{Version: transfer.Version, Repo: repo.NSRef()}
	if err := appendSnapshotBundle(repo, b, srcSnapshot); err != nil {
		return err
	}
	full := strings.TrimRight(rem.URL, "/") + "/api/v1/namespaces/" + repo.Name
	return postBundleOnly(full+"/rr/objects", b, rem.Token)
}

// appendSnapshotBundle adds a snapshot + its reachable objects to a bundle.
func appendSnapshotBundle(repo *store.Repo, b *transfer.Bundle, snapID object.ID) error {
	snap, err := repo.GetSnapshot(snapID)
	if err != nil {
		return err
	}
	b.Snapshots = append(b.Snapshots, snap)
	if rev, rerr := repo.GetRevision(snap.RevisionID); rerr == nil {
		b.Revisions = append(b.Revisions, rev)
	}
	seen := map[string]bool{}
	var walk func(id object.ID) error
	walk = func(id object.ID) error {
		if seen[id.String()] {
			return nil
		}
		seen[id.String()] = true
		o, rerr := repo.ReadObject(id)
		if rerr != nil {
			return rerr
		}
		payload, eerr := object.EncodeObject(o)
		if eerr != nil {
			return eerr
		}
		b.Objects = append(b.Objects, transfer.ObjectRecord{Kind: o.Kind, Content: payload})
		if o.Kind == object.KindTree && o.Tree != nil {
			for _, e := range o.Tree.SortedEntries() {
				if err := walk(e.ID); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return walk(snap.TreeID)
}

// requestPost POSTs a JSON body to a remote RR endpoint and returns the decoded
// response.
func requestPost(repo *store.Repo, remoteName, path string, body any) (string, error) {
	rem, err := repo.GetRemote(remoteName)
	if err != nil {
		return "", fmt.Errorf("remote %q not found: %w", remoteName, err)
	}
	full := strings.TrimRight(rem.URL, "/") + "/api/v1/namespaces/" + repo.Name + path
	payload, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	data, err := doPost(full, payload, rem.Token)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// requestGet GETs a remote RR endpoint.
func requestGet(repo *store.Repo, remoteName, path string) (string, error) {
	rem, err := repo.GetRemote(remoteName)
	if err != nil {
		return "", fmt.Errorf("remote %q not found: %w", remoteName, err)
	}
	full := strings.TrimRight(rem.URL, "/") + "/api/v1/namespaces/" + repo.Name + path
	req, err := http.NewRequest(http.MethodGet, full, nil)
	if err != nil {
		return "", err
	}
	if rem.Token != "" {
		req.Header.Set("Authorization", "Bearer "+rem.Token)
	}
	resp, err := remoteClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("server %d: %s", resp.StatusCode, string(buf))
	}
	return string(buf), nil
}

// remoteOf returns the remote name (default "origin") for a request subcommand.
func remoteOf(args []string) (string, []string) {
	name := "origin"
	var rest []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--remote" || args[i] == "-remote" {
			if i+1 < len(args) {
				name = args[i+1]
				i++
			}
			continue
		}
		rest = append(rest, args[i])
	}
	return name, rest
}

func cmdRequestList(c *ctx, args []string) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("request list:", err)
	}
	remote, rest := remoteOf(args)
	state := ""
	if len(rest) > 0 {
		state = rest[0]
	}
	path := "/rr"
	if state != "" {
		path += "?state=" + state
	}
	resp, err := requestGet(repo, remote, path)
	if err != nil {
		c.fatal("request list:", err)
	}
	fmt.Println(resp)
}

func cmdRequestShow(c *ctx, args []string) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("request show:", err)
	}
	remote, rest := remoteOf(args)
	if len(rest) < 1 {
		c.fatal("usage: request show <iid> [--remote origin]")
	}
	resp, err := requestGet(repo, remote, "/rr/"+rest[0])
	if err != nil {
		c.fatal("request show:", err)
	}
	fmt.Println(resp)
}

func cmdRequestDiff(c *ctx, args []string) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("request diff:", err)
	}
	remote, rest := remoteOf(args)
	if len(rest) < 1 {
		c.fatal("usage: request diff <iid> [--remote origin]")
	}
	resp, err := requestGet(repo, remote, "/rr/"+rest[0]+"/diff")
	if err != nil {
		c.fatal("request diff:", err)
	}
	fmt.Println(resp)
}

func cmdRequestUpdate(c *ctx, args []string) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("request update:", err)
	}
	remote, rest := remoteOf(args)
	if len(rest) < 2 {
		c.fatal("usage: request update <iid> <snapshot-sha> [--remote origin]")
	}
	if _, herr := object.HexToID(rest[1]); herr != nil {
		c.fatal("request update: bad snapshot sha:", herr)
	}
	resp, err := requestPost(repo, remote, "/rr/"+rest[0]+"/update", map[string]any{"source_snapshot": rest[1]})
	if err != nil {
		c.fatal("request update:", err)
	}
	fmt.Println(resp)
}

func cmdRequestMerge(c *ctx, args []string) {
	repo, _, err := c.loadRepo()
	if err != nil {
		c.fatal("request merge:", err)
	}
	// --allow-conflict lands the merge even if it carries conflicts (the target
	// moves to a tree with conflict markers). Off by default: a conflicted merge
	// stays pending until resolved.
	allow := false
	var cleaned []string
	for _, a := range args {
		if a == "--allow-conflict" || a == "-allow-conflict" {
			allow = true
			continue
		}
		cleaned = append(cleaned, a)
	}
	remote, rest := remoteOf(cleaned)
	if len(rest) < 1 {
		c.fatal("usage: request merge <iid> [--allow-conflict] [--remote origin]")
	}
	path := "/rr/" + rest[0] + "/merge"
	if allow {
		path += "?allow_conflict=true"
	}
	resp, err := requestPost(repo, remote, path, map[string]any{})
	if err != nil {
		c.fatal("request merge:", err)
	}
	fmt.Println(resp)
}

// postBundleOnly sends a bundle frame to url with the token (used to push a
// request's snapshot without a ref).
func postBundleOnly(url string, b *transfer.Bundle, token string) error {
	encoded, err := transfer.CompressBundle(b)
	if err != nil {
		return err
	}
	_, err = doPostCompressed(url, encoded, token)
	return err
}
