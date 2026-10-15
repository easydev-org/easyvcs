package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/easylab-platform/easyvcs/ignore"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

func cmdInit(c *ctx) {
	args := os.Args[2:]
	if len(args) == 0 {
		c.fatal("usage: init <ns/name> [DIR]")
	}
	ns, name, err := splitRepo(args[0])
	if err != nil {
		c.fatal("init:", err)
	}
	dir := "."
	if len(args) > 1 {
		dir = args[1]
	}
	repo, err := c.cs.Create(store.NSRef{Name: ns + "/" + name})
	if err != nil {
		c.fatal("init:", err)
	}
	if err := store.WriteMarker(dir, &store.WorkspaceMarker{Repo: repo.NSRef()}); err != nil {
		c.fatal("init:", err)
	}
	fmt.Printf("initialized repository %s/%s, workspace bound at %s\n", ns, name, dir)
}

// cmdClone clones a repository from a remote URL (http(s):// or a local path)
// into a fresh repository + workspace in the current directory. It registers the
// remote as "origin", performs the first fetch to record remote refs, and pulls
// the remote default branch (or an explicit source ref) into the local branch.
// For a local remote the source repo is identified by the target workspace
// marker's Home + Repo; for an http(s) URL it talks to the EasyVCS server.
func cmdClone(c *ctx) {
	args := os.Args[2:]
	url := ""
	var branch string
	var dir string
	var namespace string
	var repoName string
	destDir := "."
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-b", "--branch":
			if i+1 < len(args) {
				branch = args[i+1]
				i++
			}
		case "-d", "--dir":
			if i+1 < len(args) {
				destDir = args[i+1]
				i++
			}
		case "-n", "--namespace":
			if i+1 < len(args) {
				namespace = args[i+1]
				i++
			}
		default:
			if url == "" {
				url = args[i]
			} else if dir == "" {
				dir = args[i]
			}
		}
	}
	if url == "" {
		c.fatal("usage: clone <url> [DIR] [-b branch] [-n namespace]")
	}
	if dir != "" {
		destDir = dir
	}
	if destDir == "." {
		// Default destination directory = last path segment of the URL.
		base := filepath.Base(strings.TrimRight(expandLocalPath(url), "/"))
		if base != "" && base != "/" && base != "." {
			destDir = base
		}
	}

	// Determine the source repo ref: for a local path use the target marker's
	// repo; for an http(s) URL derive it from the path.
	srcRef := store.NSRef{}
	if isLocalURL(url) {
		m, err := store.LookupMarker(expandLocalPath(url))
		if err != nil {
			c.fatal("clone:", err)
		}
		srcRef = m.Repo
	} else {
		ns, name := repoRefFromURLPath(url)
		if ns == "" {
			ns = "default"
		}
		srcRef = store.NSRef{Name: ns + "/" + name}
	}
	if namespace != "" || repoName != "" {
		// The container name is "ns/name" (a slash-named namespace).
		nsPart := namespace
		if nsPart == "" {
			nsPart = topOf(srcRef.Name)
		}
		nmPart := repoName
		if nmPart == "" {
			nmPart = bottomOf(srcRef.Name)
		}
		srcRef = store.NSRef{Name: nsPart + "/" + nmPart}
	}

	// Create the destination repo (fresh central store for the clone).
	home := store.HomeDir()
	cs, err := store.OpenDefault()
	if err != nil {
		c.fatal("clone:", err)
	}
	dstRepo, err := cs.Create(srcRef)
	if err != nil {
		c.fatal("clone:", err)
	}
	if err := dstRepo.PutRemote("origin", url, ""); err != nil {
		c.fatal("clone:", err)
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		c.fatal("clone:", err)
	}
	if err := store.WriteMarker(destDir, &store.WorkspaceMarker{Repo: dstRepo.NSRef(), Home: home}); err != nil {
		c.fatal("clone:", err)
	}
	if err := cs.PutWorkspace(&store.WorkspaceRow{Path: destDir, Repo: dstRepo.NSRef(), Branch: branch}); err != nil {
		c.fatal("clone:", err)
	}

	// Fetch/pull the remote default branch (or explicit branch).
	var pullErr error
	if isLocalURL(url) {
		if err := doFetch(dstRepo, []string{"origin"}); err != nil {
			fmt.Fprintln(os.Stderr, "clone (fetch):", err)
		}
		want := branch
		if want == "" {
			if def, _ := dstRepo.GetRemoteDefaultBranch("origin"); def != "" {
				want = def
			} else if meta, _ := dstRepo.RepoMeta(); meta.DefaultBranch != "" {
				want = meta.DefaultBranch
			}
		}
		if want == "" {
			want = "main"
		}
		if err := doPull(dstRepo, []string{"origin", want}); err != nil {
			pullErr = err
		}
	} else {
		want := branch
		if want == "" {
			want = "main"
		}
		pullErr = doPull(dstRepo, []string{"origin", want})
	}
	if pullErr != nil {
		c.fatal("clone (pull):", pullErr)
	}
	fmt.Printf("cloned %s -> %s\n", url, destDir)
}

