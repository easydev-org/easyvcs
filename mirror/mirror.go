// Package mirror implements push/pull mirroring between an EasyVCS
// repository (revision-native) and an external git server, using go-git (no
// `git` binary). A push mirror exports a branch's tree to an external branch; a
// pull mirror reads an external branch into EasyVCS (Depth 0 = full history,
// Depth 1 = a single-revision snapshot).
package mirror

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	git "github.com/go-git/go-git/v5"
	gitconfig "github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	gitobject "github.com/go-git/go-git/v5/plumbing/object"

	"github.com/easylab-platform/easyvcs/gitbridge"
	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// PushTarget configures one force-push destination bound to a branch.
type PushTarget struct {
	Name    string
	URL     string
	Branch  string // branch name to export (default "main")
	Token   string
	LastRev string // last pushed revision id (may be empty on first push)
}

// PushResult reports the outcome of a single push.
type PushResult struct {
	Target string
	RevID  string
	TreeID string
}

// Push exports the tree of PushTarget.Branch to the external URL with
// `git push --force`. The target branch keeps exactly that tree; any
// divergence on the remote is overwritten (mirror semantics).
func Push(ctx context.Context, repo *store.Repo, t PushTarget) (*PushResult, error) {
	ws := revision.NewWorkspace(repo)

	ref, err := repo.GetRef(t.Branch)
	if err != nil {
		return nil, fmt.Errorf("push mirror: branch %q: %w", t.Branch, err)
	}
	if ref.Kind != store.RefBranch {
		return nil, fmt.Errorf("push mirror: %q is not a branch", t.Branch)
	}
	rev, err := repo.GetRevision(ref.Target)
	if err != nil {
		return nil, fmt.Errorf("push mirror: revision %q: %w", ref.Target, err)
	}
	snap, err := repo.GetSnapshot(rev.Hash)
	if err != nil {
		return nil, err
	}

	tmp, err := os.MkdirTemp("", "easyvcs-push-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	// Materialize the tree into a fresh git worktree (go-git, no `git` binary
	// needed) and force-push the branch. Using go-git here removes the runtime
	// dependency on the git CLI that the old shell-out path required.
	msg := snap.Description
	if msg == "" {
		msg = "easyvcs push " + ref.Target
	}
	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	g, err := git.PlainInit(work, false)
	if err != nil {
		return nil, err
	}
	if _, err := g.CreateRemote(&gitconfig.RemoteConfig{Name: "origin", URLs: []string{t.URL}}); err != nil {
		return nil, err
	}
	if err := ws.Materialize(snap.TreeID, work); err != nil {
		return nil, err
	}
	wt, err := g.Worktree()
	if err != nil {
		return nil, err
	}
	if err := wt.AddWithOptions(&git.AddOptions{All: true}); err != nil {
		return nil, err
	}
	if _, err := wt.Commit(msg, &git.CommitOptions{
		AllowEmptyCommits: true,
		Author:            &gitobject.Signature{Name: "easyvcs", Email: "easyvcs@example.com"},
	}); err != nil {
		return nil, err
	}
	auth, err := gitbridge.AuthFor(t.URL, t.Token, "", "")
	if err != nil {
		return nil, err
	}
	refName := plumbing.NewBranchReferenceName(t.Branch)
	if err := g.Storer.SetReference(plumbing.NewHashReference(refName, headHash(g))); err != nil {
		return nil, err
	}
	if err := g.PushContext(ctx, &git.PushOptions{
		RemoteName: "origin",
		RefSpecs:   []gitconfig.RefSpec{gitconfig.RefSpec(refName.String() + ":" + refName.String())},
		Force:      true,
		Auth:       auth,
	}); err != nil {
		return nil, err
	}

	return &PushResult{Target: t.Name, RevID: ref.Target, TreeID: snap.TreeID.String()}, nil
}

// headHash returns the current HEAD commit hash of g.
func headHash(g *git.Repository) plumbing.Hash {
	if h, err := g.Head(); err == nil {
		return h.Hash()
	}
	return plumbing.ZeroHash
}

// PullConfig configures one read-only mirror import of an external branch.
type PullConfig struct {
	URL      string
	Branch   string // external branch to snapshot (default "main")
	Token    string
	Interval int // seconds between scheduled pulls; <=0 = manual only
	// Depth limits fetched history: 0 = full; 1 = tip only (a snapshot). go-git
	// performs the shallow, single-branch clone (no `git` binary).
	Depth int
}

// Pull clones the external branch and records a single snapshot revision for
// it, repositioning the mirror's branch (default branch) onto that
// revision. History is not accumulated: each pull replaces the tip tree, so a
// mirror never exposes a commit-graph of its own (option B).
func Pull(ctx context.Context, repo *store.Repo, cfg PullConfig) (*store.Revision, error) {
	if cfg.Branch == "" {
		cfg.Branch = "main"
	}
	ws := revision.NewWorkspace(repo)

	tmp, err := os.MkdirTemp("", "easyvcs-pull-*")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	src, err := clone(ctx, cfg.URL, cfg.Branch, cfg.Token, cfg.Depth, tmp)
	if err != nil {
		return nil, err
	}
	treeID, err := ws.BuildTreeFromFS(src)
	if err != nil {
		return nil, err
	}

	// Parent = the mirror's current tip snapshot (if any), else none. This
	// keeps the snapshot lineage detectable without exposing an accumulated
	// external history on the read surface.
	var parents []object.ID
	if ref, err := repo.GetRef(cfg.Branch); err == nil && ref != nil {
		if rev, err := repo.GetRevision(ref.Target); err == nil {
			parents = []object.ID{rev.Hash}
		}
	}

	_, rev, err := ws.Commit(revision.CommitParams{
		Parents:     parents,
		TreeID:      treeID,
		Description: "mirror pull from " + cfg.URL,
		// System-authored: a mirror pull is the server syncing an external repo,
		// not a user change, so it is intentionally attributed to "easyvcs"
		// rather than to any caller (there is no authenticated user here).
		Author: store.Author{Name: "easyvcs", Email: "easyvcs@example.com"},
	})
	if err != nil {
		return nil, err
	}

	// Point the mirror's branch to the new revision.
	if err := repo.PutRef(&store.Ref{Name: cfg.Branch, Kind: store.RefBranch, Target: rev.ID}); err != nil {
		return nil, err
	}
	return rev, nil
}

// clone fetches an external branch into dir (via go-git — no `git` binary) and
// returns the worktree path. Depth 1 is a shallow tip-only fetch; 0 is full.
func clone(ctx context.Context, url, branch, token string, depth int, dir string) (string, error) {
	work := filepath.Join(dir, "src")
	co := &git.CloneOptions{URL: url, Depth: depth}
	if branch != "" {
		co.ReferenceName = plumbing.NewBranchReferenceName(branch)
		co.SingleBranch = true
	}
	auth, err := gitbridge.AuthFor(url, token, "", "")
	if err != nil {
		return "", err
	}
	co.Auth = auth
	if _, err := git.PlainCloneContext(ctx, work, false, co); err != nil {
		return "", fmt.Errorf("git clone: %w", err)
	}
	return work, nil
}
