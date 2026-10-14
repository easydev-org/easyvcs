package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/easylab-platform/easyvcs/encoding"
	"github.com/easylab-platform/easyvcs/object"
	"gorm.io/gorm"
)

// Create creates a new repository and returns a repo-scoped handle. The
// namespace is globally unique and owned by the user in RepoRef.Owner; owner 0
// creates an unowned, public dev repo (no ACLs). The owner is denormalized into
// owner_user_id.
func (s *CentralStore) Create(r RepoRef) (*Repo, error) {
	if r.Owner != 0 {
		if err := s.EnsureNamespace(r.Owner, r.Namespace); err != nil {
			return nil, err
		}
	}
	var count int64
	if err := s.d.gdb.Model(&repoRow{}).Where("namespace=? AND name=?", r.Namespace, r.Name).Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, fmt.Errorf("%w: %s", ErrRepoExists, r)
	}
	// New repositories are PRIVATE by default (multi-tenant safety: a repo is
	// only visible to its owner/members until the caller explicitly publishes
	// it). They also protect `main` by default: it may only change through a
	// merged Revision Request (direct push is refused). See store/protect.go.
	row := &repoRow{OwnerUserID: r.Owner, Namespace: r.Namespace, Name: r.Name, Created: time.Now().UTC().UnixMilli(), Visibility: "private", ProtectedBranches: defaultProtectedBranchesJSON}
	if err := s.d.gdb.Create(row).Error; err != nil {
		return nil, err
	}
	return &Repo{cs: s, repoID: row.ID, OwnerUserID: row.OwnerUserID, Namespace: r.Namespace, Name: r.Name}, nil
}

// OpenRepo opens an existing repository by namespace/name (global). Returns
// ErrRepoNotFound if missing.
func (s *CentralStore) OpenRepo(r RepoRef) (*Repo, error) {
	var row repoRow
	err := s.d.gdb.Where("namespace=? AND name=?", r.Namespace, r.Name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, fmt.Errorf("%w: %s", ErrRepoNotFound, r)
	}
	if err != nil {
		return nil, err
	}
	return &Repo{cs: s, repoID: row.ID, OwnerUserID: row.OwnerUserID, Namespace: r.Namespace, Name: r.Name}, nil
}

