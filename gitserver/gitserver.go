// Package gitserver exposes an easyvcs repository over the git SMART HTTP
// protocol (git-upload-pack only — read/clone/fetch), so a standard `git clone`
// and, crucially, package managers that treat a git repo as a package
// (go modules with GOPROXY=direct, npm/pip/cargo/composer git deps, …) can use
// easyvcs directly as a source.
//
// It is READ-ONLY: there is no receive-pack (push). Writes still go through the
// easyvcs API / Revision Requests. The git objects are built on the fly from
// easyvcs revisions/snapshots into an in-memory store, so easyvcs keeps its
// "no .git object store" property — nothing is persisted.
package gitserver

import (
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/format/pktline"
	gitobject "github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/protocol/packp/capability"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/plumbing/transport"
	gitsrv "github.com/go-git/go-git/v5/plumbing/transport/server"
	"github.com/go-git/go-git/v5/storage/memory"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// Content types for the smart-HTTP protocol.
const (
	contentTypeAdv = "application/x-git-upload-pack-advertisement"
	contentTypeRsp = "application/x-git-upload-pack-result"
	uploadPackName = "git-upload-pack"
)

// Build materializes repo's refs (branches + tags) into an in-memory git object
// store as a commit chain (one git commit per easyvcs snapshot). It is
// deterministic: the same refs produce the same commit hashes, so a client's
// fetch is stable across calls.
//
// subpath, when non-empty, scopes the export to a SUBDIRECTORY: each commit's
// tree is the subtree at that path (snapshots that lack the path are skipped),
// so a monorepo's subdirectory is served as its own git repository
// ("<repo>/<subdir>" is an independent git endpoint / package).
func Build(ws *revision.Workspace, repo *store.Repo, subpath string) (storer.Storer, error) {
	b := &builder{
		ws:       ws,
		repo:     repo,
		sub:      strings.Trim(strings.TrimSpace(subpath), "/"),
		touching: nil,
		sto:      memory.NewStorage(),
		commits:  map[object.ID]plumbing.Hash{},
		trees:    map[object.ID]plumbing.Hash{},
		blobs:    map[object.ID]plumbing.Hash{},
	}
	refs, err := repo.ListRefs()
	if err != nil {
		return nil, err
	}
	defaultBranch := "main"
	if meta, merr := repo.RepoMeta(); merr == nil && meta.DefaultBranch != "" {
		defaultBranch = meta.DefaultBranch
	}
	// When scoping to a subdir, use the path index to know which revisions touch
	// it, so we SKIP the (expensive) subtree lookup for every other snapshot.
	if b.sub != "" {
		ids, err := repo.RevisionsTouching(b.sub)
		if err != nil {
			return nil, err
		}
		b.touching = make(map[string]bool, len(ids))
		for _, id := range ids {
			b.touching[id] = true
		}
	}
	var head plumbing.Hash
	for _, ref := range refs {
		rev, err := repo.GetRevision(ref.Target)
		if err != nil {
			continue // a ref pointing at a missing revision (e.g. a stale tag)
		}
		h, err := b.commitFor(rev.Hash)
		if err != nil {
			return nil, err
		}
		if h.IsZero() {
			continue // the subpath does not exist in this ref's history
		}
		switch ref.Kind {
		case store.RefBranch:
			name := plumbing.NewBranchReferenceName(ref.Name)
			if err := b.sto.SetReference(plumbing.NewHashReference(name, h)); err != nil {
				return nil, err
			}
			if ref.Name == defaultBranch {
				head = h
			}
		case store.RefTag:
			name := plumbing.NewTagReferenceName(ref.Name)
			if err := b.sto.SetReference(plumbing.NewHashReference(name, h)); err != nil {
				return nil, err
			}
		}
	}
	if !head.IsZero() {
		_ = b.sto.SetReference(plumbing.NewSymbolicReference(plumbing.HEAD, plumbing.NewBranchReferenceName(defaultBranch)))
	}
	return b.sto, nil
}

type builder struct {
	ws   *revision.Workspace
	repo *store.Repo
	sub  string
	// touching is the set of revision ids that change something under sub (nil =
	// all revisions). It lets commitFor skip snapshots the subdir never touched.
	touching map[string]bool
	sto      *memory.Storage
	commits  map[object.ID]plumbing.Hash
	trees    map[object.ID]plumbing.Hash
	blobs    map[object.ID]plumbing.Hash
}

// commitFor emits (and memoizes) the git commit for a snapshot (keyed by its
// RevisionHash). Parent snapshots are emitted recursively.
func (b *builder) commitFor(snapHash object.ID) (plumbing.Hash, error) {
	if h, ok := b.commits[snapHash]; ok {
		return h, nil
	}
	snap, err := b.repo.GetSnapshot(snapHash)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	treeID := snap.TreeID
	if b.sub != "" {
		if b.touching != nil && !b.touching[snap.RevisionID] {
			b.commits[snapHash] = plumbing.ZeroHash
			return plumbing.ZeroHash, nil
		}
		// Scope to the subtree at b.sub. A snapshot that lacks the path is
		// SKIPPED (zero hash) so the subdir gets a clean history of only the
		// commits that touch it.
		root, err := b.ws.ReadTree(snap.TreeID)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		entry, err := b.ws.FindEntry(root, b.sub)
		if err != nil || entry.Kind != object.KindTree {
			b.commits[snapHash] = plumbing.ZeroHash
			return plumbing.ZeroHash, nil
		}
		treeID = entry.ID
	}
	tree, err := b.treeFor(treeID)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	var parents []plumbing.Hash
	for _, p := range snap.Parents {
		ph, err := b.commitFor(p)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		if !ph.IsZero() {
			parents = append(parents, ph)
		}
	}
	sig := gitobject.Signature{Name: snap.Author.Name, Email: snap.Author.Email, When: snap.CommitTime}
	msg := snap.Description
	if msg == "" {
		msg = "easyvcs snapshot " + snap.RevisionID
	}
	c := &gitobject.Commit{
		Author:       sig,
		Committer:    sig,
		Message:      msg,
		TreeHash:     tree,
		ParentHashes: parents,
	}
	o := b.sto.NewEncodedObject()
	if err := c.Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}
	h, err := b.sto.SetEncodedObject(o)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	b.commits[snapHash] = h
	return h, nil
}

