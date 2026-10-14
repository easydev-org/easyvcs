package store

// NamespaceID returns the numeric repo id used internally.
func (r *Repo) NamespaceID() int64 { return r.namespaceID }

func (r *Repo) String() string { return r.Name }

// Close is a no-op for the repo handle (the CentralStore is shared).
func (r *Repo) Close() error { return nil }

// NSRef returns the NSRef for this handle.
func (r *Repo) NSRef() NSRef {
	return NSRef{Owner: r.OwnerUserID, Name: r.Name}
}

// Owner returns the repository's owner user id (the namespace owner).
func (r *Repo) Owner() int64 { return r.OwnerUserID }
