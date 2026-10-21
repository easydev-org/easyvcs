package server

import (
	"context"
	"net/http"

	"github.com/easylab-platform/easyvcs/store"
)

// Identity is who the caller is, as decided by an EXTERNAL authority.
//
// The default resolver (used when none is installed) answers "who" from the
// bearer token and "what role" from the store's namespace ACL. A host that
// embeds this server — e.g. a platform with its own session/API-key scheme —
// can install its own resolver so easyvcs trusts the host's decision instead of
// re-reading the store.
//
// Role semantics match store.Role: RoleOwner / maintainer / developer; the zero
// value means "no role on this namespace".
type Identity struct {
	// UserID is the store user id (0 = anonymous). It is used for viewer-scoped
	// reads of private refs/drafts, so a host SHOULD supply the real id.
	UserID int64
	// Username is informational (audit / author fallback).
	Username string
	// Role is the caller's role on the REQUEST's namespace. When empty the
	// server falls back to the store lookup (unchanged behaviour).
	Role store.Role
}

// IdentityResolver decides the caller's Identity from the request. ok=false
// means "no decision"; the server then behaves exactly as before (token lookup).
type IdentityResolver func(r *http.Request) (Identity, bool)

// WithIdentity installs an EXTERNAL identity resolver. It is the single
// integration point for a host that embeds this server: currentUser and the
// four capability checks consult it first, and fall back to the store when it
// declines.
//
// nil (or never calling this) leaves the default behaviour untouched, so the
// standalone binary and every existing test are unaffected.
func (s *Server) WithIdentity(fn IdentityResolver) *Server {
	s.identity = fn
	return s
}

// resolveIdentity asks the installed resolver (if any) for the caller.
func (s *Server) resolveIdentity(r *http.Request) (Identity, bool) {
	if s.identity == nil {
		return Identity{}, false
	}
	return s.identity(r)
}

// identityCtxKey is the context key carrying a resolved Identity.
type identityCtxKey struct{}

// withIdentity stashes a resolved Identity on the request context.
func withIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// identityOf returns the Identity stashed by the middleware, if any.
func identityOf(r *http.Request) (Identity, bool) {
	id, ok := r.Context().Value(identityCtxKey{}).(Identity)
	return id, ok
}

// roleOf returns the caller's role on the request's namespace. When the host
// installed a resolver AND it decided, its Role wins; otherwise the store ACL is
// consulted (unchanged behaviour).
//
// It is the ONE place the four capability checks get their role from, so an
// embedded host overrides authorization with a single hook.
func (s *Server) roleOf(r *http.Request, rref store.NSRef, tk *store.Token) store.Role {
	if id, ok := identityOf(r); ok && id.Role != "" {
		return id.Role
	}
	role, err := s.cs.RoleOfRef(rref, userIDVal(tk))
	if err != nil {
		return store.RoleNone
	}
	return role
}

// canReadReq / canPushReq / canProposeReq / canMergeReq are the REQUEST-AWARE
// capability checks: they consult an installed identity resolver first, then the
// store ACL. They are drop-in replacements for canRead/canPush/etc. and are what
// the gateways call.
func (s *Server) canReadReq(r *http.Request, rref store.NSRef, tk *store.Token) bool {
	role := s.roleOf(r, rref, tk)
	if role.CanRead() {
		return true
	}
	return tk == nil && s.cs.IsOpenInstance()
}

func (s *Server) canPushReq(r *http.Request, rref store.NSRef, tk *store.Token) bool {
	role := s.roleOf(r, rref, tk)
	if role.CanPush() {
		return true
	}
	return s.cs.IsOpenInstance()
}

func (s *Server) canProposeReq(r *http.Request, rref store.NSRef, tk *store.Token) bool {
	if tk == nil {
		return false
	}
	return s.roleOf(r, rref, tk).CanPropose()
}

func (s *Server) canMergeReq(r *http.Request, rref store.NSRef, tk *store.Token) bool {
	role := s.roleOf(r, rref, tk)
	if role.CanMerge() {
		return true
	}
	return s.cs.IsOpenInstance()
}

// stashIdentity resolves the caller once and attaches it to the request, so the
// capability checks downstream do not each call the resolver.
func (s *Server) stashIdentity(r *http.Request) *http.Request {
	if id, ok := s.resolveIdentity(r); ok {
		return r.WithContext(withIdentity(r.Context(), id))
	}
	return r
}
