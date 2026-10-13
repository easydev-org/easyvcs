package store

// RepoID returns the numeric repo id used internally.
func (r *Repo) RepoID() int64 { return r.repoID }

func (r *Repo) String() string { return r.Namespace + "/" + r.Name }

// Close is a no-op for the repo handle (the CentralStore is shared).
func (r *Repo) Close() error { return nil }

// RepoRef returns the RepoRef for this handle.
func (r *Repo) RepoRef() RepoRef {
	return RepoRef{Owner: r.OwnerUserID, Namespace: r.Namespace, Name: r.Name}
}

// Owner returns the repository's owner user id (the namespace owner).
func (r *Repo) Owner() int64 { return r.OwnerUserID }
