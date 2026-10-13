package server

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
)

// handleSubRepo exposes a SUBDIRECTORY of a repository as a (recursive) repo: its
// refs (the branches/tags whose tip has that subdir) and its history (the
// revisions that changed anything under it, newest-first) — powered by the
// `revision_path_dirs` index, so it is O(revisions under the path), not O(all).
//
// It is the native counterpart of the git endpoint: the same subpath is both an
// easyvcs sub-repo and a git remote.
func (s *Server) handleSubRepo(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	path := strings.Trim(r.URL.Query().Get("path"), "/")
	if path == "" {
		writeErr(w, http.StatusBadRequest, errPathRequired)
		return
	}
	ws := revision.NewWorkspace(repo)
	limit := limitFrom(r)

	// Refs whose tip contains the subdir.
	refs, err := repo.ListRefs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	outRefs := make([]map[string]any, 0)
	for _, ref := range refs {
		rev, err := repo.GetRevision(ref.Target)
		if err != nil {
			continue
		}
		snap, err := repo.GetSnapshot(rev.Hash)
		if err != nil {
			continue
		}
		root, err := ws.ReadTree(snap.TreeID)
		if err != nil {
			continue
		}
		if e, err := ws.FindEntry(root, path); err != nil || e.Kind != object.KindTree {
			continue // the subdir is not present at this ref's tip
		}
		outRefs = append(outRefs, map[string]any{
			"kind": string(ref.Kind), "name": ref.Name, "target": ref.Target, "subtree": subtreeAt(ws, snap.TreeID, path),
		})
	}

	// History: the revisions that touched the subdir (from the index).
	ids, err := repo.RevisionsTouching(path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	type commitView struct {
		RevisionID string
		Created    int64
		entry      map[string]any
	}
	commits := make([]commitView, 0, len(ids))
	for _, id := range ids {
		rev, err := repo.GetRevision(id)
		if err != nil {
			continue
		}
		snap, err := repo.GetSnapshot(rev.Hash)
		if err != nil {
			continue
		}
		commits = append(commits, commitView{
			RevisionID: id, Created: rev.Created.UnixMilli(),
			entry: map[string]any{
				"revision_id": id, "sha": rev.Hash.String(),
				"description": snap.Description, "author": snap.Author.String(),
				"time": snap.CommitTime.UnixMilli(), "subtree": subtreeAt(ws, snap.TreeID, path),
			},
		})
	}
	sort.Slice(commits, func(i, j int) bool { return commits[i].Created > commits[j].Created })
	outCommits := make([]map[string]any, 0, len(commits))
	for i, c := range commits {
		if i >= limit {
			break
		}
		outCommits = append(outCommits, c.entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"path": path, "refs": outRefs, "commits": outCommits})
}

// subtreeAt returns the object id of the subtree at path within treeID, or ""
// when absent.
func subtreeAt(ws *revision.Workspace, treeID object.ID, path string) string {
	root, err := ws.ReadTree(treeID)
	if err != nil {
		return ""
	}
	e, err := ws.FindEntry(root, path)
	if err != nil || e.Kind != object.KindTree {
		return ""
	}
	return e.ID.String()
}

var errPathRequired = errors.New("path is required")
