package store

import (
	"encoding/json"
)

// GORM models mirror the central store schema. They are used by AutoMigrate to
// create portable DDL (sqlite / postgres / mysql) so the metadata layer can run
// on any of the three backends with no cgo. Binary columns (objects.content,
// snapshots.meta, revisions.changed_paths) are []byte and map to BLOB/BYTEA/
// LONGBLOB per dialect. Cross-dialect types (id autoincrement, TIMESTAMP) are
// handled by GORM.
//
// Ownership model: a USER is the top-level identity AND the ownership boundary
// (there is no separate tenant — user ≡ tenant). A user owns NAMESPACES; a
// namespace is owned by exactly one user. A repository lives under a namespace,
// so its owner (the namespace owner) is unambiguous. Additional collaborators
// are granted per-repository roles (maintainer / developer) via namespace_members.

// repositories table. (Namespace, name) is globally unique: a repository is
// addressed as namespace/name (GitHub-like), and its owner is the namespace
// owner (denormalized into owner_user_id for fast authorization).

// namespaces table. A namespace (org) is owned by exactly one user and its name
// is globally unique. A user may own many namespaces. A namespace IS the
// container: there is no separate repository entity (see
// docs/namespace-container.md) — the "repository" of old is a PATH within it.
type namespaceRow struct {
	ID                int64  `gorm:"primaryKey;autoIncrement"`
	OwnerUserID       int64  `gorm:"not null"`
	Name              string `gorm:"not null;uniqueIndex:idx_namespace"`
	Created           int64  `gorm:"not null"`
	Description       string `gorm:"not null;default:''"`
	Visibility        string `gorm:"not null;default:'private'"`
	DefaultBranch     string `gorm:"column:default_branch;not null;default:'main'"`
	Kind              string `gorm:"not null;default:'normal'"`
	MirrorURL         string `gorm:"column:mirror_url;not null;default:''"`
	MirrorBranch      string `gorm:"column:mirror_branch;not null;default:'main'"`
	MirrorInterval    int64  `gorm:"column:mirror_interval;not null;default:300"`
	MirrorLastRev     string `gorm:"column:mirror_last_rev;not null;default:''"`
	MirrorLastSync    int64  `gorm:"column:mirror_last_sync;not null;default:0"`
	MirrorLastErr     string `gorm:"column:mirror_last_error;not null;default:''"`
	MirrorToken       string `gorm:"column:mirror_token;not null;default:''"`
	ProtectedBranches string `gorm:"column:protected_branches;not null;default:''"`
}

func (namespaceRow) TableName() string { return "namespaces" }

// namespace_members table grants a user a non-owner role on a NAMESPACE. The
// owner is NOT stored here: it is derived from the namespace owner
// (namespaceRow.OwnerUserID). Roles: "maintainer" | "developer".
type namespaceMemberRow struct {
	NamespaceID int64  `gorm:"primaryKey;not null"`
	UserID      int64  `gorm:"primaryKey;not null"`
	Role        string `gorm:"not null;default:'developer'"`
	GrantedBy   *int64
	Created     int64 `gorm:"not null"`
}

func (namespaceMemberRow) TableName() string { return "namespace_members" }

// push_mirrors table.
type pushMirrorRow struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	NamespaceID int64  `gorm:"not null;uniqueIndex:idx_pushmirror"`
	Name        string `gorm:"not null;uniqueIndex:idx_pushmirror"`
	URL         string `gorm:"not null"`
	Branch      string `gorm:"not null;default:'main'"`
	Token       string `gorm:"not null;default:''"`
	LastRev     string `gorm:"not null;default:''"`
	LastError   string `gorm:"not null;default:''"`
}

// objects table (global, content-addressed).
type objectRow struct {
	ID      string `gorm:"primaryKey;column:sha"`
	Kind    int    `gorm:"not null"`
	Content []byte `gorm:"not null"`
}

// snapshots table.
type snapshotRow struct {
	NamespaceID int64  `gorm:"primaryKey;not null"`
	ID          string `gorm:"primaryKey;column:sha;not null"`
	RevisionID  string `gorm:"not null"`
	TreeID      string `gorm:"not null"`
	CommitTime  int64  `gorm:"not null"`
	Meta        []byte `gorm:"not null"`
}

// revisions table.
type revisionRow struct {
	NamespaceID int64  `gorm:"primaryKey;not null"`
	ID          string `gorm:"primaryKey;not null"`
	Hash        string `gorm:"not null"`
	Created     int64  `gorm:"not null"`
	ForkFrom    string
	ChangedPath []byte `gorm:"column:changed_paths"`
}

// revisionPathDirRow indexes which DIRECTORY prefixes a revision touched: for
// every changed path, each of its ancestor directories is a row. It backs
// "subdirectory as a repository": the revisions that touch a subpath P are
// exactly those with a row (namespace_id, dir=P). Because every subtree in easyvcs is
// already a content-addressed object, this index is all that is needed to treat
// any subpath as a (recursive) repository — no content is duplicated.
type revisionPathDirRow struct {
	NamespaceID int64  `gorm:"primaryKey;not null;index:idx_rpd_dir,priority:3"`
	Dir         string `gorm:"primaryKey;not null;index:idx_rpd_dir,priority:1"`
	RevisionID  string `gorm:"primaryKey;not null;index:idx_rpd_dir,priority:2"`
}

