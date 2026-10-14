package store

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Remote is a named URL to another EasyVCS server. Token is the optional
// bearer token used to authenticate writes against that server.
type Remote struct {
	Name  string
	URL   string
	Token string
}

// PutRemote registers or updates a remote for this repository. An empty token
// clears the stored token.
func (r *Repo) PutRemote(name, url, token string) error {
	enc, err := encryptSecret(token)
	if err != nil {
		return err
	}
	row := &remoteRow{NamespaceID: r.namespaceID, Name: name, URL: url, Token: nullable(enc)}
	return r.cs.d.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "name"}},
		UpdateAll: true,
	}).Create(row).Error
}

// UpdateRemoteSyncTip records the last successfully synced tip for a remote
// branch (the local revision id that mirrors the remote's tip after a pull).
// Empty clears it. This enables incremental/conflict-aware pulls.
func (r *Repo) UpdateRemoteSyncTip(name, lastSyncTip string) error {
	return r.cs.d.gdb.Model(&remoteRow{}).Where("namespace_id=? AND name=?", r.namespaceID, name).Update("last_sync_tip", lastSyncTip).Error
}

// GetLastSyncTip returns the last synced tip for a remote ("" if none).
func (r *Repo) GetLastSyncTip(name string) (string, error) {
	var row remoteRow
	err := r.cs.d.gdb.Where("namespace_id=? AND name=?", r.namespaceID, name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.LastSyncTip, nil
}

// RemoteRef is a snapshot of a remote's ref as last fetched, scoped to a remote
// name (e.g. "origin.main", kind=branch). This mirrors git's refs/remotes.
type RemoteRef struct {
	RemoteName string
	Kind       RefKind
	Name       string
	Target     string // revision id on the remote
}

// SetRemoteRef upserts a remote ref. Primary key is remote+kind+name.
func (r *Repo) SetRemoteRef(remoteName string, ref *RemoteRef) error {
	row := &remoteRefRow{NamespaceID: r.namespaceID, RemoteName: remoteName, Kind: string(ref.Kind), Name: ref.Name, Target: ref.Target}
	return r.cs.d.gdb.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "namespace_id"}, {Name: "remote_name"}, {Name: "kind"}, {Name: "name"}},
		UpdateAll: true,
	}).Create(row).Error
}

// ListRemoteRefs lists all remote refs for this repo (optionally filtered by
// remote name). Returns refs keyed under the remote (name is bare, e.g. "main").
func (r *Repo) ListRemoteRefs(remoteName string) ([]*RemoteRef, error) {
	q := r.cs.d.gdb.Where("namespace_id=?", r.namespaceID)
	if remoteName != "" {
		q = q.Where("remote_name=?", remoteName)
	}
	var rows []remoteRefRow
	if err := q.Order("remote_name, name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*RemoteRef, 0, len(rows))
	for i := range rows {
		out = append(out, &RemoteRef{RemoteName: rows[i].RemoteName, Kind: RefKind(rows[i].Kind), Name: rows[i].Name, Target: rows[i].Target})
	}
	return out, nil
}

// DeleteRemoteRefsForRemote clears all recorded refs for a remote (used when a
// remote is deleted or force-refreshed).
func (r *Repo) DeleteRemoteRefsForRemote(remoteName string) error {
	return r.cs.d.gdb.Where("namespace_id=? AND remote_name=?", r.namespaceID, remoteName).Delete(&remoteRefRow{}).Error
}

// SetRemoteDefaultBranch records which branch on a remote is its default.
func (r *Repo) SetRemoteDefaultBranch(name, def string) error {
	return r.cs.d.gdb.Model(&remoteRow{}).Where("namespace_id=? AND name=?", r.namespaceID, name).Update("default_branch", def).Error
}

// GetRemoteDefaultBranch returns the remote's default branch ("" if none).
func (r *Repo) GetRemoteDefaultBranch(name string) (string, error) {
	var row remoteRow
	err := r.cs.d.gdb.Where("namespace_id=? AND name=?", r.namespaceID, name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return row.DefaultBranch, nil
}

// GetRemote returns a remote by name.
func (r *Repo) GetRemote(name string) (*Remote, error) {
	var row remoteRow
	err := r.cs.d.gdb.Where("namespace_id=? AND name=?", r.namespaceID, name).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	token := ""
	if row.Token != nil {
		dec, derr := decryptSecret(*row.Token)
		if derr != nil {
			return nil, derr
		}
		token = dec
	}
	return &Remote{Name: name, URL: row.URL, Token: token}, nil
}

// DeleteRemote removes a remote by name.
func (r *Repo) DeleteRemote(name string) error {
	return r.cs.d.gdb.Where("namespace_id=? AND name=?", r.namespaceID, name).Delete(&remoteRow{}).Error
}

// ListRemotes lists all remotes for this repository. Tokens are omitted from
// the returned list to avoid leaking secrets.
func (r *Repo) ListRemotes() ([]*Remote, error) {
	var rows []remoteRow
	if err := r.cs.d.gdb.Where("namespace_id=?", r.namespaceID).Order("name").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*Remote, 0, len(rows))
	for i := range rows {
		out = append(out, &Remote{Name: rows[i].Name, URL: rows[i].URL})
	}
	return out, nil
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
