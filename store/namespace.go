package store

// Repository lifecycle roles and per-namespace ownership live in rbac.go and
// user.go. Collaborative access is expressed by repo_members (maintainer /
// developer) plus the namespace owner (owner).
