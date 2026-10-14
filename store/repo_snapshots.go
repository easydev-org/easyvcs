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
	return r.cs.d.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "sha"}},
		UpdateAll: true,
	}).Create(row).Error
}

// GetSnapshot reads a snapshot scoped to this repository.
func (r *Repo) GetSnapshot(id object.ID) (*Snapshot, error) {
	return r.getSnapshot(id)
}

func (r *Repo) getSnapshot(id object.ID) (*Snapshot, error) {
	var row snapshotRow
	err := r.cs.d.gdb.Where("namespace_id=? AND sha=?", r.namespaceID, id.String()).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
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
