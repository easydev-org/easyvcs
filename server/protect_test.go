package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/easylab-platform/easyvcs/store"
	"github.com/easylab-platform/easyvcs/transfer"
)

// branchBundle builds a push bundle that moves `branch` to `target` and carries
// every revision (so the target always resolves).
func branchBundle(t *testing.T, repo *store.Repo, branch, target string) []byte {
	t.Helper()
	revs, err := repo.ListRevisions()
	if err != nil {
		t.Fatal(err)
	}
	b := &transfer.Bundle{Version: transfer.Version, Repo: repo.NSRef(),
		Revisions: revs, Refs: []*store.Ref{{Name: branch, Kind: store.RefBranch, Target: target}}}
	payload, err := transfer.CompressBundle(b)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

// push posts a bundle to the repo's push endpoint and returns the status.
func push(t *testing.T, s *Server, payload []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/team-app/push", bytes.NewReader(payload))
	req.Header.Set("Content-Encoding", "gzip")
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	return rec
}

// childAndRoot returns the child and root revision ids of the seeded repo.
func childAndRoot(t *testing.T, repo *store.Repo) (child, root string) {
	t.Helper()
	revs, err := repo.ListRevisions()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range revs {
		snap, err := repo.GetSnapshot(r.Hash)
		if err != nil {
			t.Fatal(err)
		}
		if len(snap.Parents) == 0 {
			root = r.ID
		} else {
			child = r.ID
		}
	}
	if child == "" || root == "" {
		t.Fatalf("need a root and a child revision, got root=%q child=%q", root, child)
	}
	return
}

// A new repository protects `main` by default.
func TestProtectedBranchesDefaultMain(t *testing.T) {
	s := newTestServer(t, "secret")
	repo, err := s.cs.Create(store.NSRef{Owner: 0, Name: "team-app"})
	if err != nil {
		t.Fatal(err)
	}
	branches, err := repo.ProtectedBranches()
	if err != nil {
		t.Fatal(err)
	}
	if len(branches) != 1 || branches[0] != "main" {
		t.Fatalf("new repo should protect [main], got %v", branches)
	}
}

// A push that MOVES a protected branch is refused (fast-forward and
// non-fast-forward alike), while a push to an unprotected branch succeeds.
// A push may only create/move the caller's OWN PRIVATE branch. Moving ANY
// public branch (main included) is refused: public change is RR-only.
func TestPushPublicBranchRefused(t *testing.T) {
	s := newTestServer(t, "secret")
	seedRepo(t, s)
	repo, _ := s.cs.OpenRepo(store.NSRef{Name: "team-app"})
	child, _ := childAndRoot(t, repo)

	// Fast-forward main -> child: refused (main is PUBLIC).
	rec := push(t, s, branchBundle(t, repo, "main", child))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("push moving public main should be 403, got %d %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["error"] != "public-branch-rr-only" {
		t.Fatalf("expected error=public-branch-rr-only, got %v", resp["error"])
	}
	if msg, _ := resp["message"].(string); msg == "" || !strings.Contains(msg, "Revision Request") {
		t.Fatalf("message should mention Revision Request, got %q", msg)
	}

	// Pushing a NEW branch name succeeds: it is created PRIVATE to the caller.
	rec = push(t, s, branchBundle(t, repo, "myprivate", child))
	if rec.Code != http.StatusOK {
		t.Fatalf("push creating a private branch should be 200, got %d %s", rec.Code, rec.Body.String())
	}
}

// A no-op push (the protected ref does not move) is allowed.
func TestPushProtectedBranchNoOpAllowed(t *testing.T) {
	s := newTestServer(t, "secret")
	seedRepo(t, s)
	repo, _ := s.cs.OpenRepo(store.NSRef{Name: "team-app"})
	if err := repo.SetProtectedBranches([]string{"main"}); err != nil {
		t.Fatal(err)
	}
	mainRef, _ := repo.GetRef("main")
	rec := push(t, s, branchBundle(t, repo, "main", mainRef.Target))
	if rec.Code != http.StatusOK {
		t.Fatalf("no-op push to protected main should be 200, got %d %s", rec.Code, rec.Body.String())
	}
}

// Changing the protected set retargets the rule: with ["release/x"], main is
// pushable and release/x is not.
// Regardless of the protected set, a PUBLIC branch may not be pushed (RR-only);
// a push may still CREATE a new private branch.
func TestPushPublicBranchesAreRROnly(t *testing.T) {
	s := newTestServer(t, "secret")
	seedRepo(t, s)
	repo, _ := s.cs.OpenRepo(store.NSRef{Name: "team-app"})
	// Even with NO protected branches, main is public -> RR-only.
	if err := repo.SetProtectedBranches([]string{}); err != nil {
		t.Fatal(err)
	}
	child, _ := childAndRoot(t, repo)
	rec := push(t, s, branchBundle(t, repo, "main", child))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("main (public) should be RR-only, got %d %s", rec.Code, rec.Body.String())
	}
	// A brand-new branch name is created PRIVATE and allowed.
	rec = push(t, s, branchBundle(t, repo, "draft-a", child))
	if rec.Code != http.StatusOK {
		t.Fatalf("push creating a private branch should be 200, got %d %s", rec.Code, rec.Body.String())
	}
}

// The protected-branch endpoints read/update the set (owner-managed).
func TestProtectedBranchEndpoints(t *testing.T) {
	s := newTestServer(t, "secret")
	seedRepo(t, s)

	get := func() []string {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/team-app/protected-branches", nil)
		req.Header.Set("Authorization", "Bearer secret")
		s.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("get protected: %d %s", rec.Code, rec.Body.String())
		}
		var out struct {
			Branches []string `json:"protected_branches"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Branches
	}
	put := func(list []string) {
		body, _ := json.Marshal(map[string]any{"protected_branches": list})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/api/v1/namespaces/team-app/protected-branches", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer secret")
		s.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("put protected: %d %s", rec.Code, rec.Body.String())
		}
	}

	put([]string{"main", "release/x", "main"}) // de-dup
	got := get()
	if len(got) != 2 || got[0] != "main" || got[1] != "release/x" {
		t.Fatalf("protected set = %v, want [main release/x]", got)
	}
	put(nil) // clear
	if got := get(); len(got) != 0 {
		t.Fatalf("cleared protected set should be empty, got %v", got)
	}
}
