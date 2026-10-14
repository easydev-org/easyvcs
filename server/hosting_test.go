package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// hostingTestServer builds a Server over a fresh store with a user+token and a
// repo `team/app` owned by that user, plus an initial `main` commit. It returns
// the server, the mux, the user's write token, and the repo handle.
func hostingTestServer(t *testing.T) (*Server, *http.ServeMux, string, *store.Repo) {
	t.Helper()
	s := newTestServer(t, "alice-token") // creates user "tester" + token "alice-token"
	u, err := s.cs.GetUserByUsername("tester")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cs.EnsureNamespace(u.ID, "team"); err != nil {
		t.Fatal(err)
	}
	repo, err := s.cs.Create(store.NSRef{Owner: u.ID, Name: "team/app"})
	if err != nil {
		t.Fatal(err)
	}
	makePublic(t, repo)
	ws := revision.NewWorkspace(repo)
	blob, _ := ws.WriteBlob([]byte("v1\n"))
	tree := object.NewTree()
	tree.Entries["README.md"] = object.Entry{Name: "README.md", Kind: object.KindBlob, ID: blob}
	tid, _ := ws.WriteTree(tree)
	snap, rev, err := ws.Commit(revision.CommitParams{TreeID: tid, Description: "base", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("main", store.RefBranch, rev.ID); err != nil {
		t.Fatal(err)
	}
	_ = snap
	return s, s.Router(), "alice-token", repo
}

// hreq performs an HTTP request against mux with an optional bearer token.
func hreq(mux *http.ServeMux, method, path, token string, body any) *httptest.ResponseRecorder {
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

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return m
}

func TestWhoami(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)

	rec := hreq(mux, http.MethodGet, "/api/v1/whoami", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("whoami: %d %s", rec.Code, rec.Body.String())
	}
	if got := decode(t, rec)["username"]; got != "tester" {
		t.Fatalf("whoami username = %v, want tester", got)
	}

	rec = hreq(mux, http.MethodGet, "/api/v1/whoami", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("anon whoami: %d", rec.Code)
	}
	m := decode(t, rec)
	if m["username"] != "anonymous" || m["authenticated"] != false {
		t.Fatalf("anon whoami = %v", m)
	}
}

func TestHostingUserOrgRepoLifecycle(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)

	// Admin surface is unavailable without EASYVCS_ADMIN_TOKEN.
	if rec := hreq(mux, http.MethodGet, "/api/v1/users", token, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("admin without env: got %d, want 503", rec.Code)
	}
	t.Setenv("EASYVCS_ADMIN_TOKEN", "adm")
	if rec := hreq(mux, http.MethodGet, "/api/v1/users", "wrong", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("admin wrong token: got %d, want 403", rec.Code)
	}

	// Create a user via the admin surface; it returns a one-time token.
	rec := hreq(mux, http.MethodPost, "/api/v1/users", "adm", map[string]any{"username": "bob", "display_name": "Bob"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create user: %d %s", rec.Code, rec.Body.String())
	}
	bobTok, _ := decode(t, rec)["token"].(string)
	if bobTok == "" {
		t.Fatal("create user returned no token")
	}

	// bob creates his own container "boborg/tool" (a namespace; there is no
	// separate repository entity under it).
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/boborg/tool", bobTok, map[string]any{"visibility": "private"}); rec.Code != http.StatusOK {
		t.Fatalf("create namespace: %d %s", rec.Code, rec.Body.String())
	}

	// Make alice's repo private so visibility filtering is observable (a public
	// repo is visible to everyone by design).
	if rec := hreq(mux, http.MethodPatch, "/api/v1/namespaces/team/app", token, map[string]any{"visibility": "private"}); rec.Code != http.StatusOK {
		t.Fatalf("privatize team/app: %d %s", rec.Code, rec.Body.String())
	}

	// bob sees only his own repos.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces", bobTok, nil)
	repos, _ := decode(t, rec)["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("bob should see 1 repo, got %v", repos)
	}
	first := repos[0].(map[string]any)
	if first["namespace"] != "boborg/tool" {
		t.Fatalf("unexpected namespace %v", first)
	}
	// The list carries default_branch + private inline, so a client does not
	// have to GET each repo individually.
	if first["default_branch"] != "main" {
		t.Fatalf("list default_branch = %v, want main", first["default_branch"])
	}
	if first["private"] != true {
		t.Fatalf("list private = %v, want true (boborg/tool is private)", first["private"])
	}

	// GET repo meta.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/boborg/tool", bobTok, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("get repo: %d", rec.Code)
	}
	if v := decode(t, rec)["visibility"]; v != "private" {
		t.Fatalf("visibility = %v, want private", v)
	}

	// PATCH visibility to public.
	rec = hreq(mux, http.MethodPatch, "/api/v1/namespaces/boborg/tool", bobTok, map[string]any{"visibility": "public"})
	if rec.Code != http.StatusOK {
		t.Fatalf("patch repo: %d %s", rec.Code, rec.Body.String())
	}
	if v := decode(t, rec)["visibility"]; v != "public" {
		t.Fatalf("after patch visibility = %v, want public", v)
	}

	// DELETE the repo.
	if rec := hreq(mux, http.MethodDelete, "/api/v1/namespaces/boborg/tool", bobTok, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete repo: %d %s", rec.Code, rec.Body.String())
	}
	// The repo is gone: a read is refused (403/404 — a missing repo does not
	// leak its existence, so the ACL check denies before the 404).
	if rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/boborg/tool", bobTok, nil); rec.Code != http.StatusNotFound && rec.Code != http.StatusForbidden {
		t.Fatalf("deleted repo get: got %d, want 404/403", rec.Code)
	}
}

