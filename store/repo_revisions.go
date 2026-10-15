package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/easylab-platform/easyvcs/object"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PutRevision stores a revision scoped to this repository.
func (r *Repo) PutRevision(rev *Revision) error {
	row := r.toRevisionRow(rev)
	if err := r.cs.d.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "id"}},
		UpdateAll: true,
	}).Create(row).Error; err != nil {
		return err
	}
	// Async: defer the index write to the background worker (read-your-writes is
	// preserved by RevisionsTouching's flush).
	if r.cs.idxQueue != nil {
		r.cs.idxQueue.enqueue(r.namespaceID, rev.ID, rev.ChangedPaths)
		return nil
	}
	return r.indexRevisionPaths(rev)
}

// PutRevisionTx inserts a revision within an open transaction.
func (r *Repo) PutRevisionTx(tx *gorm.DB, rev *Revision) error {
	row := r.toRevisionRow(rev)
	if err := tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "id"}},
		UpdateAll: true,
	}).Create(row).Error; err != nil {
		return err
	}
	// The index is NOT written here: it is either deferred (async) or written by
	// the caller via IndexRevision AFTER the transaction commits. Writing it
	// inside the tx would both bloat the commit and (async) duplicate the work.
	return nil
}

// IndexRevision records (or defers) the path index for a revision. When the
// store runs with an async index it enqueues the job; otherwise it writes the
// rows now. PutRevisionTx leaves the index out (it runs inside a transaction);
// the caller invokes IndexRevision AFTER the transaction commits.
func (r *Repo) IndexRevision(rev *Revision) error {
	if r.cs.idxQueue != nil {
		r.cs.idxQueue.enqueue(r.namespaceID, rev.ID, rev.ChangedPaths)
		return nil
	}
	return r.indexRevisionPaths(rev)
}

// indexRevisionPaths records the directory prefixes a revision touched, so a
// subdirectory can be queried as a repository without scanning every revision.
// It is idempotent (delete-then-insert) and safe to re-run.
func (r *Repo) indexRevisionPaths(rev *Revision) error {
	return r.indexRevisionPathsTx(r.cs.d.gdb, rev)
}

func (r *Repo) indexRevisionPathsTx(tx *gorm.DB, rev *Revision) error {
	if err := tx.Where("namespace_id=? AND revision_id=?", r.namespaceID, rev.ID).
		Delete(&revisionPathDirRow{}).Error; err != nil {
		return err
	}
	dirs := changedPathDirs(rev.ChangedPaths)
	if len(dirs) == 0 {
		return nil
	}
	rows := make([]revisionPathDirRow, 0, len(dirs))
	for d := range dirs {
		rows = append(rows, revisionPathDirRow{NamespaceID: r.namespaceID, Dir: d, RevisionID: rev.ID})
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rows).Error
}

// changedPathDirs returns every prefix of every changed path — the path itself
// plus each ancestor directory ("" is the root). "a/b/c.txt" -> {"a/b/c.txt",
// "a/b", "a", ""}. Including the path itself means a change that DELETES a
// directory still registers under that directory.
func changedPathDirs(paths []string) map[string]bool {
	out := map[string]bool{"": true}
	for _, p := range paths {
		p = strings.Trim(p, "/")
		if p == "" {
			continue
		}
		for {
			out[p] = true
			i := strings.LastIndex(p, "/")
			if i < 0 {
				break
			}
			p = p[:i]
		}
	}
	return out
}

// RevisionsTouching returns (newest-first) the revisions that changed anything
// under dir (a repo-relative directory; "" = the whole repository). It uses the
// path index, so it is O(revisions under dir), not O(all revisions).
func (r *Repo) RevisionsTouching(dir string) ([]string, error) {
	dir = strings.Trim(dir, "/")
	// Read-your-writes: drain this namespace's deferred index jobs first.
	r.cs.idxQueue.flush(r.namespaceID)
	// Lazy backfill: revisions written before the index existed have no rows.
	// Build it once per namespace. The readiness flag is cached on the store
	// (rows are only added), so this does NOT issue a COUNT on every call.
	if !r.cs.indexReady(r.namespaceID) {
		var n int64
		if err := r.cs.d.gdb.Model(&revisionPathDirRow{}).Where("namespace_id=?", r.namespaceID).Count(&n).Error; err != nil {
			return nil, err
		}
		if n == 0 {
			if err := r.ReindexPathDirs(); err != nil {
				return nil, err
			}
		}
		r.cs.markIndexReady(r.namespaceID)
	}
	var rows []revisionPathDirRow
	if err := r.cs.d.gdb.Where("namespace_id=? AND dir=?", r.namespaceID, dir).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.RevisionID)
	}
	return out, nil
}

