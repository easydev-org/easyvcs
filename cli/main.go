// Command easyvcs is the EasyVCS command-line interface.
//
// EasyVCS uses a central SQLite database (default ~/.easyvcs/easyvcs.db,
// overridable via EASYVCS_HOME) holding all repositories. A working directory
// is bound to a repository via a .easyvcs-workspace pointer file. Commands are
// minimal and mirror the revision-native model: committing creates a snapshot
// under a stable revision id; rebase repoints a revision's parents changing only
// the snapshot's sha.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// HasIgnores reports whether the current directory has any ignore files.
func HasIgnores() bool {
	_, err := os.Stat(".gitignore")
	if err == nil {
		return true
	}
	_, err = os.Stat(".vcsignore")
	return err == nil
}

const usage = `easyvcs - a revision-native version control system (central-store)

Repository / workspace:
  init <ns/name> [DIR]      create a repository and a workspace pointing at it
  clone <url> [DIR]         clone a remote (http(s):// or a local path) into a new repo + workspace
  workspace attach [DIR]    bind current dir to a repo (auto-forks on mismatch)
  workspace list            list registered workspaces
  repositories              list all repositories in the central store

Revisions:
  commit [--new] [DIR]      create a snapshot (default: amend current revision)
  amend [DIR]               update the workspace's current revision snapshot
  log [DIR]                 list revisions and their current snapshot
  show <sha> [DIR]          show a snapshot's metadata
  checkout <sha> [DIR]      set current_revision + materialize tree into dir
  rebase <revision> [--onto <parent>]...  repoint a revision onto one or more parents
  squash <revision> [DIR]     absorb a revision into its parent (parent id stable)
  resolve <revision> <path> [--side N]  resolve a conflict to a chosen side
  message <revision> <text>    rewrite a revision's commit message (id stable)

Maintenance:
  gc [--dry-run]            prune unreferenced objects (whole store)
  verify                   check object/snapshot consistency

References:
  branch <name> <revision> [DIR]  derive a mutable branch -> independent revision
  tag <name> <revision> [DIR]       set an immutable tag -> revision
  refs [DIR]                list all branchs and tags

  log [--count] <path>       list revisions that modified a file (or count)

Content:
  diff <shaA> <shaB> [DIR]  list file differences between two snapshot trees

Portable:
  export <ns/name> -o FILE  export a repository to a single .evcs file
  import FILE [-n ns]       import a repository from a .evcs file

Network (own smart protocol):
  remote add <name> <url>   register a remote server URL
  remote                    list remotes
  fetch <remote> [ref...]   fetch all (or given) remote branchs into origin/*
  pull <remote> [branch]  fetch + merge a remote branch into local (default branch)
  push <remote>             push changes to an EasyVCS server

Git interop (no smart protocol, full + minimal-diff commit):
  git-pull <git-url>        fetch a git repo as one local revision
  git-push <git-url>        push current revision as one git commit

Flags:
  -help, -h                 show this help
`

type ctx struct {
	cs *store.CentralStore
	// onErr, when set, replaces the process-exit behavior of command failures
	// (tests install a panic-based hook so failures are observable).
	onErr func()
}

// fatal reports a command failure and terminates the command. In production it
// exits the process with status 1; tests may override via onErr.
func (c *ctx) fatal(args ...any) {
	fmt.Fprintln(os.Stderr, args...)
	if c.onErr != nil {
		c.onErr()
		return
	}
	os.Exit(1)
}

// fatalf is fatal with formatting.
func (c *ctx) fatalf(format string, args ...any) {
	c.fatal(fmt.Sprintf(format, args...))
}

// DefaultAuthor is the identity used when none is configured (kept for
// backward compatibility with existing commits/tests).
var DefaultAuthor = store.Author{Name: "easyvcs", Email: "easyvcs@example.com"}

// authorIdentity resolves the commit identity, in order of precedence:
//
//	--author <name> / --session <s> flags  >  EASYVCS_AUTHOR_NAME /
//	EASYVCS_AUTHOR_EMAIL / EASYVCS_SESSION  >  $EASYVCS_HOME/config (key=value)
//	> DefaultAuthor.
//
// A config line is "name", "email" or "session" = value. Email defaults to
// "<name>@easyvcs" when a name is set but no email. An unset identity yields
// DefaultAuthor, so existing behaviour (and tests) are unchanged.
func authorIdentity(flagArgs []string) store.Author {
	name := os.Getenv("EASYVCS_AUTHOR_NAME")
	email := os.Getenv("EASYVCS_AUTHOR_EMAIL")
	session := os.Getenv("EASYVCS_SESSION")

	for i := 0; i < len(flagArgs); i++ {
		switch flagArgs[i] {
		case "--author":
			if i+1 < len(flagArgs) {
				name = flagArgs[i+1]
				i++
			}
		case "--session":
			if i+1 < len(flagArgs) {
				session = flagArgs[i+1]
				i++
			}
		}
	}

	if cfgName, cfgEmail, cfgSession, ok := readAuthorConfig(); ok {
		if name == "" {
			name = cfgName
		}
		if email == "" {
			email = cfgEmail
		}
		if session == "" {
			session = cfgSession
		}
	}

	if name == "" {
		a := DefaultAuthor
		a.Session = session
		return a
	}
	if email == "" {
		email = name + "@easyvcs"
	}
	return store.Author{Name: name, Email: email, Session: session}
}