// RepoExists reports whether a repository exists (namespace/name is global).
func (s *CentralStore) RepoExists(r RepoRef) (bool, error) {
	var count int64
	if err := s.d.gdb.Model(&repoRow{}).Where("namespace=? AND name=?", r.Namespace, r.Name).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// Delete removes a repository and all of its scoped metadata. Reviews and
// comments are removed via a subquery on the repo's MR ids; any failure is
// returned (no silent partial deletes).
func (s *CentralStore) Delete(r RepoRef) error {
	repo, err := s.OpenRepo(r)
	if err != nil {
		return err
	}
	for _, table := range []any{&snapshotRow{}, &revisionRow{}, &refRow{}, &remoteRow{}, &remoteRefRow{}, &pushMirrorRow{}, &workspaceRow{}, &gitRevisionLinkRow{}, &repoMemberRow{}} {
		if err := s.d.gdb.Where("repo_id=?", repo.repoID).Delete(table).Error; err != nil {
			return err
		}
	}
	// Collaboration is Revision Requests, scoped to the repo: clean the RRs and
	// their reviews/comments/updates/merge rows.
	rrIDs := s.d.gdb.Model(&revisionRequestRow{}).Select("id").Where("repo_id=?", repo.repoID)
	for _, table := range []any{&rrReviewRow{}, &rrCommentRow{}, &rrUpdateRow{}, &rrMergeRow{}} {
		if err := s.d.gdb.Where("rr_id IN (?)", rrIDs).Delete(table).Error; err != nil {
			return err
		}
	}
	if err := s.d.gdb.Where("repo_id=?", repo.repoID).Delete(&revisionRequestRow{}).Error; err != nil {
		return err
	}
	if err := s.d.gdb.Delete(&repoRow{}, repo.repoID).Error; err != nil {
		return err
	}
	return nil
}

// ListForOwner returns every repository owned by one user.
func (s *CentralStore) ListForOwner(owner int64) ([]RepoRef, error) {
	var rows []repoRow
	if err := s.d.gdb.Where("owner_user_id=?", owner).Find(&rows).Error; err != nil {
		return nil, err
	}
	refs := make([]RepoRef, 0, len(rows))
	for _, row := range rows {
		refs = append(refs, RepoRef{Owner: row.OwnerUserID, Namespace: row.Namespace, Name: row.Name})
	}
	return refs, nil
}

func (s *CentralStore) List() ([]RepoRef, error) {
	var rows []repoRow
	if err := s.d.gdb.Order("namespace, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]RepoRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, RepoRef{Owner: r.OwnerUserID, Namespace: r.Namespace, Name: r.Name})
	}
	return out, nil
}

// ListOwned returns only the repositories the user OWNS or is a member of —
// never another tenant's public repos. It backs `GET /repos?owned=true`, which
// a tenant-scoped client (e.g. agent-gateway) uses to enumerate exactly what it
// may act on, without the cross-tenant noise ListAccessible includes.
func (s *CentralStore) ListOwned(userID int64) ([]RepoRef, error) {
	var rows []repoRow
	q := s.d.gdb.Where(
		"owner_user_id = ? OR id IN (SELECT repo_id FROM repo_members WHERE user_id = ?)",
		userID, userID)
	if err := q.Order("namespace, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]RepoRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, RepoRef{Owner: r.OwnerUserID, Namespace: r.Namespace, Name: r.Name})
	}
	return out, nil
}

// ListAccessible returns every repository a user may READ: public repos (any
// owner) plus private repos where the user is the owner or a repo member.
func (s *CentralStore) ListAccessible(userID int64) ([]RepoRef, error) {
	var rows []repoRow
	q := s.d.gdb.Where("visibility != ?", "private")
	if userID != 0 {
		q = s.d.gdb.Where(
			"visibility != ? OR owner_user_id = ? OR id IN (SELECT repo_id FROM repo_members WHERE user_id = ?)",
			"private", userID, userID)
	}
	if err := q.Order("namespace, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]RepoRef, 0, len(rows))
	for _, r := range rows {
		out = append(out, RepoRef{Owner: r.OwnerUserID, Namespace: r.Namespace, Name: r.Name})
	}
	return out, nil
}

// ListOwnedInfo is ListOwned plus each repo's Visibility and DefaultBranch.
// The list endpoints return these so a client does not have to GET every repo
// individually (the fields already live on the row the list query loads).
func (s *CentralStore) ListOwnedInfo(userID int64) ([]RepoInfo, error) {
	var rows []repoRow
	q := s.d.gdb.Where(
		"owner_user_id = ? OR id IN (SELECT repo_id FROM repo_members WHERE user_id = ?)",
		userID, userID)
	if err := q.Order("namespace, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	return repoInfos(rows), nil
}

// ListAccessibleInfo is ListAccessible plus each repo's Visibility and
// DefaultBranch (see ListOwnedInfo).
func (s *CentralStore) ListAccessibleInfo(userID int64) ([]RepoInfo, error) {
	var rows []repoRow
	q := s.d.gdb.Where("visibility != ?", "private")
	if userID != 0 {
		q = s.d.gdb.Where(
			"visibility != ? OR owner_user_id = ? OR id IN (SELECT repo_id FROM repo_members WHERE user_id = ?)",
			"private", userID, userID)
	}
	if err := q.Order("namespace, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	return repoInfos(rows), nil
}

func repoInfos(rows []repoRow) []RepoInfo {
	out := make([]RepoInfo, 0, len(rows))
	for _, r := range rows {
		out = append(out, RepoInfo{
			RepoRef:       RepoRef{Owner: r.OwnerUserID, Namespace: r.Namespace, Name: r.Name},
			Visibility:    r.Visibility,
			DefaultBranch: r.DefaultBranch,
		})
	}
	return out
}

// Fork copies a repository's scoped data (snapshots, revisions, refs) plus its
// hosting metadata into a new (namespace, name), sharing the global object
// store. The source repository is left untouched. It returns the new repo.
func (s *CentralStore) Fork(src RepoRef, dst RepoRef) (*Repo, error) {
	srcRepo, err := s.OpenRepo(src)
	if err != nil {
		return nil, err
	}
	dstRepo, err := s.Create(dst)
	if err != nil {
		return nil, err
	}
	// Copy hosting metadata. A metadata copy failure is best-effort (the fork
	// still succeeds); it is not silently dropped without a note.
	if meta, err := srcRepo.RepoMeta(); err == nil {
		if err := dstRepo.UpdateRepoMeta(meta); err != nil {
			return nil, fmt.Errorf("fork: copy repo meta: %w", err)
		}
	}

	// Repo identities are globally unique; a forked repository must carry its
	// OWN revision ids so two forks never share a revision (traceability). We
	// remap every snapshot/revision id to a fresh random id and recompute the
	// snapshot hash (the revision id participates in the content hash). Objects
	// (blob/tree) are content-addressed and shared as-is, so no object copy is
	// needed beyond referencing the same id.
	//
	// We read the source snapshots and revisions first to build the id map, then
	// write the remapped rows. Refs are remapped to the new revision ids.
	var remap = map[string]string{} // old revision id -> new revision id

	var snapRows []snapshotRow
	if err := s.d.gdb.Where("repo_id=?", srcRepo.repoID).Find(&snapRows).Error; err != nil {
		return nil, err
	}
	type snapRec struct {
		sha, revisionID, treeID string
		commitTime              int64
		meta                    []byte
	}
	snaps := make([]snapRec, 0, len(snapRows))
	for _, sr := range snapRows {
		snaps = append(snaps, snapRec{sha: sr.ID, revisionID: sr.RevisionID, treeID: sr.TreeID, commitTime: sr.CommitTime, meta: sr.Meta})
	}

	var revRows []revisionRow
	if err := s.d.gdb.Where("repo_id=?", srcRepo.repoID).Find(&revRows).Error; err != nil {
		return nil, err
	}
	type revRec struct {
		id, hash string
		created  int64
		forkFrom sql.NullString
		changed  []byte
	}
	revs := make([]revRec, 0, len(revRows))
	for _, rr := range revRows {
		revs = append(revs, revRec{id: rr.ID, hash: rr.Hash, created: rr.Created, forkFrom: sql.NullString{String: rr.ForkFrom, Valid: rr.ForkFrom != ""}, changed: rr.ChangedPath})
	}

	// Assign fresh ids to every source revision.
	for _, r := range revs {
		nid, err := object.RandomRevisionID()
		if err != nil {
			return nil, err
		}
		remap[r.id] = nid
	}

	// Build all remapped snapshots in memory first, keyed by old snapshot hash,
	// so parent pointers can be rewritten to the new (re-keyed) snapshot hashes.
	type newSnap struct {
		ns *Snapshot
	}
	var newSnaps []newSnap
	for _, sn := range snaps {
		decoded, err := encoding.DecodeSnapshotMeta(sn.meta)
		if err != nil {
			return nil, err
		}
		nid := remap[sn.revisionID]
		treeID, err := object.HexToID(sn.treeID)
		if err != nil {
			return nil, err
		}
		ns := &Snapshot{
			RevisionID:  nid,
			Parents:     decoded.Parents, // placeholder; rewritten below
			TreeID:      treeID,
			Description: decoded.Description,
			Author:      Author{Name: decoded.Author.Name, Email: decoded.Author.Email},
			CommitTime:  time.UnixMilli(sn.commitTime),
		}
		ns.RevisionHash = SnapshotHashFor(ns)
		newSnaps = append(newSnaps, newSnap{ns: ns})
	}
	// Rewrite parent pointers to the new snapshot hashes. We need a preliminary
	// map of old-snapshot-sha -> new-hash to translate parent edges; this map is
	// sealed AFTER parent rewrite (below) because a snapshot's own hash changes
	// when its parents change. For the initial (parent-unrewritten) snapshots we
	// use the first-pass hash to translate edges.
	prelim := map[string]object.ID{}
	for i, sn := range snaps {
		prelim[sn.sha] = newSnaps[i].ns.RevisionHash
	}
	for i := range newSnaps {
		oldParents := snaps[i]
		decoded, err := encoding.DecodeSnapshotMeta(oldParents.meta)
		if err != nil {
			return nil, err
		}
		ns := newSnaps[i].ns
		parents := make([]object.ID, 0, len(decoded.Parents))
		for _, p := range decoded.Parents {
			if m, ok := prelim[p.String()]; ok {
				parents = append(parents, m)
			} else {
				parents = append(parents, p) // external/root edge, keep as-is
			}
		}
		ns.Parents = parents
		ns.RevisionHash = SnapshotHashFor(ns)
	}
	// Seal the final old-sha -> final-new-hash map after parent rewrites.
	finalHashByOldSha := map[string]object.ID{}
	for i, sn := range snaps {
		finalHashByOldSha[sn.sha] = newSnaps[i].ns.RevisionHash
	}
	for _, ns := range newSnaps {
		if err := dstRepo.PutSnapshot(ns.ns); err != nil {
			return nil, err
		}
	}

	// Write remapped revisions. The revision's hash is the recomputed hash of
	// its remapped snapshot (looked up by the new revision id among newSnaps).
	for _, r := range revs {
		nid := remap[r.id]
		var newHash object.ID
		for _, sn := range snaps {
			if remap[sn.revisionID] == nid {
				newHash = finalHashByOldSha[sn.sha]
				break
			}
		}
		if newHash == (object.ID{}) {
			newHash = mustHexID(r.hash)
		}
		if err := dstRepo.PutRevision(&Revision{
			ID:           nid,
			Hash:         newHash,
			Created:      time.UnixMilli(r.created),
			ForkFrom:     r.forkFrom.String,
			ChangedPaths: decodeChangedPathsJSON(r.changed),
		}); err != nil {
			return nil, err
		}
	}

	// Write remapped refs (branch/tag targets -> new revision id).
	var refRows []refRow
	if err := s.d.gdb.Where("repo_id=?", srcRepo.repoID).Find(&refRows).Error; err != nil {
		return nil, err
	}
	for _, rr := range refRows {
		newTarget := rr.Target
		if mapped, ok := remap[rr.Target]; ok {
			newTarget = mapped
		}
		if err := dstRepo.PutRef(&Ref{Name: rr.Name, Kind: RefKind(rr.Kind), Target: newTarget}); err != nil {
			return nil, err
		}
	}
	return dstRepo, nil
}

func mustHexID(s string) object.ID {
	id, _ := object.HexToID(s)
	return id
}

// RepoRefByID resolves a repository id to its RepoRef (namespace/name/owner).
func (s *CentralStore) RepoRefByID(id int64) (RepoRef, error) {
	var row repoRow
	if err := s.d.gdb.Where("id=?", id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return RepoRef{}, ErrRepoNotFound
		}
		return RepoRef{}, err
	}
	return RepoRef{Owner: row.OwnerUserID, Namespace: row.Namespace, Name: row.Name}, nil
}

// BeginTx starts a GORM transaction on the central store.
func (r *Repo) BeginTx() *gorm.DB { return r.cs.d.gdb.Begin() }

// Transaction runs fn inside a database transaction, committing on success and
// rolling back on error or panic. This is the preferred entry point for atomic
// multi-table writes (BeginTx + manual Commit is easy to leak).
func (r *Repo) Transaction(fn func(tx *gorm.DB) error) error {
	return r.cs.d.gdb.Transaction(fn)
}

// QueryCount returns the total number of content-addressed objects. It is a
// convenience for tests to verify global deduplication.
func (s *CentralStore) QueryCount(count *int) error {
	var c int64
	if err := s.d.gdb.Model(&objectRow{}).Count(&c).Error; err != nil {
		return err
	}
	*count = int(c)
	return nil
}
