package store

import (
	"errors"
	"time"

	"github.com/easylab-platform/easyvcs/encoding"
	"github.com/easylab-platform/easyvcs/object"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PutSnapshot stores a snapshot scoped to this repository. Parents, author, and
// description are packed into a single compact meta BLOB (see encoding pkg).
func (r *Repo) PutSnapshot(snap *Snapshot) error {
	meta := encoding.EncodeSnapshotMeta(encoding.BaseSnapshot{
		Parents:     snap.Parents,
		Author:      encoding.Author{Name: snap.Author.Name, Email: snap.Author.Email, Session: snap.Author.Session},
		Description: snap.Description,
	})
	row := &snapshotRow{
		ID: snap.RevisionHash.String(), NamespaceID: r.namespaceID, RevisionID: snap.RevisionID,
		TreeID: snap.TreeID.String(), CommitTime: snap.CommitTime.UnixMilli(), Meta: meta,
	}
	if err := r.cs.d.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "sha"}},
		UpdateAll: true,
	}).Create(row).Error; err != nil {
		return err
	}
	// Refresh the cache: a snapshot row can be re-put (same hash, updated meta).
	r.cs.snapCache.put(row.ID, snap)
	return nil
}

// GetSnapshot reads a snapshot scoped to this repository.
func (r *Repo) GetSnapshot(id object.ID) (*Snapshot, error) {
	return r.getSnapshot(id)
}

func (r *Repo) getSnapshot(id object.ID) (*Snapshot, error) {
	if s, ok := r.cs.snapCache.get(id.String()); ok {
		return s, nil
	}
	var row snapshotRow
	err := r.cs.d.gdb.Where("namespace_id=? AND sha=?", r.namespaceID, id.String()).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s, err := r.snapshotFromRow(&row)
	if err != nil {
		return nil, err
	}
	r.cs.snapCache.put(id.String(), s)
	return s, nil
}

// snapshotFromRow decodes a snapshot row into the domain type.
func (r *Repo) snapshotFromRow(row *snapshotRow) (*Snapshot, error) {
	oid, err := object.HexToID(row.ID)
	if err != nil {
		return nil, err
	}
	tid, err := object.HexToID(row.TreeID)
	if err != nil {
		return nil, err
	}
	decoded, err := encoding.DecodeSnapshotMeta(row.Meta)
	if err != nil {
		return nil, err
	}
	return &Snapshot{
		RevisionHash: oid, RevisionID: row.RevisionID, Parents: decoded.Parents, TreeID: tid,
		Description: decoded.Description,
		Author:      Author{Name: decoded.Author.Name, Email: decoded.Author.Email, Session: decoded.Author.Session},
		CommitTime:  time.UnixMilli(row.CommitTime),
	}, nil
}

// SnapshotsByHashes fetches many snapshots in ONE query (WHERE sha IN (...)),
// eliminating the per-revision N+1 in log / subrepo / file history. Hashes not
// found are simply absent from the map. Cached entries are served from memory.
func (r *Repo) SnapshotsByHashes(hashes []string) (map[string]*Snapshot, error) {
	out := make(map[string]*Snapshot, len(hashes))
	missing := make([]string, 0, len(hashes))
	seen := make(map[string]bool, len(hashes))
	for _, h := range hashes {
		if h == "" || seen[h] {
			continue
		}
		seen[h] = true
		if s, ok := r.cs.snapCache.get(h); ok {
			out[h] = s
			continue
		}
		missing = append(missing, h)
	}
	if len(missing) == 0 {
		return out, nil
	}
	var rows []snapshotRow
	if err := r.cs.d.gdb.Where("namespace_id=? AND sha IN ?", r.namespaceID, missing).Find(&rows).Error; err != nil {
		return nil, err
	}
	for i := range rows {
		s, err := r.snapshotFromRow(&rows[i])
		if err != nil {
			continue
		}
		out[rows[i].ID] = s
		r.cs.snapCache.put(rows[i].ID, s)
	}
	return out, nil
}

// PutSnapshotTx inserts a snapshot within an open transaction.
func (r *Repo) PutSnapshotTx(tx *gorm.DB, snap *Snapshot) error {
	meta := encoding.EncodeSnapshotMeta(encoding.BaseSnapshot{
		Parents:     snap.Parents,
		Author:      encoding.Author{Name: snap.Author.Name, Email: snap.Author.Email, Session: snap.Author.Session},
		Description: snap.Description,
	})
	row := &snapshotRow{
		ID: snap.RevisionHash.String(), NamespaceID: r.namespaceID, RevisionID: snap.RevisionID,
		TreeID: snap.TreeID.String(), CommitTime: snap.CommitTime.UnixMilli(), Meta: meta,
	}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "sha"}},
		UpdateAll: true,
	}).Create(row).Error
}
