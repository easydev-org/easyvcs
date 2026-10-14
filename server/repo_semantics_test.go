package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/easyvcs/store"
)

// ---- repo semantics: existence vs. permission, init, owned listing ----

// makePublic publishes a repo. New repos default to PRIVATE now, so fixtures
// that expect anonymous / other-user reads must opt in explicitly (mirroring
// what a real caller does when it wants a public repo).
func makePublic(t *testing.T, repo *store.Repo) {
	t.Helper()
	meta, err := repo.RepoMeta()
	if err != nil {
		t.Fatal(err)
	}
	meta.Visibility = "public"
	if err := repo.UpdateRepoMeta(meta); err != nil {
		t.Fatal(err)
	}
}

func hreqRaw(mux *http.ServeMux, method, path, token string, body any) *httptest.ResponseRecorder {
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestGetRepoMissingIs404DistinctFromForbidden pins that GET /repos/{ns}/{name}
// returns 404 for a CONFIRMED absence and 403 only for a repo that exists but
// the caller may not read — so a client can tell "does not exist" from "not
// mine" (the ACL check no longer masks a 404).
func TestGetRepoMissingIs404DistinctFromForbidden(t *testing.T) {
	s := newTestServer(t, "secret") // user "tester" owns team/*
	seedRepo(t, s)
	mux := s.Router()

	// seedRepo publishes team/app; make it PRIVATE again so the outsider case
	// below exercises the 403 (a public repo is readable by everyone).
	repo, err := s.cs.OpenRepo(store.NSRef{Name: "team-app"})
	if err != nil {
		t.Fatal(err)
	}
	meta, _ := repo.RepoMeta()
	meta.Visibility = "private"
	if err := repo.UpdateRepoMeta(meta); err != nil {
		t.Fatal(err)
	}

	// 1. Missing repo (namespace exists, repo does not) => 404.
	if rec := hreqRaw(mux, http.MethodGet, "/api/v1/namespaces/team-nope", "secret", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("missing repo: got %d %s, want 404", rec.Code, rec.Body.String())
	}
	// 2. Owner reads its own (private) repo => 200.
	if rec := hreqRaw(mux, http.MethodGet, "/api/v1/namespaces/team-app", "secret", nil); rec.Code != http.StatusOK {
		t.Fatalf("owner read: got %d %s, want 200", rec.Code, rec.Body.String())
	}
	// 3. A second user (no access) on an EXISTING repo => 403 (not 404).
	u2, err := s.cs.CreateUser("outsider", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.cs.CreateToken("outsider-token", u2.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if rec := hreqRaw(mux, http.MethodGet, "/api/v1/namespaces/team-app", "outsider-token", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("outsider on existing repo: got %d %s, want 403", rec.Code, rec.Body.String())
	}
	// 4. The same outsider on a MISSING repo still gets 404 (absence wins).
	if rec := hreqRaw(mux, http.MethodGet, "/api/v1/namespaces/team-ghost", "outsider-token", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("outsider on missing repo: got %d %s, want 404", rec.Code, rec.Body.String())
	}
}

// TestCreateRepoInitSeedsMain pins that POST /repos {init:true} writes an initial
// empty commit on `main` (so "create a branch from main" works), WITHOUT
// touching branch protection, and that the default (no init) stays an empty repo.
func TestCreateRepoInitSeedsMain(t *testing.T) {
	s := newTestServer(t, "secret")
	mux := s.Router()

	// init:true => main exists with one revision.
	if rec := hreqRaw(mux, http.MethodPost, "/api/v1/namespaces/team-seeded", "secret", map[string]any{"init": true}); rec.Code != http.StatusOK {
		t.Fatalf("create init repo: %d %s", rec.Code, rec.Body.String())
	}
	refs := hreqRaw(mux, http.MethodGet, "/api/v1/namespaces/team-seeded/refs", "secret", nil)
	if refs.Code != http.StatusOK || !bytes.Contains(refs.Body.Bytes(), []byte(`"main"`)) {
		t.Fatalf("seeded repo refs: %d %s", refs.Code, refs.Body.String())
	}
	log := hreqRaw(mux, http.MethodGet, "/api/v1/namespaces/team-seeded/log?ref=main", "secret", nil)
	if log.Code != http.StatusOK || !bytes.Contains(log.Body.Bytes(), []byte("initial commit")) {
		t.Fatalf("seeded repo log: %d %s", log.Code, log.Body.String())
	}
	// main is still PROTECTED: a direct commit is refused (bootstrap did not
	// relax protection).
	commit := hreqRaw(mux, http.MethodPost, "/api/v1/namespaces/team-seeded/commit", "secret",
		map[string]any{"branch": "main", "message": "x", "files": []any{map[string]any{"path": "a.txt", "operation": "create", "content": "x"}}})
	if commit.Code != http.StatusForbidden {
		t.Fatalf("direct commit to seeded main: got %d %s, want 403", commit.Code, commit.Body.String())
	}

	// Default (no init) => empty repo (no refs), unchanged behaviour.
	if rec := hreqRaw(mux, http.MethodPost, "/api/v1/namespaces/team-empty", "secret", map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("create empty repo: %d %s", rec.Code, rec.Body.String())
	}
	refs = hreqRaw(mux, http.MethodGet, "/api/v1/namespaces/team-empty/refs", "secret", nil)
	if refs.Code != http.StatusOK || bytes.Contains(refs.Body.Bytes(), []byte(`"main"`)) {
		t.Fatalf("empty repo should have no main ref: %d %s", refs.Code, refs.Body.String())
	}
}

// TestListReposOwnedAndDefaultPrivate pins (a) `?owned=true` lists only the
// caller's own/member repos (never another tenant's public ones) and (b) new
// repos default to PRIVATE unless visibility:"public" is explicit.
func TestListReposOwnedAndDefaultPrivate(t *testing.T) {
	s := newTestServer(t, "secret") // "tester" owns team/*
	mux := s.Router()
	if rec := hreqRaw(mux, http.MethodPost, "/api/v1/namespaces", "secret", map[string]any{"org": "team"}); rec.Code != http.StatusOK {
		t.Fatalf("create org: %d %s", rec.Code, rec.Body.String())
	}
	if rec := hreqRaw(mux, http.MethodPost, "/api/v1/namespaces/team-mine", "secret", map[string]any{}); rec.Code != http.StatusOK {
		t.Fatalf("create repo: %d %s", rec.Code, rec.Body.String())
	}
	// Default visibility is private.
	if rec := hreqRaw(mux, http.MethodGet, "/api/v1/namespaces/team-mine", "secret", nil); !bytes.Contains(rec.Body.Bytes(), []byte(`"visibility":"private"`)) {
		t.Fatalf("default visibility should be private: %s", rec.Body.String())
	}

	// A second tenant with its own PUBLIC repo.
	u2, err := s.cs.CreateUser("other", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.cs.CreateToken("other-token", u2.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if rec := hreqRaw(mux, http.MethodPost, "/api/v1/namespaces", "other-token", map[string]any{"org": "otherorg"}); rec.Code != http.StatusOK {
		t.Fatalf("create other org: %d %s", rec.Code, rec.Body.String())
	}
	if rec := hreqRaw(mux, http.MethodPost, "/api/v1/namespaces/otherorg-pub", "other-token", map[string]any{"visibility": "public"}); rec.Code != http.StatusOK {
		t.Fatalf("create public repo: %d %s", rec.Code, rec.Body.String())
	}

	// tester's ?owned=true must NOT include otherorg/pub.
	owned := hreqRaw(mux, http.MethodGet, "/api/v1/namespaces?owned=true", "secret", nil)
	if owned.Code != http.StatusOK {
		t.Fatalf("owned list: %d %s", owned.Code, owned.Body.String())
	}
	if bytes.Contains(owned.Body.Bytes(), []byte("otherorg")) {
		t.Fatalf("owned list leaked another tenant's repo: %s", owned.Body.String())
	}
	if !bytes.Contains(owned.Body.Bytes(), []byte("team-mine")) {
		t.Fatalf("owned list should contain mine: %s", owned.Body.String())
	}
	// Without ?owned it may include the other tenant's public repo (legacy).
	all := hreqRaw(mux, http.MethodGet, "/api/v1/namespaces", "secret", nil)
	if !bytes.Contains(all.Body.Bytes(), []byte("otherorg")) {
		t.Fatalf("legacy list should include public repos: %s", all.Body.String())
	}
}