// repoRefFromURLPath derives a RepoRef{Namespace,Name} from a url path of the
// form .../namespace/name or .../name. It is used to name a clone of a (network)
// remote when no explicit namespace/name is given.
func repoRefFromURLPath(url string) (string, string) {
	p := url
	if i := strings.Index(p, "://"); i >= 0 {
		p = p[i+3:]
		// Drop the host (up to the first '/'), leaving the path segments.
		if j := strings.Index(p, "/"); j >= 0 {
			p = p[j+1:]
		} else {
			p = ""
		}
	}
	p = strings.Trim(p, "/")
	parts := strings.Split(p, "/")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	if len(parts) >= 2 {
		return parts[len(parts)-2], parts[len(parts)-1]
	}
	if len(parts) == 1 {
		return "default", parts[0]
	}
	return "default", ""
}

func cmdWorkspace(c *ctx) {
	args := os.Args[2:]
	if len(args) == 0 {
		c.fatal("usage: workspace attach|open|list")
	}
	switch args[0] {
	case "attach", "open":
		cmdWorkspaceAttach(c)
	case "list":
		cmdWorkspaceList(c)
	default:
		c.fatalf("unknown workspace subcommand %q\n", args[0])
	}
}

// cmdWorkspaceAttach binds the current directory to a repository. It reads (or
// creates) a workspace marker, resolves the repo, and if the recorded current
// revision no longer exists it auto-forks a new tip revision (plan A). It only
// runs on explicit attach, never implicitly during other commands.
func cmdWorkspaceAttach(c *ctx) {
	marker, err := store.LookupMarker(".")
	if err != nil {
		// No marker yet: create from a repo arg if provided.
		args := os.Args[2:]
		if len(args) < 2 {
			c.fatal("usage: workspace attach <ns/name> [DIR]")
		}
		ns, name, err := splitRepo(args[1])
		if err != nil {
			c.fatal("workspace attach:", err)
		}
		repo, err := c.cs.OpenRepo(store.NSRef{Name: ns + "/" + name})
		if err != nil {
			c.fatal("workspace attach:", err)
		}
		marker = &store.WorkspaceMarker{Repo: repo.NSRef()}
		if err := store.WriteMarker(".", marker); err != nil {
			c.fatal("workspace attach:", err)
		}
		fmt.Printf("workspace attached to %s\n", repo)
		return
	}
	cs, err := store.OpenStoreForMarker(marker)
	if err != nil {
		c.fatal("workspace attach:", err)
	}
	repo, err := cs.OpenRepo(marker.Repo)
	if err != nil {
		c.fatal("workspace attach: repository not found:", marker.Repo, "(import it first)")
	}
	// If current_revision is set but missing, auto-fork a new tip.
	if marker.CurrentRevision != "" {
		_, err := repo.GetRevision(marker.CurrentRevision)
		if err != nil {
			fmt.Printf("detected current revision %s missing; forking new tip from workspace\n", short(marker.CurrentRevision))
			ws := revision.NewWorkspace(repo)
			treeID, err := ws.BuildTreeFromFS(".")
			if err != nil {
				c.fatal("workspace attach:", err)
			}
			// Filter the new tree with the ignore matcher before committing.
			m, err := ignore.New(".")
			if err != nil {
				c.fatal("workspace attach: load ignore rules:", err)
			}
			if HasIgnores() {
				treeID, err = ws.RebuildTreeFiltered(".", m, treeID)
				if err != nil {
					c.fatal("workspace attach:", err)
				}
			}
			// New revision on top of the repo tip (first branch or root).
			parents := repoTip(repo)
			_, ch, err := ws.Commit(revision.CommitParams{
				Parents:     parents,
				TreeID:      treeID,
				Description: "fork",
				Author:      authorIdentity(os.Args[2:]),
			})
			if err != nil {
				c.fatal("workspace attach:", err)
			}
			marker.CurrentRevision = ch.ID
			if err := store.WriteMarker(".", marker); err != nil {
				c.fatal("workspace attach:", err)
			}
			fmt.Printf("forked new revision %s\n", short(ch.ID))
		}
	}
	if err := c.cs.PutWorkspace(&store.WorkspaceRow{
		Path: ".", Repo: marker.Repo, CurrentRevision: marker.CurrentRevision, Branch: marker.Branch,
	}); err != nil {
		c.fatal("workspace attach:", err)
	}
	fmt.Printf("workspace bound to %s (current revision %s)\n", repo, short(marker.CurrentRevision))
}

func cmdWorkspaceList(c *ctx) {
	ws, err := c.cs.ListWorkspaces()
	if err != nil {
		c.fatal("workspace list:", err)
	}
	if len(ws) == 0 {
		fmt.Println("no workspaces")
		return
	}
	for _, w := range ws {
		fmt.Printf("%s  ->  %s  (%s)\n", w.Path, w.Repo, short(w.CurrentRevision))
	}
}

func cmdRepositories(c *ctx) {
	cs, err := c.open()
	if err != nil {
		c.fatal("repositories:", err)
	}
	repos, err := cs.List()
	if err != nil {
		c.fatal("repositories:", err)
	}
	if len(repos) == 0 {
		fmt.Println("no repositories")
		return
	}
	for _, r := range repos {
		fmt.Println(r)
	}
}
