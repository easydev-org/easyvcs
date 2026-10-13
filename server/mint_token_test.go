package server

import (
	"net/http"
	"testing"
)

// TestMintUserTokenForExistingUser pins the admin mint endpoint: it issues a
// NEW, usable token for a user that ALREADY exists (the case POST /users
// refuses with 409 and never re-returns a token for), without revoking the
// user's existing tokens.
func TestMintUserTokenForExistingUser(t *testing.T) {
	_, mux, _, _ := hostingTestServer(t)
	t.Setenv("EASYVCS_ADMIN_TOKEN", "adm")

	// Create bob; capture token1.
	rec := hreq(mux, http.MethodPost, "/api/v1/users", "adm", map[string]any{"username": "bob"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create user: %d %s", rec.Code, rec.Body.String())
	}
	body := decode(t, rec)
	tok1, _ := body["token"].(string)
	id := int64(body["user"].(map[string]any)["id"].(float64))
	if tok1 == "" || id == 0 {
		t.Fatalf("bad create response: %v", body)
	}

	// A second POST /users with the same username is refused (409) — the very
	// case this endpoint exists to unblock.
	if rec := hreq(mux, http.MethodPost, "/api/v1/users", "adm", map[string]any{"username": "bob"}); rec.Code != http.StatusConflict {
		t.Fatalf("duplicate create: got %d, want 409", rec.Code)
	}

	// Admin mints a fresh token for the EXISTING user.
	rec = hreq(mux, http.MethodPost, "/api/v1/users/"+itoa(id)+"/tokens", "adm", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("mint: %d %s", rec.Code, rec.Body.String())
	}
	mb := decode(t, rec)
	tok2, _ := mb["token"].(string)
	if tok2 == "" || tok2 == tok1 {
		t.Fatalf("mint must return a NEW non-empty token, got %q (tok1 %q)", tok2, tok1)
	}
	if mb["level"] != "write" {
		t.Fatalf("default level = %v, want write", mb["level"])
	}
	if mb["user"].(map[string]any)["username"] != "bob" {
		t.Fatalf("mint response user = %v", mb["user"])
	}

	// BOTH tokens authenticate (the old one is NOT revoked).
	for name, tok := range map[string]string{"tok1": tok1, "tok2": tok2} {
		if rec := hreq(mux, http.MethodGet, "/api/v1/orgs", tok, nil); rec.Code != http.StatusOK {
			t.Fatalf("%s no longer authenticates: %d %s", name, rec.Code, rec.Body.String())
		}
	}

	// A second mint yields yet another distinct token.
	rec = hreq(mux, http.MethodPost, "/api/v1/users/"+itoa(id)+"/tokens", "adm", map[string]any{"level": "read"})
	if rec.Code != http.StatusOK {
		t.Fatalf("second mint: %d %s", rec.Code, rec.Body.String())
	}
	mb2 := decode(t, rec)
	tok3, _ := mb2["token"].(string)
	if tok3 == "" || tok3 == tok1 || tok3 == tok2 {
		t.Fatalf("second mint must be distinct: %q", tok3)
	}
	if mb2["level"] != "read" {
		t.Fatalf("level = %v, want read", mb2["level"])
	}
	if rec := hreq(mux, http.MethodGet, "/api/v1/orgs", tok3, nil); rec.Code != http.StatusOK {
		t.Fatalf("read token does not authenticate: %d", rec.Code)
	}
}

// TestMintUserTokenAuthAndErrors covers the failure modes: admin-only, unknown
// user, bad level, and an unconfigured admin surface.
func TestMintUserTokenAuthAndErrors(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)

	// No admin token configured -> 503 (matches the rest of the admin surface).
	if rec := hreq(mux, http.MethodPost, "/api/v1/users/1/tokens", "adm", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("admin unconfigured: got %d, want 503", rec.Code)
	}
	t.Setenv("EASYVCS_ADMIN_TOKEN", "adm")

	// Wrong admin credential -> 403.
	if rec := hreq(mux, http.MethodPost, "/api/v1/users/1/tokens", "wrong", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("wrong admin: got %d, want 403", rec.Code)
	}
	// A normal (non-admin) user token is NOT the admin token -> 403.
	if rec := hreq(mux, http.MethodPost, "/api/v1/users/1/tokens", token, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin token: got %d, want 403", rec.Code)
	}
	// Anonymous -> 403 (no bearer at all; requireAdmin treats it as forbidden).
	if rec := hreq(mux, http.MethodPost, "/api/v1/users/1/tokens", "", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous: got %d, want 403", rec.Code)
	}

	// Unknown user id -> 404.
	if rec := hreq(mux, http.MethodPost, "/api/v1/users/9999/tokens", "adm", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown user: got %d, want 404", rec.Code)
	}

	// Bad level -> 400 (user must exist first).
	rec := hreq(mux, http.MethodPost, "/api/v1/users", "adm", map[string]any{"username": "carol"})
	id := int64(decode(t, rec)["user"].(map[string]any)["id"].(float64))
	if rec := hreq(mux, http.MethodPost, "/api/v1/users/"+itoa(id)+"/tokens", "adm", map[string]any{"level": "root"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad level: got %d, want 400", rec.Code)
	}
}
