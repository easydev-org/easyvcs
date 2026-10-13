package store

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PutRef stores a ref scoped to this repository.
func (r *Repo) PutRef(ref *Ref) error {
	row := &refRow{RepoID: r.repoID, Name: ref.Name, Kind: string(ref.Kind), Target: ref.Target}
	return r.cs.d.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repo_id"}, {Name: "name"}},
		UpdateAll: true,
	}).Create(row).Error
}

// PutRefTx upserts a ref within an open transaction.
func (r *Repo) PutRefTx(tx *gorm.DB, ref *Ref) error {
	row := &refRow{RepoID: r.repoID, Name: ref.Name, Kind: string(ref.Kind), Target: ref.Target}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "repo_id"}, {Name: "name"}},
		UpdateAll: true,
	}).Create(row).Error
}

// DeleteRef removes a ref scoped to this repository.
func (r *Repo) DeleteRef(name string) error {
	res := r.cs.d.gdb.Where("repo_id=? AND name=?", r.repoID, name).Delete(&refRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// GetRef reads a ref scoped to this repository.
func (r *Repo) GetRef(name string) (*Ref, error) {
	var row refRow
	err := r.cs.d.gdb.Where("repo_id=? AND name=?", r.repoID, name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &Ref{Name: row.Name, Kind: RefKind(row.Kind), Target: row.Target}, nil
}

// ListRefs lists refs scoped to this repository.
func (r *Repo) ListRefs() ([]*Ref, error) {
	var rows []refRow
	if err := r.cs.d.gdb.Where("repo_id=?", r.repoID).Order("name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*Ref, 0, len(rows))
	for i := range rows {
		out = append(out, &Ref{Name: rows[i].Name, Kind: RefKind(rows[i].Kind), Target: rows[i].Target})
	}
	return out, nil
}
