package store

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// PushMirror is a named external git destination that receives a force push of
// a single branch's tree. A normal (read-write) repository may have several.
type PushMirror struct {
	ID          int64
	NamespaceID int64
	Name        string
	URL         string
	Branch      string // branch name to push (default "main")
	Token       string
	LastRev     string // last revision id pushed (dirty detection)
	LastError   string
}

// PutPushMirror inserts or replaces a push-mirror for this repo. The token is
// encrypted at rest (EASYVCS_SECRET_KEY; plaintext when unset).
func (r *Repo) PutPushMirror(m *PushMirror) error {
	enc, err := encryptSecret(m.Token)
	if err != nil {
		return err
	}
	row := &pushMirrorRow{NamespaceID: r.namespaceID, Name: m.Name, URL: m.URL, Branch: m.Branch, Token: enc, LastRev: m.LastRev, LastError: m.LastError}
	return r.cs.d.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "name"}},
		UpdateAll: true,
	}).Create(row).Error
}

// GetPushMirror returns one push-mirror by name (token decrypted).
func (r *Repo) GetPushMirror(name string) (*PushMirror, error) {
	var row pushMirrorRow
	err := r.cs.d.gdb.Where("namespace_id=? AND name=?", r.namespaceID, name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	dec, err := decryptSecret(row.Token)
	if err != nil {
		return nil, err
	}
	return &PushMirror{ID: row.ID, NamespaceID: row.NamespaceID, Name: row.Name, URL: row.URL,
		Branch: row.Branch, Token: dec, LastRev: row.LastRev, LastError: row.LastError}, nil
}

// ListPushMirrors lists all push-mirrors for this repo (tokens omitted).
func (r *Repo) ListPushMirrors() ([]*PushMirror, error) {
	var rows []pushMirrorRow
	if err := r.cs.d.gdb.Where("namespace_id=?", r.namespaceID).Order("name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*PushMirror, 0, len(rows))
	for i := range rows {
		out = append(out, &PushMirror{ID: rows[i].ID, NamespaceID: rows[i].NamespaceID, Name: rows[i].Name, URL: rows[i].URL,
			Branch: rows[i].Branch, LastRev: rows[i].LastRev, LastError: rows[i].LastError})
	}
	return out, nil
}

// DeletePushMirror removes a push-mirror by name.
func (r *Repo) DeletePushMirror(name string) error {
	res := r.cs.d.gdb.Where("namespace_id=? AND name=?", r.namespaceID, name).Delete(&pushMirrorRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdatePushMirrorSync records the last pushed revision / error for a push-mirror.
func (r *Repo) UpdatePushMirrorSync(name, lastRev, lastErr string) error {
	return r.cs.d.gdb.Model(&pushMirrorRow{}).Where("namespace_id=? AND name=?", r.namespaceID, name).Updates(map[string]any{
		"last_rev": lastRev, "last_error": lastErr,
	}).Error
}

// CreateMirror creates a read-only mirror repository and returns its handle.
func (s *CentralStore) CreateMirror(r NSRef, meta RepoMeta) (*Repo, error) {
	if meta.Kind == "" {
		meta.Kind = "mirror"
	}
	repo, err := s.Create(r)
	if err != nil {
		return nil, err
	}
	if meta.MirrorInterval <= 0 {
		meta.MirrorInterval = 300
	}
	if meta.MirrorBranch == "" {
		meta.MirrorBranch = meta.DefaultBranch
		if meta.MirrorBranch == "" {
			meta.MirrorBranch = "main"
		}
	}
	if err := repo.UpdateMirrorMeta(meta); err != nil {
		return nil, err
	}
	return repo, nil
}

// AllPushMirrors lists every push-mirror across all repositories (scheduler).
// Tokens are decrypted for the scheduler's authenticated pushes. Each returned
// mirror keeps its OWN Name (the mirror name) and NamespaceID; the caller resolves
// the repository via NSRefByID(NamespaceID) — no compound label is built, so there
// is no name to split back apart.
func (s *CentralStore) AllPushMirrors() ([]*PushMirror, error) {
	var rows []pushMirrorRow
	if err := s.d.gdb.Order("name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*PushMirror, 0, len(rows))
	for i := range rows {
		dec, err := decryptSecret(rows[i].Token)
		if err != nil {
			return nil, fmt.Errorf("push mirror %s: %w", rows[i].Name, err)
		}
		out = append(out, &PushMirror{ID: rows[i].ID, NamespaceID: rows[i].NamespaceID, Name: rows[i].Name, URL: rows[i].URL,
			Branch: rows[i].Branch, Token: dec, LastRev: rows[i].LastRev, LastError: rows[i].LastError})
	}
	return out, nil
}

// AllMirrors lists every read-only mirror repository (scheduler). The mirror
// token is decrypted (RepoMeta.UpdateMirrorMeta encrypts on write; this raw
// row read must mirror that).
func (s *CentralStore) AllMirrors() ([]*Repo, RepoMetaMap, error) {
	var rows []namespaceRow
	if err := s.d.gdb.Where("kind=?", "mirror").Order("name").Find(&rows).Error; err != nil {
		return nil, nil, err
	}
	repos := make([]*Repo, 0, len(rows))
	meta := RepoMetaMap{}
	for i := range rows {
		repos = append(repos, &Repo{cs: s, namespaceID: rows[i].ID, OwnerUserID: rows[i].OwnerUserID, Name: rows[i].Name})
		dec, err := decryptSecret(rows[i].MirrorToken)
		if err != nil {
			return nil, nil, fmt.Errorf("mirror %s: %w", rows[i].Name, err)
		}
		meta[rows[i].Name] = RepoMeta{
			Kind: rows[i].Kind, MirrorURL: rows[i].MirrorURL, MirrorBranch: rows[i].MirrorBranch,
			MirrorInterval: int(rows[i].MirrorInterval), MirrorLastRev: rows[i].MirrorLastRev,
			MirrorLastSync: rows[i].MirrorLastSync, MirrorLastErr: rows[i].MirrorLastErr, MirrorToken: dec,
		}
	}
	return repos, meta, nil
}

// RepoMetaMap keys a RepoMeta by "namespace/name".
type RepoMetaMap map[string]RepoMeta

func nameOf(ns, name string) string { return ns + "/" + name }

// TouchMirrorSync updates a mirror's last sync time/rev/error.
func (r *Repo) TouchMirrorSync(lastRev string, syncEpoch int64, lastErr string) error {
	return r.cs.d.gdb.Model(&namespaceRow{}).Where("id=?", r.namespaceID).Updates(map[string]any{
		"mirror_last_rev": lastRev, "mirror_last_sync": syncEpoch, "mirror_last_error": lastErr,
	}).Error
}
