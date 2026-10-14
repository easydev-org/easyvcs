package server

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
)

// ---- M4: browse ----

func (s *Server) handleTree(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ws := revision.NewWorkspace(repo)
	treeID, err := resolveRefTree(repo, ws, r.URL.Query().Get("ref"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	tree, err := ws.ReadTree(treeID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	path := scopePath(r)
	if path != "" {
		entry, err := ws.FindEntry(tree, path)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		if entry.Kind != object.KindTree {
			// A file path returns a single-entry listing.
			writeJSON(w, http.StatusOK, map[string]any{"entries": []map[string]any{treeEntryView(ws, entry, path)}})
			return
		}
		if tree, err = ws.ReadTree(entry.ID); err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
	}
	entries := make([]map[string]any, 0, len(tree.Entries))
	for _, e := range tree.SortedEntries() {
		entries = append(entries, treeEntryView(ws, e, joinPath(path, e.Name)))
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

// treeEntryView renders one tree entry (name/kind/size/path).
func treeEntryView(ws *revision.Workspace, e object.Entry, path string) map[string]any {
	kind := "file"
	if e.Kind == object.KindTree {
		kind = "dir"
	} else if e.Kind == object.KindConflict {
		kind = "conflict"
	}
	var size int
	if e.Kind == object.KindBlob {
		if data, err := ws.ReadBlob(e.ID); err == nil {
			size = len(data)
		}
	}
	return map[string]any{"name": e.Name, "path": path, "kind": kind, "size": size, "sha": e.ID.String()}
}

// joinPath joins a directory prefix and a child name with a slash.
func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "/" + name
}

func (s *Server) handleBlob(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ws := revision.NewWorkspace(repo)
	path := scopePath(r)
	if path == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	treeID, err := resolveRefTree(repo, ws, r.URL.Query().Get("ref"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	tree, err := ws.ReadTree(treeID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	entry, err := ws.FindEntry(tree, path)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if entry.Kind == object.KindConflict {
		// A conflicted file reads back as its marker text (is_text), so the
		// caller can view and resolve it.
		text, cerr := ws.RenderConflict(entry.ID)
		if cerr != nil {
			writeErr(w, http.StatusInternalServerError, cerr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"path": path, "sha": entry.ID.String(), "size": len(text), "is_text": true, "content": text, "conflict": true,
		})
		return
	}
	if entry.Kind != object.KindBlob {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%s is not a file", path))
		return
	}
	data, err := ws.ReadBlob(entry.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// Text is returned inline; a binary file is flagged (use /raw for bytes).
	if revision.IsBinary(data) {
		writeJSON(w, http.StatusOK, map[string]any{
			"path": path, "sha": entry.ID.String(), "size": len(data), "is_text": false, "binary": true,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path": path, "sha": entry.ID.String(), "size": len(data), "is_text": true, "content": string(data),
	})
}

func (s *Server) handleRaw(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ws := revision.NewWorkspace(repo)
	path := scopePath(r)
	if path == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	treeID, err := resolveRefTree(repo, ws, r.URL.Query().Get("ref"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	tree, err := ws.ReadTree(treeID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	entry, err := ws.FindEntry(tree, path)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if entry.Kind == object.KindConflict {
		// A conflicted file is served as its marker text (same rendering as
		// Materialize / /archive), so a client can see and resolve it.
		text, cerr := ws.RenderConflict(entry.ID)
		if cerr != nil {
			writeErr(w, http.StatusInternalServerError, cerr)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Git-SHA", entry.ID.String())
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(text))
		return
	}
	if entry.Kind != object.KindBlob {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%s is not a file", path))
		return
	}
	data, err := ws.ReadBlob(entry.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Git-SHA", entry.ID.String())
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (s *Server) handleLog(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ws := revision.NewWorkspace(repo)
	limit := limitFrom(r)
	path := scopePath(r)
	ref := r.URL.Query().Get("ref")

	if path != "" {
		edits, err := ws.FileHistoryOpt(revision.HistoryOpt{Start: ref, Limit: limit, Desc: true}, path)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		out := make([]map[string]any, 0, len(edits))
		for _, e := range edits {
			out = append(out, map[string]any{
				"revision_id": e.RevisionID, "sha": e.RevisionHash.String(),
				"status": string(e.Status), "time": e.Timestamp.UnixMilli(),
			})
		}
		writeJSON(w, http.StatusOK, map[string]any{"commits": out})
		return
	}

	revs, err := ws.Log()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// A non-empty ref scopes the log to the revisions REACHABLE from it
	// (ancestors along first-parent and fork edges) — the branch's own
	// history. An empty ref keeps the historical behaviour: the whole
	// repository's revisions (the gateway/webui Log pane passes no ref).
	if ref != "" {
		startID, err := resolveRefRevisionID(repo, ws, ref)
		if err != nil {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		keep := revisionAncestors(repo, startID)
		filtered := revs[:0]
		for _, rev := range revs {
			if keep[rev.ID] {
				filtered = append(filtered, rev)
			}
		}
		revs = filtered
	}
	// Newest first, then truncate.
	sort.Slice(revs, func(i, j int) bool { return revs[i].Created.After(revs[j].Created) })
	out := make([]map[string]any, 0, len(revs))
	for i, rev := range revs {
		if i >= limit {
			break
		}
		entry := map[string]any{"revision_id": rev.ID, "sha": rev.Hash.String(), "created": rev.Created.UnixMilli()}
		if snap, err := repo.GetSnapshot(rev.Hash); err == nil {
			entry["description"] = snap.Description
			entry["author"] = snap.Author.String()
			entry["session"] = snap.Author.Session
			entry["time"] = snap.CommitTime.UnixMilli()
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"commits": out})
}

func (s *Server) handleCommit(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	sha := r.PathValue("sha")
	ws := revision.NewWorkspace(repo)
	revID, err := resolveRefRevisionID(repo, ws, sha)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	rev, err := repo.GetRevision(revID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	snap, err := repo.GetSnapshot(rev.Hash)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	parents := make([]string, 0, len(snap.Parents))
	for _, p := range snap.Parents {
		parents = append(parents, p.String())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"revision_id": rev.ID, "sha": snap.RevisionHash.String(), "tree": snap.TreeID.String(),
		"parents": parents, "description": snap.Description, "author": snap.Author.String(),
		"session": snap.Author.Session,
		"time":    snap.CommitTime.UnixMilli(),
	})
}

func (s *Server) handleDiff(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ws := revision.NewWorkspace(repo)
	baseRef := r.URL.Query().Get("base")
	headRef := r.URL.Query().Get("head")
	baseTree, err := resolveRefTree(repo, ws, baseRef)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("base: %w", err))
		return
	}
	headTree, err := resolveRefTree(repo, ws, headRef)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("head: %w", err))
		return
	}
	diffs, err := ws.DiffContent(baseTree, headTree)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, map[string]any{
			"path": d.Path, "status": string(d.Status), "old_path": d.OldPath,
			"binary": d.Binary, "added": d.AddedLines, "removed": d.RemovedLines, "content": d.Content,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out})
}

func (s *Server) handleBlame(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ws := revision.NewWorkspace(repo)
	path := scopePath(r)
	if path == "" {
		writeErr(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	lines, err := ws.AnnotateAt(revision.HistoryOpt{Start: r.URL.Query().Get("ref")}, path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(lines))
	for _, ln := range lines {
		out = append(out, map[string]any{
			"line": ln.LineNumber, "revision_id": ln.RevisionID, "message": ln.Message,
			"author": ln.Author, "session": ln.Session, "content": ln.Content,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"lines": out})
}

// handleArchive streams a repository tree at a ref as a gzipped tar. The route
// is `GET .../archive/{file}` where {file} is `<ref>.tar.gz`; the ".tar.gz"
// suffix is required (Go's ServeMux wildcards must match a whole path segment,
// so the extension cannot live in the pattern). An empty ref is not addressable
// here — the caller names the branch/tag/revision explicitly (e.g.
// `main.tar.gz`). It is read-only and streams: nothing is buffered whole and no
// temporary files are used.
func (s *Server) handleArchive(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	file := r.PathValue("file")
	ref, ok := strings.CutSuffix(file, ".tar.gz")
	if !ok || ref == "" {
		writeErr(w, http.StatusBadRequest, errors.New("archive path must be {ref}.tar.gz"))
		return
	}
	ws := revision.NewWorkspace(repo)
	treeID, err := resolveRefTree(repo, ws, ref)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	// Optional `path`: export only a SUBTREE (a directory), rooted at that
	// directory. Absent/empty = the whole tree (unchanged behaviour).
	sub := scopePath(r)
	w.Header().Set("Content-Type", "application/gzip")
	name := repo.Name
	if sub != "" {
		name = repo.Name + "-" + strings.ReplaceAll(sub, "/", "_")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name+"-"+ref+".tar.gz"))
	w.WriteHeader(http.StatusOK)
	// The body is already streaming; a failure mid-stream cannot change the
	// status, so it is dropped (the client sees a truncated archive).
	if sub == "" {
		_ = ws.WriteTreeTarGz(treeID, w)
		return
	}
	// A bad subtree path is a client error, but the 200 + headers are already
	// sent; surface it as a log line and stop (the archive is truncated/empty).
	if err := ws.WriteSubtreeTarGz(treeID, sub, w); err != nil {
		log.Printf("easyvcs-server: archive subtree %q @ %s: %v", sub, ref, err)
	}
}
