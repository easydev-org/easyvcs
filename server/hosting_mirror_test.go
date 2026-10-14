package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/easylab-platform/easyvcs/store"
)

// ---- push-mirror endpoints ----

func pmReq(t *testing.T, s *Server, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	return rec
}

// TestPushMirrorCRUD covers create -> list (no token) -> delete -> list empty.
func TestPushMirrorCRUD(t *testing.T) {
	s := newTestServer(t, "secret")
	seedRepo(t, s)

	rec := pmReq(t, s, http.MethodPost, "/api/v1/namespaces/team-app/push-mirrors", map[string]any{
		"name": "dest", "url": "https://example.invalid/org/repo.git", "branch": "main", "token": "s3cr3t",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("s3cr3t")) {
		t.Fatalf("create response leaked the token: %s", rec.Body.String())
	}

	rec = pmReq(t, s, http.MethodGet, "/api/v1/namespaces/team-app/push-mirrors", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte("s3cr3t")) {
		t.Fatalf("list response leaked the token: %s", rec.Body.String())
	}
	var listed struct {
		Mirrors []struct {
			Name string `json:"name"`
			URL  string `json:"url"`
		} `json:"push_mirrors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &listed)
	if len(listed.Mirrors) != 1 || listed.Mirrors[0].Name != "dest" {
		t.Fatalf("list = %+v, want one mirror named dest", listed.Mirrors)
	}

	rec = pmReq(t, s, http.MethodDelete, "/api/v1/namespaces/team-app/push-mirrors/dest", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body.String())
	}
	rec = pmReq(t, s, http.MethodGet, "/api/v1/namespaces/team-app/push-mirrors", nil)
	var after struct {
		Mirrors []any `json:"push_mirrors"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &after)
	if len(after.Mirrors) != 0 {
		t.Fatalf("after delete = %+v, want empty", after.Mirrors)
	}
}

// TestPushMirrorSyncToBare pushes to a LOCAL bare repo (file:// URL) and
// verifies the content lands, then updates on a second sync. It also verifies a
// bad destination records last_error without breaking the server.
func TestPushMirrorSyncToBare(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	s := newTestServer(t, "secret")
	seedRepo(t, s)
	// seedRepo sets main to rev1 (a.txt only). Grab a repo handle to push.
	repo, err := s.cs.OpenRepo(store.NSRef{Name: "team-app"})
	if err != nil {
		t.Fatal(err)
	}

	dest := t.TempDir()
	runGit(t, "", "init", "--bare", "--quiet", dest)

	rec := pmReq(t, s, http.MethodPost, "/api/v1/namespaces/team-app/push-mirrors", map[string]any{
		"name": "dest", "url": "file://" + dest, "branch": "main",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create: %d %s", rec.Code, rec.Body.String())
	}

	rec = pmReq(t, s, http.MethodPost, "/api/v1/namespaces/team-app/push-mirrors/dest/sync", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body.String())
	}
	// The bare repo now has a main branch with a.txt.
	if out := runGitOut(t, dest, "ls-tree", "-r", "--name-only", "main"); out != "a.txt\n" {
		t.Fatalf("mirrored tree = %q, want a.txt", out)
	}
	// last_rev was recorded.
	m, _ := repo.GetPushMirror("dest")
	if m.LastRev == "" || m.LastError != "" {
		t.Fatalf("mirror state = %+v, want a non-empty last_rev and empty last_error", m)
	}

	// A bad destination records last_error and does not break the server.
	rec = pmReq(t, s, http.MethodPost, "/api/v1/namespaces/team-app/push-mirrors", map[string]any{
		"name": "bad", "url": "file:///nonexistent/does/not/exist.git", "branch": "main",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create bad: %d %s", rec.Code, rec.Body.String())
	}
	rec = pmReq(t, s, http.MethodPost, "/api/v1/namespaces/team-app/push-mirrors/bad/sync", nil)
	if rec.Code == http.StatusOK {
		t.Fatalf("sync of a bad destination should fail, got 200")
	}
	bm, _ := repo.GetPushMirror("bad")
	if bm.LastError == "" {
		t.Fatalf("bad mirror last_error not recorded: %+v", bm)
	}
}

// TestPushMirrorAuth: non-owner create/delete/sync => 403; list needs read.
func TestPushMirrorAuth(t *testing.T) {
	s := newTestServer(t, "secret") // user "tester" owns team/*; token "secret"
	seedRepo(t, s)
	// A second user (no role on team/app) with a write token.
	u2, err := s.cs.CreateUser("intruder", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.cs.CreateToken("intruder-token", u2.ID, "write"); err != nil {
		t.Fatal(err)
	}

	do := func(token, method, path string) int {
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(`{"name":"x","url":"file:///x"}`)))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		s.Router().ServeHTTP(rec, req)
		return rec.Code
	}
	if c := do("intruder-token", http.MethodPost, "/api/v1/namespaces/team-app/push-mirrors"); c != http.StatusForbidden {
		t.Fatalf("non-owner create = %d, want 403", c)
	}
	if c := do("intruder-token", http.MethodDelete, "/api/v1/namespaces/team-app/push-mirrors/x"); c != http.StatusForbidden {
		t.Fatalf("non-owner delete = %d, want 403", c)
	}
	if c := do("intruder-token", http.MethodPost, "/api/v1/namespaces/team-app/push-mirrors/x/sync"); c != http.StatusForbidden {
		t.Fatalf("non-owner sync = %d, want 403", c)
	}
	// Anonymous read on a private repo => 401/403.
	if err := repoSetPrivate(t, s, "team", "app"); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/team-app/push-mirrors", nil)
	rec := httptest.NewRecorder()
	s.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous list on private repo = %d, want 401/403", rec.Code)
	}
}

// TestSyncAllPushMirrors drives one scheduler pass directly.
func TestSyncAllPushMirrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git CLI not available")
	}
	s := newTestServer(t, "secret")
	seedRepo(t, s)
	dest := t.TempDir()
	runGit(t, "", "init", "--bare", "--quiet", dest)
	repo, _ := s.cs.OpenRepo(store.NSRef{Name: "team-app"})
	if err := repo.PutPushMirror(&store.PushMirror{Name: "sched", URL: "file://" + dest, Branch: "main"}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	SyncAllPushMirrors(ctx, s.cs, 20*time.Second)
	if out := runGitOut(t, dest, "ls-tree", "-r", "--name-only", "main"); out != "a.txt\n" {
		t.Fatalf("scheduler pass mirrored tree = %q, want a.txt", out)
	}
}

// ---- git helpers (local to this test file) ----

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, string(out))
	}
}

func runGitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", args, err, string(out))
	}
	return string(out)
}

func repoSetPrivate(t *testing.T, s *Server, ns, name string) error {
	t.Helper()
	repo, err := s.cs.OpenRepo(store.NSRef{Name: ns + "-" + name})
	if err != nil {
		return err
	}
	return repo.UpdateRepoMeta(store.RepoMeta{Visibility: "private", DefaultBranch: "main"})
}
