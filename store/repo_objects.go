package store

import (
	"errors"

	"github.com/easylab-platform/easyvcs/object"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// WriteObject stores a global content-addressed object. Objects are immutable,
// so an existing object is never overwritten.
func (r *Repo) WriteObject(o *object.Object) error {
	id := o.ID()
	payload, err := object.EncodeObject(o)
	if err != nil {
		return err
	}
	row := &objectRow{ID: id.String(), Kind: int(o.Kind), Content: payload}
	// ON CONFLICT DO NOTHING
	if err := r.cs.d.gdb.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
		return err
	}
	// A just-written object is hot: seed the cache.
	r.cs.objCache.put(id, o)
	return nil
}

// WriteObjectsBatch stores many objects in a single transaction, reducing
// round-trips when applying a bundle. Each object's id is recomputed from its
// content; duplicate ids are ignored (idempotent).
func (r *Repo) WriteObjectsBatch(objs []*object.Object) error {
	if len(objs) == 0 {
		return nil
	}
	return r.cs.d.gdb.Transaction(func(tx *gorm.DB) error {
		for _, o := range objs {
			id := o.ID()
			payload, err := object.EncodeObject(o)
			if err != nil {
				return err
			}
			row := &objectRow{ID: id.String(), Kind: int(o.Kind), Content: payload}
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

// ReadObject reads a global content-addressed object.
func (r *Repo) ReadObject(id object.ID) (*object.Object, error) {
	if o, ok := r.cs.objCache.get(id); ok {
		return o, nil
	}
	var row objectRow
	err := r.cs.d.gdb.Where("sha=?", id.String()).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	o, err := object.DecodeObject(id, row.Content)
	if err != nil {
		return nil, err
	}
	r.cs.objCache.put(id, o)
	return o, nil
}

// ObjectIDs returns all content-addressed object ids held anywhere in the
// central store (objects are global and deduplicated across repositories). It
// is used by clients to advertise "have" objects during fetch/push so a peer
// only sends objects the destination lacks.
func (r *Repo) ObjectIDs() ([]object.ID, error) {
	var rows []string
	if err := r.cs.d.gdb.Model(&objectRow{}).Pluck("sha", &rows).Error; err != nil {
		return nil, err
	}
	out := make([]object.ID, 0, len(rows))
	for _, s := range rows {
		if id, err := object.HexToID(s); err == nil {
			out = append(out, id)
		}
	}
	return out, nil
}

// ObjectExists reports whether an object is present.
func (r *Repo) ObjectExists(id object.ID) (bool, error) {
	var count int64
	if err := r.cs.d.gdb.Model(&objectRow{}).Where("sha=?", id.String()).Count(&count).Error; err != nil {
		return false, err
	}
	return count > 0, nil
}

// DeleteObject removes a single content-addressed object (global). It is used
// by the GC command to prune objects no longer referenced by any snapshot. The
// caller is responsible for reachability analysis.
func (r *Repo) DeleteObject(id object.ID) error {
	return r.cs.d.gdb.Where("sha=?", id.String()).Delete(&objectRow{}).Error
}

// WriteObjectsBatchTx stores objects in the given transaction.
func (r *Repo) WriteObjectsBatchTx(tx *gorm.DB, objs []*object.Object) error {
	for _, o := range objs {
		id := o.ID()
		payload, err := object.EncodeObject(o)
		if err != nil {
			return err
		}
		row := &objectRow{ID: id.String(), Kind: int(o.Kind), Content: payload}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(row).Error; err != nil {
			return err
		}
	}
	return nil
}