func TestHostingTokens(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)

	// List own tokens (the seed token).
	rec := hreq(mux, http.MethodGet, "/api/v1/tokens", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("list tokens: %d", rec.Code)
	}
	if n := len(decode(t, rec)["tokens"].([]any)); n != 1 {
		t.Fatalf("expected 1 token, got %d", n)
	}

	// Create a read token, then delete it by id.
	rec = hreq(mux, http.MethodPost, "/api/v1/tokens", token, map[string]any{"level": "read"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create token: %d %s", rec.Code, rec.Body.String())
	}
	m := decode(t, rec)
	newTok, _ := m["token"].(string)
	if newTok == "" {
		t.Fatal("no plaintext token returned")
	}
	id := int64(m["id"].(float64))
	if rec := hreq(mux, http.MethodDelete, "/api/v1/tokens/"+itoa(id), token, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete token: %d %s", rec.Code, rec.Body.String())
	}
	// The new token no longer authenticates.
	if rec := hreq(mux, http.MethodGet, "/api/v1/whoami", newTok, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("deleted token still works: %d", rec.Code)
	}
}

func TestHostingRefs(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)

	// Create a branch from main.
	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches", token, map[string]any{"name": "feature", "from": "main"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create branch: %d %s", rec.Code, rec.Body.String())
	}
	// Branches list includes it.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/branches", token, nil)
	names := map[string]bool{}
	for _, b := range decode(t, rec)["branches"].([]any) {
		names[b.(map[string]any)["name"].(string)] = true
	}
	if !names["main"] || !names["feature"] {
		t.Fatalf("branches = %v", names)
	}

	// Tag main (immutable).
	rec = hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/tags", token, map[string]any{"name": "v1", "target": "main"})
	if rec.Code != http.StatusOK {
		t.Fatalf("create tag: %d %s", rec.Code, rec.Body.String())
	}
	// Re-tagging the same name is refused (immutable).
	rec = hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/tags", token, map[string]any{"name": "v1", "target": "main"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("re-tag: got %d, want 409", rec.Code)
	}

	// Delete the branch.
	if rec := hreq(mux, http.MethodDelete, "/api/v1/namespaces/team/app/branches/feature", token, nil); rec.Code != http.StatusOK {
		t.Fatalf("delete branch: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := repo.GetRef("feature"); err == nil {
		t.Fatal("feature branch should be gone")
	}
}