func (revisionPathDirRow) TableName() string { return "revision_path_dirs" }

// refs table.
type refRow struct {
	NamespaceID int64 `gorm:"primaryKey;not null"`
	// Path is the SCOPE this ref belongs to ("" = the root scope). A ref lives
	// in exactly one scope; the root scope's refs project down to sub-scopes
	// (see docs/subrepo-scopes.md).
	Path   string `gorm:"primaryKey;not null;default:''"`
	Name   string `gorm:"primaryKey;not null"`
	Kind   string `gorm:"not null"`
	Target string `gorm:"not null"`
}

// workspaces table.
type workspaceRow struct {
	ID              int64   `gorm:"primaryKey;autoIncrement"`
	Path            string  `gorm:"uniqueIndex"`
	NamespaceID     int64   `gorm:"not null"`
	CurrentRevision *string `gorm:"column:current_revision"`
	Branch          *string
}

// remotes table.
type remoteRow struct {
	NamespaceID   int64  `gorm:"primaryKey;not null"`
	Name          string `gorm:"primaryKey;not null"`
	URL           string `gorm:"not null"`
	Token         *string
	LastSyncTip   string `gorm:"not null;default:''"`
	DefaultBranch string `gorm:"not null;default:''"`
	Ancestry      []byte `gorm:"column:ancestry"`
}

// remote_refs table.
type remoteRefRow struct {
	NamespaceID int64  `gorm:"primaryKey;not null"`
	RemoteName  string `gorm:"primaryKey;not null"`
	Kind        string `gorm:"primaryKey;not null"`
	Name        string `gorm:"primaryKey;not null"`
	Target      string `gorm:"not null"`
}

// users table. A user is simultaneously the identity (credentials, audit) and
// the ownership boundary: it owns namespaces and repositories, and is bound to
// one abcp-agent tenant. `Disabled` stops the user's credentials from
// authenticating anywhere.
type userRow struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	Username    string `gorm:"not null;uniqueIndex"`
	DisplayName string `gorm:"not null;default:''"`
	Disabled    bool   `gorm:"not null;default:false"`
	// AgentTenant is the abcp-agent tenant id bound to this user (set at
	// provisioning time). It is the explicit record of the user↔agent binding.
	AgentTenant string `gorm:"not null;default:''"`
	// AgentToken is the user's agent.v1 bearer credential (the bootstrap token
	// minted by the agent's AdminService at provisioning). The gateway presents
	// it on every forwarded agent RPC of this user. It is an internal service
	// credential — opaque to lab users — stored as-is so the gateway can forward
	// it without a secrets service.
	AgentToken string `gorm:"not null;default:''"`
	Created    int64  `gorm:"not null"`
}

func (userRow) TableName() string { return "users" }

// tokens table.
type tokenRow struct {
	ID      int64  `gorm:"primaryKey;autoIncrement"`
	Token   string `gorm:"not null;uniqueIndex"`
	UserID  int64  `gorm:"not null"`
	Level   string `gorm:"not null;default:'write'"` // retained for backward compat; no longer authorizes
	Created int64  `gorm:"not null"`
}

// revision_requests table. A Revision Request (RR) is the change-native
// collaboration unit: its subject is a PINNED snapshot (content-addressed,
// immutable), not a branch pointer. source_snapshot and base_snapshot are set
// at creation and never change; the target branch moving only means the merge is
// computed against the CURRENT tip. There is exactly one source form — a
// snapshot — so there is no source kind or source revision column.
type revisionRequestRow struct {
	ID          int64 `gorm:"primaryKey;autoIncrement"`
	NamespaceID int64 `gorm:"not null;uniqueIndex:idx_rr"`
	IID         int64 `gorm:"column:iid;not null;uniqueIndex:idx_rr"`
	// SourceSnapshot is the PINNED content-addressed snapshot under review.
	SourceSnapshot string `gorm:"not null"`
	// BaseSnapshot is the PINNED target tip snapshot at creation time.
	BaseSnapshot string `gorm:"not null;default:''"`
	TargetBranch string `gorm:"not null;default:'main'"`
	// Path scopes the RR to a SUBDIRECTORY (docs/subrepo-scopes.md §7.3): the
	// diff and merge are limited to that subtree. "" = the whole repo.
	Path     string `gorm:"not null;default:''"`
	State    string `gorm:"not null;default:'open'"`
	Title    string `gorm:"not null"`
	Body     string `gorm:"not null;default:''"`
	AuthorID *int64
	Created  int64 `gorm:"not null"`
	Updated  int64 `gorm:"not null"`
}

func (revisionRequestRow) TableName() string { return "revision_requests" }

