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
	// Truncate first, then fetch all snapshots in ONE query (no N+1).
	if len(revs) > limit {
		revs = revs[:limit]
	}
	hashes := make([]string, 0, len(revs))
	for _, rev := range revs {
		hashes = append(hashes, rev.Hash.String())
	}
	snaps, _ := repo.SnapshotsByHashes(hashes)
	out := make([]map[string]any, 0, len(revs))
	for _, rev := range revs {
		entry := map[string]any{"revision_id": rev.ID, "sha": rev.Hash.String(), "created": rev.Created.UnixMilli()}
		if snap, ok := snaps[rev.Hash.String()]; ok {
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
	// Conflicts are a property of the SNAPSHOT (its tree carries conflict
	// objects): expose them here so any client can see whether a snapshot is
	// conflicted and at which paths, without a separate state machine.
	out := map[string]any{
		"revision_id": rev.ID, "sha": snap.RevisionHash.String(), "tree": snap.TreeID.String(),
		"parents": parents, "description": snap.Description, "author": snap.Author.String(),
		"session": snap.Author.Session,
		"time":    snap.CommitTime.UnixMilli(),
	}
	atoms, _ := ws.ConflictsInTree(snap.TreeID)
	paths := make([]string, 0, len(atoms))
	for _, a := range atoms {
		paths = append(paths, a.Path)
	}
	out["conflicted"] = len(paths) > 0
	out["conflicts"] = paths
	writeJSON(w, http.StatusOK, out)
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

// handleConflict returns the STRUCTURED view of a conflict at ?path=: each way
// (base/ours/theirs/…) with its label, path, kind, and — for a text blob — its
// content inline. Binary and directory ways are NOT inlined (the marker text
// would be a lossy placeholder); they carry their `sha` so the client can fetch
// the raw bytes via /object/{sha} or list the directory via /tree.
func (s *Server) handleConflict(w http.ResponseWriter, r *http.Request) {
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
	if entry.Kind != object.KindConflict {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("%s is not a conflict", path))
		return
	}
	c, err := ws.ReadConflict(entry.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	terms := make([]map[string]any, 0, len(c.Removes)+len(c.Adds))
	for _, t := range c.Removes {
		terms = append(terms, s.conflictTermView(ws, "base", t))
	}
	for i, t := range c.Adds {
		role := t.Label
		if role == "" {
			role = fmt.Sprintf("side%d", i)
		}
		terms = append(terms, s.conflictTermView(ws, role, t))
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":         path,
		"conflict_sha": entry.ID.String(),
		"terms":        terms,
	})
}

// conflictTermView renders one conflict term: its role/label/path, the object
// kind, and (for a text blob) the content inline. Binary/dir/absent terms carry
// no content but do carry their sha (fetch via /object/{sha} or /tree).
func (s *Server) conflictTermView(ws *revision.Workspace, role string, t object.Term) map[string]any {
	v := map[string]any{"role": role, "label": t.Label, "path": t.Path, "sha": t.ID.String()}
	if t.ID == (object.ID{}) {
		v["kind"] = "absent" // a deletion side
		return v
	}
	o, err := ws.ReadObject(t.ID)
	if err != nil {
		v["kind"] = "unknown"
		return v
	}
	switch o.Kind {
	case object.KindBlob:
		v["kind"] = "blob"
		v["size"] = len(o.Blob)
		if revision.IsBinary(o.Blob) {
			v["is_binary"] = true
		} else {
			v["is_text"] = true
			v["content"] = string(o.Blob)
		}
	case object.KindTree:
		v["kind"] = "dir"
		if tt, err := ws.ReadTree(t.ID); err == nil {
			names := make([]string, 0, len(tt.Entries))
			for _, e := range tt.SortedEntries() {
				names = append(names, e.Name)
			}
			v["entries"] = names
		}
	case object.KindConflict:
		v["kind"] = "conflict" // a nested conflict term
		v["content"] = ""
	default:
		v["kind"] = "other"
	}
	return v
}

// handleObject serves the RAW bytes of any object by id (?sha=), so a client can
// fetch the actual bytes of a BINARY conflict term (which the marker text only
// placeholders). Content-addressed and immutable, so it is safe to cache.
func (s *Server) handleObject(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ws := revision.NewWorkspace(repo)
	sha := strings.TrimSpace(r.URL.Query().Get("sha"))
	id, err := object.HexToID(sha)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid sha: %w", err))
		return
	}
	o, err := ws.ReadObject(id)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	switch o.Kind {
	case object.KindBlob:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("X-Object-Kind", "blob")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(o.Blob)
	case object.KindTree:
		// Serve the directory as a JSON listing (a tree has no bytes to stream).
		names := make([]map[string]any, 0)
		for _, e := range o.Tree.SortedEntries() {
			names = append(names, map[string]any{"name": e.Name, "kind": entryKindName(e.Kind), "sha": e.ID.String()})
		}
		writeJSON(w, http.StatusOK, map[string]any{"kind": "dir", "entries": names})
	case object.KindConflict:
		text, cerr := ws.RenderConflict(id)
		if cerr != nil {
			writeErr(w, http.StatusInternalServerError, cerr)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Object-Kind", "conflict")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(text))
	default:
		writeErr(w, http.StatusBadRequest, errors.New("unsupported object kind"))
	}
}

func entryKindName(k object.Kind) string {
	switch k {
	case object.KindTree:
		return "dir"
	case object.KindConflict:
		return "conflict"
	default:
		return "file"
	}
}

// handleSearch searches a namespace's tree at ?ref= for a query. Two modes:
//   - ?name=<q>   matches FILE PATHS (substring, case-insensitive).
//   - ?content=<q> matches file CONTENTS (text files only; returns matching lines).
//
// At least one is required. Results are capped by ?limit= (default 50).
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	ws := revision.NewWorkspace(repo)
	nameQ := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("name")))
	contentQ := strings.TrimSpace(r.URL.Query().Get("content"))
	if nameQ == "" && contentQ == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name or content query is required"))
		return
	}
	treeID, err := resolveRefTree(repo, ws, r.URL.Query().Get("ref"))
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	limit := limitFrom(r)
	scope := scopePath(r)
	var root *object.Tree
	if scope == "" {
		root = ws.MustTree(treeID)
	} else {
		root = ws.MustTree(treeID)
		e, ferr := ws.FindEntry(root, scope)
		if ferr != nil || e.Kind != object.KindTree {
			writeErr(w, http.StatusNotFound, fmt.Errorf("scope %q not found", scope))
			return
		}
		root = ws.MustTree(e.ID)
	}
	type hit struct {
		Path    string   `json:"path"`
		Line    int      `json:"line,omitempty"`
		Text    string   `json:"text,omitempty"`
		Matches []string `json:"matches,omitempty"`
	}
	var out []hit
	var walk func(prefix string, t *object.Tree)
	walk = func(prefix string, t *object.Tree) {
		for _, e := range t.SortedEntries() {
			if len(out) >= limit {
				return
			}
			p := joinPath(prefix, e.Name)
			switch e.Kind {
			case object.KindTree:
				if sub, serr := ws.ReadTree(e.ID); serr == nil {
					walk(p, sub)
				}
			case object.KindBlob:
				if nameQ != "" && strings.Contains(strings.ToLower(p), nameQ) {
					out = append(out, hit{Path: p})
					continue
				}
				if contentQ != "" {
					data, berr := ws.ReadBlob(e.ID)
					if berr != nil || revision.IsBinary(data) {
						continue
					}
					lines := strings.Split(string(data), "\n")
					var matches []string
					firstLine := 0
					for i, ln := range lines {
						if strings.Contains(ln, contentQ) {
							if firstLine == 0 {
								firstLine = i + 1
							}
							if len(matches) < 5 {
								matches = append(matches, strings.TrimSpace(ln))
							}
						}
					}
					if len(matches) > 0 {
						out = append(out, hit{Path: p, Line: firstLine, Text: strings.Join(matches, "\n"), Matches: matches})
					}
				}
			}
		}
	}
	walk("", root)
	writeJSON(w, http.StatusOK, map[string]any{"results": out, "query": map[string]string{"name": nameQ, "content": contentQ}})
}
