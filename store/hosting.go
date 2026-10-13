package store

// Hosting-surface accessors. These are additive (no existing signature is
// changed) and exist only because the hosting REST surface needs a couple of
// lookups the CLI did not: deleting one's own token by id (the plaintext is
// never available after creation, so DeleteToken(secret) cannot be used from a
// token-list view).

// DeleteTokenByID removes a token by id, scoped to its owning user, so a caller
// can only delete their OWN token from the token list (which never carries the
// plaintext secret). Returns ErrTokenNotFound when no row matches.
func (s *CentralStore) DeleteTokenByID(id, userID int64) error {
	res := s.d.gdb.Where("id=? AND user_id=?", id, userID).Delete(&tokenRow{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrTokenNotFound
	}
	return nil
}
