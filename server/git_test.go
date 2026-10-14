package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestGitInfoRefsRoot is the regression test for the root-repo git smart-HTTP
// path. It goes through the REAL Router() (not a hand-built mux), so it catches
// routing bugs like the catch-all `.../{name}/` losing the leading slash for the
// root repo (which made `git clone .../{ns}/{name}` 404).
func TestGitInfoRefsRoot(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)

	// Root repo: the MOST common clone path — must be 200 (advertisement).
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/team-app/info/refs?service=git-upload-pack", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("root info/refs = %d, want 200 (body: %s)", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "git-upload-pack-advertisement") {
		t.Fatalf("root info/refs Content-Type = %q", ct)
	}
	// The advertisement must include the smart prefix and the refs.
	if !strings.Contains(rec.Body.String(), "# service=git-upload-pack") {
		t.Fatalf("advertisement missing smart prefix: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "refs/heads/main") {
		t.Fatalf("advertisement missing refs/heads/main: %q", rec.Body.String())
	}

	// A ".git"-suffixed root clone URL must resolve too.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/team-app.git/info/refs?service=git-upload-pack", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("root .git info/refs = %d, want 200", rec.Code)
	}

	// A non-git subpath is not the git endpoint -> 404.
	rec = httptest.NewRecorder()
	r = httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/team-app/not-a-git-path", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("non-git subpath = %d, want 404", rec.Code)
	}
}
