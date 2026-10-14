package store

import (
	"errors"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// normPath normalizes a scope path (trim slashes; "" for root).
func normPath(p string) string { return strings.Trim(strings.TrimSpace(p), "/") }

// PutRef stores a ref in its scope (r.Path; "" = root).
func (r *Repo) PutRef(ref *Ref) error {
	row := &refRow{NamespaceID: r.namespaceID, Path: normPath(ref.Path), Name: ref.Name, Kind: string(ref.Kind), Target: ref.Target}
	return r.cs.d.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "path"}, {Name: "name"}},
		UpdateAll: true,
	}).Create(row).Error
}

// PutRefTx upserts a ref within an open transaction.
func (r *Repo) PutRefTx(tx *gorm.DB, ref *Ref) error {
	row := &refRow{NamespaceID: r.namespaceID, Path: normPath(ref.Path), Name: ref.Name, Kind: string(ref.Kind), Target: ref.Target}
	return tx.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "path"}, {Name: "name"}},
		UpdateAll: true,
	}).Create(row).Error
}

// DeleteRef removes a ROOT-scope ref (path=""). Use DeleteRefIn for a sub-scope.
func (r *Repo) DeleteRef(name string) error { return r.DeleteRefIn("", name) }

// DeleteRefIn removes a ref by (scope path, name).
func (r *Repo) DeleteRefIn(path, name string) error {
	res := r.cs.d.gdb.Where("namespace_id=? AND path=? AND name=?", r.namespaceID, normPath(path), name).Delete(&refRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// GetRef reads a ROOT-scope ref. Use GetRefIn for a sub-scope.
func (r *Repo) GetRef(name string) (*Ref, error) { return r.GetRefIn("", name) }

// GetRefIn reads a ref by (scope path, name).
func (r *Repo) GetRefIn(path, name string) (*Ref, error) {
	var row refRow
	err := r.cs.d.gdb.Where("namespace_id=? AND path=? AND name=?", r.namespaceID, normPath(path), name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &Ref{Name: row.Name, Kind: RefKind(row.Kind), Target: row.Target, Path: row.Path}, nil
}

// ListRefs lists EVERY ref of the repository, regardless of scope (root refs
// have Path==""). Callers that want a scope's view use ListRefsIn.
func (r *Repo) ListRefs() ([]*Ref, error) {
	var rows []refRow
	if err := r.cs.d.gdb.Where("namespace_id=?", r.namespaceID).Order("path, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*Ref, 0, len(rows))
	for i := range rows {
		out = append(out, &Ref{Name: rows[i].Name, Kind: RefKind(rows[i].Kind), Target: rows[i].Target, Path: rows[i].Path})
	}
	return out, nil
}

// ListOwnRefs lists the refs DEFINED in a scope (path == the given scope),
// without projection. "" lists the root's own refs.
func (r *Repo) ListOwnRefs(path string) ([]*Ref, error) {
	var rows []refRow
	if err := r.cs.d.gdb.Where("namespace_id=? AND path=?", r.namespaceID, normPath(path)).Order("name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*Ref, 0, len(rows))
	for i := range rows {
		out = append(out, &Ref{Name: rows[i].Name, Kind: RefKind(rows[i].Kind), Target: rows[i].Target, Path: rows[i].Path})
	}
	return out, nil
}

// ListRefsIn returns the refs VISIBLE in scope Q, applying the projection rule
// (docs/subrepo-scopes.md §4):
//
//	refs(Q) = root refs (r.path=="")  ∪  refs defined at Q or under it.
//
// `hasDir` decides whether a ROOT ref projects into Q: it reports whether the
// ref's tip tree contains a directory at Q. When hasDir is nil, every root ref
// projects (the caller has already scoped by other means).
func (r *Repo) ListRefsIn(path string, hasDir func(target string) bool) ([]*Ref, error) {
	q := normPath(path)
	var rows []refRow
	// path == "" (root, projects down) OR path == q OR path is q + "/...".
	like := q + "/%"
	err := r.cs.d.gdb.Where("namespace_id=? AND (path='' OR path=? OR path LIKE ?)", r.namespaceID, q, like).
		Order("path, name").Find(&rows).Error
	if err != nil {
		return nil, err
	}
	out := make([]*Ref, 0, len(rows))
	for i := range rows {
		row := rows[i]
		// A root ref projects into a NON-root scope only if its tip contains
		// the directory. (At the root scope q=="", every ref is visible.)
		if row.Path == "" && q != "" && hasDir != nil && !hasDir(row.Target) {
			continue
		}
		out = append(out, &Ref{Name: row.Name, Kind: RefKind(row.Kind), Target: row.Target, Path: row.Path})
	}
	return out, nil
}
