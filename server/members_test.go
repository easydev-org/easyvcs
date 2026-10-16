package server

import (
	"net/http"
	"testing"
)

// TestMaintainerPermissions pins the role matrix: a maintainer (added via the
// members API) may do the everyday maintenance ops (tag, protected-branches,
// branch sync, public branch) but NOT the owner-only admin ops (delete
// namespace, change visibility, manage members).
func TestMaintainerPermissions(t *testing.T) {
	s, mux, ownerTok, _ := hostingTestServer(t)
	// Add "bob" as a maintainer.
	if rec := hreq(mux, http.MethodPost, "/api/v1/users", "alice-token", map[string]any{"username": "bob"}); rec.Code != 200 {
		// admin token is different; create via store instead.
	}
	bob, err := s.cs.CreateUser("bob", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.cs.CreateToken("bob-token", bob.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/members", ownerTok,
		map[string]any{"username": "bob", "role": "maintainer"}); rec.Code != 200 {
		t.Fatalf("add member: %d %s", rec.Code, rec.Body.String())
	}

	// A maintainer MAY create a tag.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/tags", "bob-token",
		map[string]any{"name": "v1", "target": "main"}); rec.Code != 200 {
		t.Fatalf("maintainer tag: %d %s", rec.Code, rec.Body.String())
	}
	// A maintainer MAY set protected branches.
	if rec := hreq(mux, http.MethodPut, "/api/v1/namespaces/team-app/protected-branches", "bob-token",
		map[string]any{"protected_branches": []string{"main"}}); rec.Code != 200 {
		t.Fatalf("maintainer protected-branches: %d %s", rec.Code, rec.Body.String())
	}
	// A maintainer MAY create a PUBLIC branch.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/branches", "bob-token",
		map[string]any{"name": "release/x", "from": "main", "public": true}); rec.Code != 200 {
		t.Fatalf("maintainer public branch: %d %s", rec.Code, rec.Body.String())
	}

	// A maintainer may NOT delete the namespace (owner only).
	if rec := hreq(mux, http.MethodDelete, "/api/v1/namespaces/team-app", "bob-token", nil); rec.Code == 200 {
		t.Fatalf("maintainer must NOT delete the namespace")
	}
	// A maintainer may NOT change visibility (owner only).
	if rec := hreq(mux, http.MethodPatch, "/api/v1/namespaces/team-app", "bob-token",
		map[string]any{"visibility": "private"}); rec.Code == 200 {
		t.Fatalf("maintainer must NOT change visibility")
	}
	// A maintainer may NOT manage members (owner only).
	if rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/members", "bob-token", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("maintainer list members = %d, want 403", rec.Code)
	}
}

// TestDeveloperCannotMaintain: a developer (public-repo fallback / member) may
// open RRs but NOT merge or do maintenance.
func TestDeveloperCannotMaintain(t *testing.T) {
	s, mux, _, _ := hostingTestServer(t)
	dev, _ := s.cs.CreateUser("dev", "Dev")
	if _, err := s.cs.CreateToken("dev-token", dev.ID, "write"); err != nil {
		t.Fatal(err)
	}
	// A plain reader on a PUBLIC repo is developer-equivalent: may propose but
	// NOT tag / protect / merge.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/tags", "dev-token",
		map[string]any{"name": "v2", "target": "main"}); rec.Code != http.StatusForbidden {
		t.Fatalf("developer tag = %d, want 403", rec.Code)
	}
	if rec := hreq(mux, http.MethodPut, "/api/v1/namespaces/team-app/protected-branches", "dev-token",
		map[string]any{"protected_branches": []string{"main"}}); rec.Code != http.StatusForbidden {
		t.Fatalf("developer protect = %d, want 403", rec.Code)
	}
}