// rr_updates table: the append-only history of explicit re-pins. Each row
// records a new (source_snapshot, base_snapshot) pair the author chose to
// re-anchor the review on; the RR's current pin is the highest seq.
type rrUpdateRow struct {
	ID             int64  `gorm:"primaryKey;autoIncrement"`
	RRID           int64  `gorm:"not null;index"`
	Seq            int64  `gorm:"not null"`
	SourceSnapshot string `gorm:"not null"`
	BaseSnapshot   string `gorm:"not null;default:''"`
	AuthorID       *int64
	Created        int64 `gorm:"not null"`
}

func (rrUpdateRow) TableName() string { return "rr_updates" }

// rr_merge table: the outcome of applying an RR (one row per RR). state is
// "pending" while a conflicted merge awaits resolution, or "done" once applied.
// merged_by is the user id that triggered the merge (nil for anonymous); the
// landing commit's author is the source snapshot's author (see rr.ComputeMerge).
type rrMergeRow struct {
	RRID             int64  `gorm:"primaryKey"`
	MergedSnapshot   string `gorm:"not null;default:''"`
	MergedRevisionID string `gorm:"not null;default:''"`
	State            string `gorm:"not null;default:'pending'"`
	MergedBy         *int64
	Created          int64 `gorm:"not null"`
	Updated          int64 `gorm:"not null"`
}

func (rrMergeRow) TableName() string { return "rr_merge" }

// gitRevisionLinkRow records the weak-traceability link between an easyvcs
// revision and a git commit produced when exporting/importing through the git
// bridge. It is plain easyvcs metadata (never stored as a .git object); it lets
// an easyvcs revision be correlated back to a commit sha for the same repo.
type gitRevisionLinkRow struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	NamespaceID int64  `gorm:"not null;uniqueIndex:idx_gitlink"`
	RevisionID  string `gorm:"not null;uniqueIndex:idx_gitlink"`
	CommitSHA   string `gorm:"not null;uniqueIndex:idx_gitlink"`
	// Ref is the git ref the commit was exported under (e.g. refs/heads/main).
	Ref string `gorm:"not null;default:''"`
	// Side is "export" (revision -> git) or "import" (git -> revision).
	Side    string `gorm:"not null;default:'export';uniqueIndex:idx_gitlink"`
	Created int64  `gorm:"not null"`
}

func (gitRevisionLinkRow) TableName() string { return "git_revision_links" }

// NOTE: the historical `package_owners` table (registry namespace ownership)
// is no longer managed by this module. It belonged to the package-registry
// concern (artifactkit), not the VCS engine, so its model was removed here.
// An existing database may still carry the table; it is simply unused now and
// is intentionally not dropped (no destructive migration).

// auditLogRow is an append-only access/audit record. It is never updated or
// deleted (no undo / reflog semantics).
type auditLogRow struct {
	ID        int64  `gorm:"primaryKey;autoIncrement"`
	Timestamp int64  `gorm:"not null;index"`
	Action    string `gorm:"not null"`
	Namespace string `gorm:"not null;default:''"`
	Repo      string `gorm:"not null;default:''"`
	UserID    *int64
	IP        string `gorm:"not null;default:''"`
	Outcome   string `gorm:"not null;default:''"`
	Detail    string `gorm:"not null;default:''"`
}

func (auditLogRow) TableName() string { return "audit_log" }

// allModels returns every table model for AutoMigrate.
func allModels() []any {
	return []any{
		&pushMirrorRow{}, &objectRow{}, &snapshotRow{}, &revisionRow{},
		&revisionPathDirRow{},
		&refRow{}, &workspaceRow{}, &remoteRow{}, &remoteRefRow{},
		&userRow{}, &tokenRow{}, &namespaceRow{}, &namespaceMemberRow{},
		&revisionRequestRow{}, &rrUpdateRow{}, &rrMergeRow{},
		&rrReviewRow{}, &rrCommentRow{},
		&gitRevisionLinkRow{}, &auditLogRow{},
	}
}

// AuditEvent is a plain append-only record of an access attempt.
type AuditEvent struct {
	Timestamp int64
	Action    string // "advertise" | "fetch" | "push"
	Namespace string
	Repo      string
	UserID    *int64
	IP        string
	Outcome   string // "ok" | "denied" | "error"
	Detail    string
}

// decodeChangedPathsJSON decodes the changed_paths blob.
func decodeChangedPathsJSON(raw []byte) []string {
	if raw == nil {
		return nil
	}
	var out []string
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

// TableName methods pin GORM to the original canonical table names so any
// remaining raw-SQL callers (mirror.go/workspace.go) and the GORM layer agree.

func (pushMirrorRow) TableName() string { return "push_mirrors" }
func (objectRow) TableName() string     { return "objects" }
func (snapshotRow) TableName() string   { return "snapshots" }
func (revisionRow) TableName() string   { return "revisions" }
func (refRow) TableName() string        { return "refs" }
func (workspaceRow) TableName() string  { return "workspaces" }
func (remoteRow) TableName() string     { return "remotes" }
func (remoteRefRow) TableName() string  { return "remote_refs" }
func (tokenRow) TableName() string      { return "tokens" }

// Collaboration is expressed as Revision Requests, whose reviews/comments live
// in `rr_reviews` / `rr_comments` (see store/rr_review.go).
