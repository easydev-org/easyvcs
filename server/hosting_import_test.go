package server

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easylab-platform/easyvcs/store"
)

// makeSourceBareRepo builds a local bare git repo with two commits on `main`
// and returns its filesystem path. The import tests use it via a `file://` URL
// so they need no network.
func makeSourceBareRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	work := t.TempDir()
	gitRun(t, work, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(work, "README.md"), []byte("hello\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", ".")
	gitRun(t, work, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "--quiet", "-m", "first")
	if err := os.MkdirAll(filepath.Join(work, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(work, "src", "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, work, "add", ".")
	gitRun(t, work, "-c", "user.name=t", "-c", "user.email=t@x", "commit", "--quiet", "-m", "second")

	bare := t.TempDir()
	// `-b main` so the bare repo's HEAD resolves to a branch that exists after
	// the push (go-git's clone reads HEAD).
	gitRun(t, "", "init", "--bare", "--quiet", "-b", "main", bare)
	gitRun(t, work, "remote", "add", "origin", bare)
	gitRun(t, work, "push", "--quiet", "origin", "main")
	return bare
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, string(out))
	}
}

// TestImportFromLocalRepo imports a local bare repo into a NEW repository and
// verifies the history and content land on `main`.
func TestImportFromLocalRepo(t *testing.T) {
	src := makeSourceBareRepo(t)
	s, mux, token, _ := hostingTestServer(t)

	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/imported/import", token, map[string]any{
		"url": "file://" + src,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("import: %d %s", rec.Code, rec.Body.String())
	}
	m := decode(t, rec)
	if m["ok"] != true || m["branch"] != "main" {
		t.Fatalf("import response = %v", m)
	}
	if n, _ := m["revisions"].(float64); int(n) != 2 {
		t.Fatalf("revisions = %v, want 2", m["revisions"])
	}

	// The imported content is readable at main.
	rec = hreq(mux, http.MethodGet, "/api/v1/repos/team/imported/blob?ref=main&path=src/main.go", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("blob: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "package main") {
		t.Fatalf("blob content = %s", rec.Body.String())
	}

	// The imported history has two commits.
	rec = hreq(mux, http.MethodGet, "/api/v1/repos/team/imported/log?ref=main", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("log: %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Count(rec.Body.String(), "revision_id"); got != 2 {
		t.Fatalf("log has %d commits, want 2: %s", got, rec.Body.String())
	}
	_ = s
}

// TestImportRefusesExistingContent: importing into a repo that already has a
// `main` tip is refused (409) — an import never overwrites.
func TestImportRefusesExistingContent(t *testing.T) {
	src := makeSourceBareRepo(t)
	_, mux, token, _ := hostingTestServer(t) // team/app already has a main commit

	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/import", token, map[string]any{
		"url": "file://" + src,
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("import over existing content = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// TestImportAuth: anonymous is 401; a non-owner importing into an EXISTING repo
// is 403 (owner-level gate); into a NEW repo under another's org is 409.
func TestImportAuth(t *testing.T) {
	src := makeSourceBareRepo(t)
	s, mux, _, _ := hostingTestServer(t)

	// Anonymous.
	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/newone/import", "", map[string]any{"url": "file://" + src})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous import = %d, want 401", rec.Code)
	}

	// A second user with a write token, no role on team/*.
	u2, err := s.cs.CreateUser("intruder", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.cs.CreateToken("intruder-token", u2.ID, "write"); err != nil {
		t.Fatal(err)
	}
	// Into an EXISTING repo the intruder has no role on: 403.
	rec = hreq(mux, http.MethodPost, "/api/v1/repos/team/app/import", "intruder-token", map[string]any{"url": "file://" + src})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner import into existing repo = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	// Into a NEW repo under another user's org: 409.
	rec = hreq(mux, http.MethodPost, "/api/v1/repos/team/newtwo/import", "intruder-token", map[string]any{"url": "file://" + src})
	if rec.Code != http.StatusConflict {
		t.Fatalf("non-owner import into another org = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// TestImportSourceUnreachable: an unreachable source is 502 with a readable
// error, and the caller's token never appears in the response.
func TestImportSourceUnreachable(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)

	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/bad/import", token, map[string]any{
		"url":   "file:///nonexistent/does/not/exist.git",
		"token": "supersecret-token-value",
	})
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("unreachable source = %d, want 502: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "supersecret-token-value") {
		t.Fatalf("response leaked the token: %s", rec.Body.String())
	}
}

// TestImportRequiresURL: a missing url is 400.
func TestImportRequiresURL(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/nourl/import", token, map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing url = %d, want 400", rec.Code)
	}
	_ = store.RepoRef{}
}
