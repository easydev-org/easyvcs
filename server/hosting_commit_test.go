package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// commitPost sends a /commit body to team/app and returns the recorder.
func commitPost(t *testing.T, mux *http.ServeMux, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return hreq(mux, http.MethodPost, "/api/v1/repos/team/app/commit", token, body)
}

// newBranch creates a feature branch off the repo's main commit (a derived
// revision, matching handleCreateBranch).
func newBranch(t *testing.T, repo *store.Repo, name string) {
	t.Helper()
	ws := revision.NewWorkspace(repo)
	ref, err := ws.GetRef("main")
	if err != nil || ref == nil {
		t.Fatalf("main ref: %v", err)
	}
	_, ch, err := ws.Derive(ref.Target, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef(name, store.RefBranch, ch.ID); err != nil {
		t.Fatal(err)
	}
}

func has(s, sub string) bool { return strings.Contains(s, sub) }

func TestCommitCreateAndReadBack(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	newBranch(t, repo, "feature/x")

	rec := commitPost(t, mux, token, map[string]any{
		"branch": "feature/x", "message": "add files",
		"files": []map[string]any{
			{"path": "a.txt", "operation": "create", "content": "hello\n"},
			{"path": "sub/b.txt", "operation": "create", "content": "world\n"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body.String())
	}
	tr := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/tree?ref=feature/x&path=sub", token, nil)
	if tr.Code != http.StatusOK {
		t.Fatalf("tree: %d %s", tr.Code, tr.Body.String())
	}
	var tv struct {
		Entries []struct {
			Path string `json:"path"`
		} `json:"entries"`
	}
	_ = json.Unmarshal(tr.Body.Bytes(), &tv)
	if len(tv.Entries) != 1 || tv.Entries[0].Path != "sub/b.txt" {
		t.Fatalf("tree entries = %+v, want sub/b.txt", tv.Entries)
	}
	bl := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/blob?ref=feature/x&path=a.txt", token, nil)
	if bl.Code != http.StatusOK || !has(bl.Body.String(), "hello") {
		t.Fatalf("blob a.txt: %d %s", bl.Code, bl.Body.String())
	}
	mainTree := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/tree?ref=main", token, nil)
	if has(mainTree.Body.String(), "a.txt") {
		t.Fatalf("main must be untouched: %s", mainTree.Body.String())
	}
}

func TestCommitBinaryRoundTrip(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	newBranch(t, repo, "feature/bin")

	raw := []byte{0x00, 0x01, 0x02, 0xff, 0xfe, 0x7f, 0x80}
	rec := commitPost(t, mux, token, map[string]any{
		"branch": "feature/bin", "message": "add bin",
		"files": []map[string]any{
			{"path": "blob.bin", "operation": "create", "content_bytes": base64.StdEncoding.EncodeToString(raw)},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("commit: %d %s", rec.Code, rec.Body.String())
	}
	rawRec := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/raw?ref=feature/bin&path=blob.bin", token, nil)
	if rawRec.Code != http.StatusOK {
		t.Fatalf("raw: %d %s", rawRec.Code, rawRec.Body.String())
	}
	if got := rawRec.Body.Bytes(); string(got) != string(raw) {
		t.Fatalf("binary round trip mismatch: got %v want %v", got, raw)
	}
}

func TestCommitDelete(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	newBranch(t, repo, "feature/del")

	if rec := commitPost(t, mux, token, map[string]any{
		"branch": "feature/del", "message": "add",
		"files": []map[string]any{{"path": "gone.txt", "operation": "create", "content": "x\n"}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("add: %d %s", rec.Code, rec.Body.String())
	}
	if rec := commitPost(t, mux, token, map[string]any{
		"branch": "feature/del", "message": "del",
		"files": []map[string]any{{"path": "gone.txt", "operation": "delete"}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("del: %d %s", rec.Code, rec.Body.String())
	}
	tr := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/tree?ref=feature/del", token, nil)
	if has(tr.Body.String(), "gone.txt") {
		t.Fatalf("gone.txt should be deleted: %s", tr.Body.String())
	}
}

func TestCommitProtectedBranchRefused(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	rec := commitPost(t, mux, token, map[string]any{
		"branch": "main", "message": "nope",
		"files": []map[string]any{{"path": "x.txt", "operation": "create", "content": "x\n"}},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("commit to main should be 403, got %d %s", rec.Code, rec.Body.String())
	}
	if !has(rec.Body.String(), "protected-branch") {
		t.Fatalf("expected protected-branch error, got %s", rec.Body.String())
	}
}

func TestCommitStaleParentConflict(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	newBranch(t, repo, "feature/stale")

	if rec := commitPost(t, mux, token, map[string]any{
		"branch": "feature/stale", "message": "c1",
		"files": []map[string]any{{"path": "a.txt", "operation": "create", "content": "1\n"}},
	}); rec.Code != http.StatusOK {
		t.Fatalf("c1: %d %s", rec.Code, rec.Body.String())
	}
	// Pin an OLD parent (main tip) while the branch has already moved -> 409.
	ws := revision.NewWorkspace(repo)
	mainRef, _ := ws.GetRef("main")
	mainRev, _ := repo.GetRevision(mainRef.Target)
	rec := commitPost(t, mux, token, map[string]any{
		"branch": "feature/stale", "message": "c2",
		"parents": []string{mainRev.Hash.String()},
		"files":   []map[string]any{{"path": "b.txt", "operation": "create", "content": "2\n"}},
	})
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale parent should be 409, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCommitAmendAccumulates(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	newBranch(t, repo, "feature/amend")

	rec := commitPost(t, mux, token, map[string]any{
		"branch": "feature/amend", "message": "staging",
		"files": []map[string]any{{"path": "a.txt", "operation": "create", "content": "a\n"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("staging: %d %s", rec.Code, rec.Body.String())
	}
	revID, _ := decode(t, rec)["revision_id"].(string)
	if revID == "" {
		t.Fatalf("no revision_id returned")
	}
	// Same revision_id -> amend, add another file.
	rec = commitPost(t, mux, token, map[string]any{
		"branch": "feature/amend", "message": "staging", "revision_id": revID,
		"files": []map[string]any{{"path": "b.txt", "operation": "create", "content": "b\n"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("amend: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := decode(t, rec)["revision_id"].(string); got != revID {
		t.Fatalf("amend changed revision id: %s != %s", got, revID)
	}
	tr := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/tree?ref=feature/amend", token, nil)
	if !has(tr.Body.String(), "a.txt") || !has(tr.Body.String(), "b.txt") {
		t.Fatalf("amend must accumulate both files: %s", tr.Body.String())
	}
	// No extra revision beyond the branch's derived base + the amended one.
	revs, _ := repo.ListRevisions()
	if len(revs) != 3 {
		t.Fatalf("amend must not create extra revisions, got %d", len(revs))
	}
	// ChangedPaths of the amended revision reflects BOTH files (vs its parent).
	got, _ := repo.GetRevision(revID)
	joined := strings.Join(got.ChangedPaths, ",")
	if !has(joined, "a.txt") || !has(joined, "b.txt") {
		t.Fatalf("amended ChangedPaths must include a.txt and b.txt, got %v", got.ChangedPaths)
	}
}

func TestCommitReword(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	newBranch(t, repo, "feature/reword")

	rec := commitPost(t, mux, token, map[string]any{
		"branch": "feature/reword", "message": "draft",
		"files": []map[string]any{{"path": "a.txt", "operation": "create", "content": "a\n"}},
	})
	revID, _ := decode(t, rec)["revision_id"].(string)
	oldRev, _ := repo.GetRevision(revID)
	oldSnap, _ := repo.GetSnapshot(oldRev.Hash)

	rec = commitPost(t, mux, token, map[string]any{
		"branch": "feature/reword", "message": "final message", "revision_id": revID,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("reword: %d %s", rec.Code, rec.Body.String())
	}
	newRev, _ := repo.GetRevision(revID)
	newSnap, _ := repo.GetSnapshot(newRev.Hash)
	if newSnap.Description != "final message" {
		t.Fatalf("message = %q, want final message", newSnap.Description)
	}
	if newSnap.TreeID != oldSnap.TreeID {
		t.Fatalf("reword must not change the tree")
	}
	if newRev.ID != revID {
		t.Fatalf("reword must keep the revision id")
	}
}

func TestCommitEmptyFilesWithoutRevisionIs400(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	newBranch(t, repo, "feature/empty")
	rec := commitPost(t, mux, token, map[string]any{"branch": "feature/empty", "message": "noop", "files": []map[string]any{}})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty files without revision_id should be 400, got %d %s", rec.Code, rec.Body.String())
	}
}

func TestCommitUnsafePathRejected(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	newBranch(t, repo, "feature/unsafe")
	for _, p := range []string{"../escape.txt", "/abs.txt", "a/../../b.txt", "a/./b.txt", "a\\b.txt"} {
		rec := commitPost(t, mux, token, map[string]any{
			"branch": "feature/unsafe", "message": "bad",
			"files": []map[string]any{{"path": p, "operation": "create", "content": "x\n"}},
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("unsafe path %q should be 400, got %d %s", p, rec.Code, rec.Body.String())
		}
	}
	tr := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/tree?ref=feature/unsafe", token, nil)
	if has(tr.Body.String(), "escape.txt") {
		t.Fatalf("unsafe commit must not land: %s", tr.Body.String())
	}
}

// TestCommitRequiresWrite checks the write gate: an anonymous caller is 401 and
// a valid token belonging to a NON-owner (no write role) is 403.
func TestCommitRequiresWrite(t *testing.T) {
	s, mux, _, repo := hostingTestServer(t)
	newBranch(t, repo, "feature/auth")

	anon := commitPost(t, mux, "", map[string]any{
		"branch": "feature/auth", "message": "x",
		"files": []map[string]any{{"path": "a.txt", "operation": "create", "content": "a\n"}},
	})
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous commit should be 401, got %d %s", anon.Code, anon.Body.String())
	}

	// A second user with a write token but no role on team/app -> 403.
	out, err := s.cs.CreateUser("outsider", "outsider")
	if err != nil {
		t.Fatal(err)
	}
	tok := "outsider-token"
	if _, err := s.cs.CreateToken(tok, out.ID, "write"); err != nil {
		t.Fatal(err)
	}
	rec := commitPost(t, mux, tok, map[string]any{
		"branch": "feature/auth", "message": "x",
		"files": []map[string]any{{"path": "a.txt", "operation": "create", "content": "a\n"}},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner commit should be 403, got %d %s", rec.Code, rec.Body.String())
	}
}