func (b *builder) treeFor(treeID object.ID) (plumbing.Hash, error) {
	if h, ok := b.trees[treeID]; ok {
		return h, nil
	}
	t, err := b.ws.ReadTree(treeID)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	gt := &gitobject.Tree{}
	for _, e := range t.SortedEntries() {
		switch e.Kind {
		case object.KindTree:
			sh, err := b.treeFor(e.ID)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			gt.Entries = append(gt.Entries, gitobject.TreeEntry{Name: e.Name, Mode: filemode.Dir, Hash: sh})
		case object.KindBlob:
			data, err := b.ws.ReadBlob(e.ID)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			bh, err := b.blobBytes(e.ID, data)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			gt.Entries = append(gt.Entries, gitobject.TreeEntry{Name: e.Name, Mode: filemode.Regular, Hash: bh})
		case object.KindConflict:
			// A conflicted file is served as its marker text (same as /raw).
			text, err := b.ws.RenderConflict(e.ID)
			if err != nil {
				return plumbing.ZeroHash, err
			}
			bh, err := b.blobBytes(e.ID, []byte(text))
			if err != nil {
				return plumbing.ZeroHash, err
			}
			gt.Entries = append(gt.Entries, gitobject.TreeEntry{Name: e.Name, Mode: filemode.Regular, Hash: bh})
		}
	}
	// git REQUIRES tree entries in git's tree order (a directory is compared as
	// if its name had a trailing "/"), which differs from the plain byte order
	// SortedEntries() yields (e.g. "a.txt" < "a/" byte-wise, but "a/" < "a.txt"
	// in git order). go-git's Tree.Encode REJECTS an out-of-order tree
	// ("entries in tree are not sorted"), so re-sort before encoding.
	sort.Slice(gt.Entries, func(i, j int) bool {
		return gitTreeLess(gt.Entries[i], gt.Entries[j])
	})
	o := b.sto.NewEncodedObject()
	if err := gt.Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}
	h, err := b.sto.SetEncodedObject(o)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	b.trees[treeID] = h
	return h, nil
}

