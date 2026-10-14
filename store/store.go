// Package store defines the persistent storage boundary for EasyVCS.
//
// EasyVCS uses a single central SQL database (default ~/.easyvcs/easyvcs.db,
// overridable via EASYVCS_HOME). All repositories live in that one database,
// keyed by a (namespace, name) pair. Content-addressed objects are global and
// deduplicated across repositories; revisions, snapshots, and refs are scoped
// to a repository via a repo id.
//
// The semantic layer depends on the repo-scoped RepoStore interface, never on
// a concrete backend. This lets the same code run against SQLite (local) or
// Postgres (server) with no changes, and no cgo.
package store

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/easylab-platform/easyvcs/object"
	"gorm.io/gorm"
)

// ErrNotFound is returned when a keyed entity is absent.
var ErrNotFound = errors.New("store: not found")

// ErrRepoExists is returned when trying to create a repository that already exists.
var ErrRepoExists = errors.New("store: repository already exists")

// ErrRepoNotFound is returned when a repository is absent.
var ErrRepoNotFound = errors.New("store: repository not found")

// ErrRevisionChanged is returned by an optimistic update (CAS) when a revision's
// current hash no longer matches the value the caller read, meaning another
// writer modified the revision concurrently. Callers should retry.
var ErrRevisionChanged = errors.New("store: revision changed concurrently")

// DefaultDBFile is the file name of the central database inside the home dir.
const DefaultDBFile = "easyvcs.db"

// SnapshotHashFor computes the content-addressed revision hash for a snapshot.
// It is the store-level canonical form (matching the revision package's
// snapshotID): the hash is derived from the revision id, tree, parents,
// description, and author. It is exported so the store can recompute a hash
// when re-keying a snapshot (e.g. forked revisions get a fresh identity).
func SnapshotHashFor(s *Snapshot) object.ID {
	var b strings.Builder
	fmt.Fprintf(&b, "revision=%s\n", s.RevisionID)
	fmt.Fprintf(&b, "tree=%s\n", s.TreeID)
	fmt.Fprintf(&b, "description=%s\n", s.Description)
	fmt.Fprintf(&b, "author=%s\n", s.Author)
	for _, p := range s.Parents {
		fmt.Fprintf(&b, "parent=%s\n", p)
	}
	return object.BlobID([]byte(b.String()))
}

// NSRef identifies a NAMESPACE — the SINGLE container (there is no separate
// repository entity; a "repository" is a PATH within a namespace, see
// docs/namespace-container.md). Owner scopes the name: a user owns namespaces
// under their own account. Owner 0 means "unspecified" for internal lookups
// that resolve the owner from the row.
type NSRef struct {
	Owner int64
	Name  string
}

// NSInfo is an NSRef plus the namespace's hosting metadata.
type NSInfo struct {
	NSRef
	Visibility    string
	DefaultBranch string
}

func (r NSRef) String() string { return r.Name }

// Author is a named identity. Session is an optional extra dimension (e.g. a
// workspace session name "org:repo:branch") used to attribute a change to the
// session that produced it; it is empty for callers that do not use sessions.
type Author struct {
	Name    string
	Email   string
	Session string
}

// String renders an author as "Name <email>". It deliberately does NOT include
// Session, so existing displays are unchanged.
func (a Author) String() string {
	if a.Email == "" {
		return a.Name
	}
	return fmt.Sprintf("%s <%s>", a.Name, a.Email)
}

// Snapshot is the content-addressed view of a revision: it is the graph node.
// A Snapshot belongs to exactly one Revision (1:1), so its hash changes on
// rewrite (rebase) while the revision_id stays stable.
type Snapshot struct {
	// RevisionHash is the content-addressed id (sha) of this snapshot. It is
	// mutable: it changes whenever the revision is rewritten.
	RevisionHash object.ID
	// RevisionID is the stable identity this snapshot belongs to (never
	// changes). It is a random stable id (hex), independent of content.
	RevisionID string
	// Parents contains the ids of the parent snapshots (DAG edges).
	Parents []object.ID
	// TreeID is the root tree of this snapshot.
	TreeID object.ID
	// Description is the commit message.
	Description string
	// Author identity.
	Author Author
	// CommitTime is the creation time.
	CommitTime time.Time
}