// ReindexPathDirs (re)builds the directory-prefix index for every revision of
// this repository from its persisted changed_paths. Idempotent; used to backfill
// repositories whose revisions predate the index.
func (r *Repo) ReindexPathDirs() error {
	var rows []revisionRow
	if err := r.cs.d.gdb.Where("namespace_id=?", r.namespaceID).Find(&rows).Error; err != nil {
		return err
	}
	return r.cs.d.gdb.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("namespace_id=?", r.namespaceID).Delete(&revisionPathDirRow{}).Error; err != nil {
			return err
		}
		for _, row := range rows {
			dirs := changedPathDirs(decodeChangedPathsJSON(row.ChangedPath))
			batch := make([]revisionPathDirRow, 0, len(dirs))
			for d := range dirs {
				batch = append(batch, revisionPathDirRow{NamespaceID: r.namespaceID, Dir: d, RevisionID: row.ID})
			}
			if len(batch) > 0 {
				if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&batch).Error; err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// toRevisionRow maps a Revision to its persistent row.
func (r *Repo) toRevisionRow(rev *Revision) *revisionRow {
	changed, _ := json.Marshal(rev.ChangedPaths)
	return &revisionRow{
		NamespaceID: r.namespaceID, ID: rev.ID, Hash: rev.Hash.String(),
		Created: rev.Created.UnixMilli(), ForkFrom: rev.ForkFrom, ChangedPath: changed,
	}
}

// GetRevision reads a revision scoped to this repository.
func (r *Repo) GetRevision(id string) (*Revision, error) {
	var row revisionRow
	err := r.cs.d.gdb.Where("namespace_id=? AND id=?", r.namespaceID, id).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rev, err := r.fromRevisionRow(&row)
	if err != nil {
		return nil, err
	}
	return rev, nil
}

func (r *Repo) fromRevisionRow(row *revisionRow) (*Revision, error) {
	cur, err := object.HexToID(row.Hash)
	if err != nil {
		// A corrupt hash would silently become a zero id and poison ancestry
		// checks downstream; surface it instead.
		return nil, fmt.Errorf("revision %s: corrupt hash %q: %w", row.ID, row.Hash, err)
	}
	return &Revision{
		ID: row.ID, Hash: cur, Created: time.UnixMilli(row.Created),
		ForkFrom: row.ForkFrom, ChangedPaths: decodeChangedPathsJSON(row.ChangedPath),
	}, nil
}

// UpdateRevisionHash atomically repoints a revision's Hash field. It preserves
// the existing ChangedPaths.
func (r *Repo) UpdateRevisionHash(revisionID string, hash object.ID) error {
	rev, err := r.GetRevision(revisionID)
	if err != nil {
		return err
	}
	rev.Hash = hash
	return r.PutRevision(rev)
}

// UpdateRevisionHashCAS repoints a revision's Hash to a new snapshot hash only
// if the revision still points at `expectHash`. This is an optimistic-concurrency
// (compare-and-swap) guard: if another writer amended the revision in between,
// ErrRevisionChanged is returned instead of silently last-write-wins. The
// snapshot is written only when the CAS succeeds.
func (r *Repo) UpdateRevisionHashCAS(revisionID string, expectHash, newHash object.ID) error {
	rev, err := r.GetRevision(revisionID)
	if err != nil {
		return err
	}
	if rev.Hash != expectHash {
		return ErrRevisionChanged
	}
	rev.Hash = newHash
	return r.PutRevision(rev)
}

// ListRevisions lists revisions scoped to this repository.
func (r *Repo) ListRevisions() ([]*Revision, error) {
	var rows []revisionRow
	if err := r.cs.d.gdb.Where("namespace_id=?", r.namespaceID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*Revision, 0, len(rows))
	for i := range rows {
		rev, err := r.fromRevisionRow(&rows[i])
		if err != nil {
			return nil, err
		}
		out = append(out, rev)
	}
	return out, nil
}