func (b *builder) blobBytes(id object.ID, data []byte) (plumbing.Hash, error) {
	if h, ok := b.blobs[id]; ok {
		return h, nil
	}
	o := b.sto.NewEncodedObject()
	o.SetType(plumbing.BlobObject)
	w, err := o.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	h, err := b.sto.SetEncodedObject(o)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	b.blobs[id] = h
	return h, nil
}

// Server serves the smart-HTTP upload-pack protocol over a prebuilt storer.
type Server struct{ sto storer.Storer }

// New returns a Server over sto (built by Build).
func New(sto storer.Storer) *Server { return &Server{sto: sto} }

func (s *Server) transport() transport.Transport {
	return gitsrv.NewServer(staticLoader{s.sto})
}

type staticLoader struct{ sto storer.Storer }

func (l staticLoader) Load(*transport.Endpoint) (storer.Storer, error) { return l.sto, nil }

// InfoRefs serves `GET .../info/refs?service=git-upload-pack`: the smart prefix
// plus the advertised refs.
func (s *Server) InfoRefs(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("service") != uploadPackName {
		http.Error(w, "only git-upload-pack is supported (read-only)", http.StatusForbidden)
		return
	}
	sess, err := s.transport().NewUploadPackSession(&transport.Endpoint{}, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ar, err := sess.AdvertisedReferences()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentTypeAdv)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	enc := pktline.NewEncoder(w)
	if err := enc.EncodeString("# service=" + uploadPackName + "\n"); err != nil {
		return
	}
	if err := enc.Flush(); err != nil {
		return
	}
	_ = ar.Encode(w)
}

// UploadPack serves `POST .../git-upload-pack`: decodes the request, computes the
// packfile, and streams the response.
func (s *Server) UploadPack(w http.ResponseWriter, r *http.Request) {
	sess, err := s.transport().NewUploadPackSession(&transport.Endpoint{}, nil)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	req := packp.NewUploadPackRequest()
	if err := req.UploadRequest.Decode(r.Body); err != nil {
		http.Error(w, "decode upload-pack request: "+err.Error(), http.StatusBadRequest)
		return
	}
	// go-git's server rejects unknown capabilities. `filter` (partial clone) is
	// requested by e.g. pip's `git clone --filter=blob:none`; drop it and serve
	// the FULL packfile instead (filtering is a bandwidth optimization, not a
	// correctness requirement).
	req.Capabilities.Delete(capability.Filter)
	if req.IsEmpty() {
		http.Error(w, "empty upload-pack request", http.StatusBadRequest)
		return
	}
	resp, err := sess.UploadPack(r.Context(), req)
	if err != nil {
		log.Printf("gitserver: upload-pack: %v", err)
		if errors.Is(err, transport.ErrEmptyUploadPackRequest) {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentTypeRsp)
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_ = resp.Encode(w)
}

// RepoPath parses a git-style path ("/ns/name.git" or "/ns/name") into (ns,
// name). It returns ok=false when the path does not have exactly two segments.
func RepoPath(path string) (ns, name string, ok bool) {
	p := strings.Trim(strings.TrimSpace(path), "/")
	p = strings.TrimSuffix(p, ".git")
	parts := strings.Split(p, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// gitTreeLess orders two git tree entries by git's tree rules: compare names
// byte-wise, but a DIRECTORY entry is treated as its name followed by '/'
// (git's convention), so e.g. "a" (dir) sorts before "a.txt". This is the order
// go-git's Tree.Encode requires.
func gitTreeLess(a, b gitobject.TreeEntry) bool {
	an := a.Name
	if a.Mode == filemode.Dir {
		an += "/"
	}
	bn := b.Name
	if b.Mode == filemode.Dir {
		bn += "/"
	}
	return an < bn
}