// Revision is the stable, user-facing unit of work. It holds a pointer to its
// single current snapshot. The revision_id never changes; the pointed-to
// snapshot's hash does change as the revision is rewritten. Revision and
// Snapshot are strictly 1:1: a revision always points at one current snapshot,
// and rewriting replaces that snapshot entirely (old object remains in the
// content-addressed store if still referenced).
type Revision struct {
	// ID is the stable revision id (hex). Generated once and never modified.
	ID string
	// Hash points at the current snapshot's revision_hash.
	Hash object.ID
	// Created time.
	Created time.Time
	// ForkFrom records the revision id this revision was forked from, if any.
	// Used by workspace auto-fork so the origin is traceable.
	ForkFrom string
	// ChangedPaths lists the repo-relative paths whose content changed in this
	// revision relative to its parent (or all paths for the root revision). It
	// is used to cheaply answer "which revisions modified a given file" by
	// filtering the DAG walk without re-reading every tree.
	ChangedPaths []string
}

// RefKind distinguishes mutable branchs from immutable tags.
type RefKind string

// Ref kinds.
const (
	RefBranch RefKind = "branch"
	RefTag    RefKind = "tag"
)

// Ref is a named pointer to a revision, scoped to a PATH within the repository
// ("" = the root scope). See docs/subrepo-scopes.md.
type Ref struct {
	Name   string
	Kind   RefKind
	Target string // revision id
	// Path is the scope this ref belongs to ("" = root). A sub-scope ref is
	// visible only within that path; a root ref projects down to every
	// sub-scope that contains it.
	Path string
}

// RepoStore is the repo-scoped persistence boundary used by the semantic layer.
// Object primitives are content-addressed and global; the metadata methods are
// scoped to a single repository.
type RepoStore interface {
	// Object access (content-addressed, immutable, global).
	WriteObject(o *object.Object) error
	ReadObject(id object.ID) (*object.Object, error)
	ObjectExists(id object.ID) (bool, error)

	// Snapshot access.
	PutSnapshot(s *Snapshot) error
	GetSnapshot(id object.ID) (*Snapshot, error)

	// Revision access.
	PutRevision(r *Revision) error
	GetRevision(id string) (*Revision, error)
	// UpdateRevisionHash atomically points a revision's Hash to a new
	// snapshot hash.
	UpdateRevisionHash(revisionID string, hash object.ID) error
	// UpdateRevisionHashCAS repoints a revision's Hash only if it still points
	// at expectHash (optimistic concurrency; ErrRevisionChanged otherwise).
	UpdateRevisionHashCAS(revisionID string, expectHash, newHash object.ID) error
	ListRevisions() ([]*Revision, error)

	// Refs. PutRef uses r.Path; GetRef/DeleteRef/ListRefs operate on the ROOT
	// scope (path "") for back-compat. The *In variants take an explicit scope.
	PutRef(r *Ref) error
	DeleteRef(name string) error
	GetRef(name string) (*Ref, error)
	ListRefs() ([]*Ref, error)
	DeleteRefIn(path, name string) error
	GetRefIn(path, name string) (*Ref, error)
	ListOwnRefs(path string) ([]*Ref, error)
	// ListRefsIn returns the refs VISIBLE in a scope: root refs (projected down
	// when hasDir reports the dir exists at the ref's tip) plus refs defined at
	// the scope or under it. hasDir may be nil (every root ref projects).
	ListRefsIn(path string, hasDir func(target string) bool) ([]*Ref, error)

	// Transactional writes (for atomic commit from changes).
	BeginTx() *gorm.DB
	WriteObjectsBatchTx(tx *gorm.DB, objs []*object.Object) error
	PutSnapshotTx(tx *gorm.DB, s *Snapshot) error
	PutRevisionTx(tx *gorm.DB, r *Revision) error

	// Close flushes and closes the backend.
	Close() error
}

// Repo is a repo-scoped handle bound to a specific repository in the central
// store. It implements RepoStore. OwnerUserID is the namespace owner (the
// repository's owner).
// Repo is a handle to a NAMESPACE container (the name is kept for source
// compatibility; semantically it is the namespace). path scopes are applied
// ABOVE it (refs/revisions filtered by path).
type Repo struct {
	cs          *CentralStore
	namespaceID int64
	OwnerUserID int64
	Name        string
}