func TestHostingBrowse(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	ws := revision.NewWorkspace(repo)

	// Commit #2: modify README and add a binary file.
	bin := []byte{0x00, 0x01, 0x02, 0x00, 0xff}
	binBlob, _ := ws.WriteBlob(bin)
	rdBlob, _ := ws.WriteBlob([]byte("v2\n"))
	tree := object.NewTree()
	tree.Entries["README.md"] = object.Entry{Name: "README.md", Kind: object.KindBlob, ID: rdBlob}
	tree.Entries["img.bin"] = object.Entry{Name: "img.bin", Kind: object.KindBlob, ID: binBlob}
	tid, _ := ws.WriteTree(tree)
	mainRef, _ := ws.GetRef("main")
	prev, _ := repo.GetRevision(mainRef.Target)
	_, rev2, err := ws.Commit(revision.CommitParams{Parents: []object.ID{prev.Hash}, TreeID: tid, Description: "c2", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("main", store.RefBranch, rev2.ID); err != nil {
		t.Fatal(err)
	}

	// tree at main lists both files.
	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/tree?ref=main", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree: %d %s", rec.Code, rec.Body.String())
	}
	entries := decode(t, rec)["entries"].([]any)
	if len(entries) != 2 {
		t.Fatalf("tree entries = %v", entries)
	}

	// blob (text) returns content.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/blob?ref=main&path=README.md", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("blob: %d %s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["content"] != "v2\n" {
		t.Fatalf("blob content = %v", decode(t, rec)["content"])
	}

	// blob (binary) is flagged, not inlined.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/blob?ref=main&path=img.bin", token, nil)
	if decode(t, rec)["binary"] != true {
		t.Fatalf("binary blob not flagged: %v", decode(t, rec))
	}

	// raw returns the exact bytes.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/raw?ref=main&path=img.bin", token, nil)
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), bin) {
		t.Fatalf("raw bytes mismatch: %d %v", rec.Code, rec.Body.Bytes())
	}

	// log lists commits newest-first.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/log?ref=main&limit=10", token, nil)
	commits := decode(t, rec)["commits"].([]any)
	if len(commits) != 2 {
		t.Fatalf("log commits = %v", commits)
	}

	// diff main~ (prev) -> main: README modified + img.bin added (binary).
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/diff?base="+prev.ID+"&head=main", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff: %d %s", rec.Code, rec.Body.String())
	}
	files := decode(t, rec)["files"].([]any)
	sawBinary := false
	for _, f := range files {
		fm := f.(map[string]any)
		if fm["path"] == "img.bin" && fm["binary"] == true {
			sawBinary = true
		}
	}
	if !sawBinary {
		t.Fatalf("diff did not flag img.bin as binary: %v", files)
	}

	// commit detail.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/commit/"+rev2.ID, token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body.String())
	}
	if decode(t, rec)["description"] != "c2" {
		t.Fatalf("commit detail = %v", decode(t, rec))
	}

	// blame attributes a README line to c2.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/blame?ref=main&path=README.md", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("blame: %d %s", rec.Code, rec.Body.String())
	}
	lines := decode(t, rec)["lines"].([]any)
	if len(lines) == 0 {
		t.Fatal("blame returned no lines")
	}
}

func TestHostingPermissions(t *testing.T) {
	s, mux, ownerTok, _ := hostingTestServer(t)

	// A second user (bob) with his own token, not a member of team/app.
	bob, err := s.cs.CreateUser("bob", "Bob")
	if err != nil {
		t.Fatal(err)
	}
	bobTok := "bob-token"
	if _, err := s.cs.CreateToken(bobTok, bob.ID, "write"); err != nil {
		t.Fatal(err)
	}

	// Non-owner PATCH/DELETE is refused.
	if rec := hreq(mux, http.MethodPatch, "/api/v1/namespaces/team/app", bobTok, map[string]any{"description": "x"}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner patch: got %d, want 403", rec.Code)
	}
	if rec := hreq(mux, http.MethodDelete, "/api/v1/namespaces/team/app", bobTok, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner delete: got %d, want 403", rec.Code)
	}

	// Anonymous write is 401.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches", "", map[string]any{"name": "x", "from": "main"}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anon write: got %d, want 401", rec.Code)
	}

	// Make the repo private; anonymous read then 403/404.
	ownerTokRec := hreq(mux, http.MethodPatch, "/api/v1/namespaces/team/app", ownerTok, map[string]any{"visibility": "private"})
	if ownerTokRec.Code != http.StatusOK {
		t.Fatalf("owner patch: %d %s", ownerTokRec.Code, ownerTokRec.Body.String())
	}
	if rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app", "", nil); rec.Code != http.StatusForbidden && rec.Code != http.StatusNotFound {
		t.Fatalf("anon read private: got %d, want 403/404", rec.Code)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
