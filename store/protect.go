package store

import (
	"encoding/json"
	"fmt"
	"strings"
)

// defaultProtectedBranchesJSON is the protection set a NEW repository gets: its
// default branch (`main`). A protected branch may only change through a merged
// Revision Request; a direct push is refused by the server. See
// server.handlePush.
const defaultProtectedBranchesJSON = `["main"]`

// ProtectedBranches returns the branch names that may only change through a
// merged Revision Request. An empty slice means no protection.
func (r *Repo) ProtectedBranches() ([]string, error) {
	var row repoRow
	if err := r.cs.d.gdb.Select("protected_branches").First(&row, r.repoID).Error; err != nil {
		return nil, err
	}
	return decodeProtectedBranches(row.ProtectedBranches), nil
}

// SetProtectedBranches replaces the repository's protected-branch set. Empty
// clears protection entirely (behaviour identical to before the feature). Names
// are de-duplicated and trimmed; an empty name is rejected.
func (r *Repo) SetProtectedBranches(branches []string) error {
	clean := make([]string, 0, len(branches))
	seen := map[string]bool{}
	for _, b := range branches {
		b = strings.TrimSpace(b)
		if b == "" {
			return fmt.Errorf("store: protected branch name must not be empty")
		}
		if seen[b] {
			continue
		}
		seen[b] = true
		clean = append(clean, b)
	}
	raw, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	return r.cs.d.gdb.Model(&repoRow{}).Where("id=?", r.repoID).Updates(map[string]any{
		"protected_branches": string(raw),
	}).Error
}

// IsBranchProtected reports whether `branch` is in the repository's protected
// set. It is the predicate the push path uses.
func (r *Repo) IsBranchProtected(branch string) (bool, error) {
	branches, err := r.ProtectedBranches()
	if err != nil {
		return false, err
	}
	for _, b := range branches {
		if b == branch {
			return true, nil
		}
	}
	return false, nil
}

// decodeProtectedBranches parses the JSON column. A malformed or empty value
// yields nil (no protection) rather than an error: a corrupt column must not
// make every push fail closed in a way the operator cannot diagnose.
func decodeProtectedBranches(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil
	}
	return out
}