// readAuthorConfig reads name/email/session from $EASYVCS_HOME/config (a simple
// key=value file). Missing file is (ok=false), not an error.
func readAuthorConfig() (name, email, session string, ok bool) {
	data, err := os.ReadFile(filepath.Join(store.HomeDir(), "config"))
	if err != nil {
		return "", "", "", false
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		switch k {
		case "name", "author.name":
			name = v
		case "email", "author.email":
			email = v
		case "session", "author.session":
			session = v
		}
	}
	return name, email, session, true
}

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(0)
	}
	cmd := os.Args[1]
	switch cmd {
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		os.Exit(0)
	}

	cs, err := store.OpenDefault()
	if err != nil {
		fmt.Fprintln(os.Stderr, "open store:", err)
		os.Exit(1)
	}
	if err := cs.SetWAL(); err != nil {
		fmt.Fprintln(os.Stderr, "enable WAL:", err)
		os.Exit(1)
	}
	c := &ctx{cs: cs}

	switch cmd {
	case "init":
		cmdInit(c)
	case "clone":
		cmdClone(c)
	case "workspace":
		cmdWorkspace(c)
	case "repositories":
		cmdRepositories(c)
	case "commit":
		cmdCommit(c)
	case "amend":
		cmdAmend(c)
	case "log":
		// log --path <scope>: the subdirectory's projected history.
		if sc := scopeFlag(os.Args[2:]); sc != "" {
			cmdLogScope(c, sc)
			return
		}
		// log [--count] <path>: report revisions that modified a file.
		if len(os.Args) >= 3 {
			count := false
			pathArg := ""
			for _, a := range os.Args[2:] {
				if a == "--count" || a == "-count" || a == "-n" {
					count = true
				} else if pathArg == "" {
					pathArg = a
				}
			}
			if pathArg != "" {
				cmdLogPath(c, pathArg, count)
				return
			}
		}
		cmdLog(c)
	case "show":
		cmdShow(c)
	case "checkout":
		cmdCheckout(c)
	case "rebase":
		cmdRebase(c)
	case "squash":
		cmdSquash(c)
	case "resolve":
		cmdResolve(c)
	case "message":
		cmdMessage(c)
	case "branch":
		cmdBranch(c)
	case "tag":
		cmdTag(c)
	case "refs":
		cmdRefs(c)
	case "gc":
		cmdGC(c)
	case "verify":
		cmdVerify(c)
	case "diff":
		cmdDiff(c)
	case "export":
		cmdExport(c)
	case "import":
		cmdImport(c)
	case "remote":
		cmdRemote(c)
	case "request", "rr":
		cmdRequest(c)
	case "fetch":
		cmdFetch(c)
	case "pull":
		cmdPull(c)
	case "push":
		cmdPush(c)
	case "git-pull":
		cmdGitPull(c)
	case "git-push":
		cmdGitPush(c)
	default:
		c.fatalf("unknown command %q\n\n%s", cmd, usage)
	}
}

// resolveWorkingDir returns the directory a command should operate on.
func resolveWorkingDir() string {
	return "."
}

// loadRepo resolves the workspace for the current directory and returns the
// repo-scoped handle plus the workspace marker. The store the workspace belongs
// to is opened from the marker's recorded location (Home/DSN), falling back to
// the process EASYVCS_HOME when the marker does not record one.
func (c *ctx) loadRepo() (*store.Repo, *store.WorkspaceMarker, error) {
	marker, err := store.LookupMarker(".")
	if err != nil {
		return nil, nil, err
	}
	cs, err := store.OpenStoreForMarker(marker)
	if err != nil {
		return nil, nil, err
	}
	c.cs = cs
	repo, err := c.cs.OpenRepo(marker.Repo)
	if err != nil {
		return nil, nil, err
	}
	return repo, marker, nil
}

// repoTip returns the parent snapshot ids for a fresh fork: the tip of the
// first branch's revision if any, otherwise no parents.
func repoTip(repo *store.Repo) []object.ID {
	refs, err := repo.ListRefs()
	if err == nil && len(refs) > 0 {
		for _, r := range refs {
			if r.Kind == store.RefBranch {
				if ch, err := repo.GetRevision(r.Target); err == nil {
					return []object.ID{ch.Hash}
				}
			}
		}
	}
	return nil
}

func snapshotOrTree(ws *revision.Workspace, id object.ID) object.ID {
	if snap, err := ws.GetSnapshot(id); err == nil {
		return snap.TreeID
	}
	return id
}

func splitRepo(s string) (string, string, error) {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return s[:i], s[i+1:], nil
		}
	}
	return "", "", fmt.Errorf("expected namespace/name, got %q", s)
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
