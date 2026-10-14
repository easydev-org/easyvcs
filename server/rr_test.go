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

// TestRRCreateDiffMergeE2E drives the RR endpoints: create a request from a
// bare (branch-less) snapshot, read its pinned diff, then merge it.
func TestRRCreateDiffMergeE2E(t *testing.T) {
	s := newTestServer(t, "secret")
	u, err := s.cs.GetUserByUsername("tester")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.cs.Create(store.NSRef{Owner: u.ID, Name: "team/app"})
	if err != nil {
		t.Fatal(err)
	}
	makePublic(t, repo)
	ws := revision.NewWorkspace(repo)

	treeOf := func(files map[string]string) object.ID {
		tr := object.NewTree()
		for name, content := range files {
			blob, _ := ws.WriteBlob([]byte(content))
			tr.Entries[name] = object.Entry{Name: name, Kind: object.KindBlob, ID: blob}
		}
		id, _ := ws.WriteTree(tr)
		return id
	}
	base, _, err := ws.Commit(revision.CommitParams{TreeID: treeOf(map[string]string{"README.md": "v1\n"}), Description: "base", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	src, _, err := ws.Commit(revision.CommitParams{
		Parents: []object.ID{base.RevisionHash}, TreeID: treeOf(map[string]string{"README.md": "v1\nrr\n"}),
		Description: "rr", Author: store.Author{Name: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := s.Router()
	post := func(path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer secret")
		mux.ServeHTTP(rec, r)
		return rec
	}

	// Create.
	rec := post("/api/v1/namespaces/team/app/rr", map[string]any{
		"source_snapshot": src.RevisionHash.String(), "target_branch": "main", "title": "add rr",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create rr: %d %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &created)
	if created["state"] != "open" {
		t.Fatalf("expected open state, got %v", created["state"])
	}
	if created["source_snapshot"] != src.RevisionHash.String() {
		t.Fatalf("pinned source = %v, want %s", created["source_snapshot"], src.RevisionHash)
	}

	// Diff (read).
	rec = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/team/app/rr/1/diff", nil)
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("rr diff: %d %s", rec.Code, rec.Body.String())
	}
	var diff map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &diff)
	files, _ := diff["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("expected 1 changed file, got %v", files)
	}

	// Merge.
	rec = post("/api/v1/namespaces/team/app/rr/1/merge", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("merge rr: %d %s", rec.Code, rec.Body.String())
	}
	mainRef, err := repo.GetRef("main")
	if err != nil {
		t.Fatal(err)
	}
	mergedRev, err := repo.GetRevision(mainRef.Target)
	if err != nil {
		t.Fatal(err)
	}
	mergedSnap, _ := repo.GetSnapshot(mergedRev.Hash)
	entry, err := ws.FindEntry(ws.MustTree(mergedSnap.TreeID), "README.md")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := ws.ReadBlob(entry.ID)
	if string(got) != "v1\nrr\n" {
		t.Fatalf("merged content = %q", string(got))
	}
}

// TestRRRequiresAuth verifies an anonymous create is refused.
func TestRRRequiresAuth(t *testing.T) {
	s := newTestServer(t, "secret")
	seedRepo(t, s)
	mux := s.Router()
	rec := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/api/v1/namespaces/team/app/rr", bytes.NewReader([]byte(`{"source_snapshot":"x","title":"t"}`)))
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for anonymous rr create, got %d", rec.Code)
	}
}

// TestRREmptyRepoDiffMerge is the HTTP-level regression for opening an RR
// against an EMPTY repository (no target branch yet): the RR's base_snapshot is
// all-zero, so `GET .../rr/{iid}/diff` and `POST .../rr/{iid}/merge` must both
// answer 200 (previously they 500'd with "snapshot 000…0 not found").
func TestRREmptyRepoDiffMerge(t *testing.T) {
	s := newTestServer(t, "secret")
	u, err := s.cs.GetUserByUsername("tester")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.cs.Create(store.NSRef{Owner: u.ID, Name: "team/app"})
	if err != nil {
		t.Fatal(err)
	}
	makePublic(t, repo)
	ws := revision.NewWorkspace(repo)
	// A single source snapshot; the repo has NO main / no other revisions.
	blob, _ := ws.WriteBlob([]byte("hello\n"))
	tr := object.NewTree()
	tr.Entries["README.md"] = object.Entry{Name: "README.md", Kind: object.KindBlob, ID: blob}
	tid, _ := ws.WriteTree(tr)
	src, _, err := ws.Commit(revision.CommitParams{TreeID: tid, Description: "initial", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}

	mux := s.Router()
	post := func(path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer secret")
		mux.ServeHTTP(rec, r)
		return rec
	}

	rec := post("/api/v1/namespaces/team/app/rr", map[string]any{
		"source_snapshot": src.RevisionHash.String(), "target_branch": "main", "title": "empty repo rr",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create rr on empty repo: %d %s", rec.Code, rec.Body.String())
	}

	// Diff must be 200 (not 500).
	rec = httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/team/app/rr/1/diff", nil)
	r.Header.Set("Authorization", "Bearer secret")
	mux.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("diff on empty repo rr: %d %s", rec.Code, rec.Body.String())
	}

	// Merge must be 200 (not 500), and create main.
	rec = post("/api/v1/namespaces/team/app/rr/1/merge", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("merge on empty repo rr: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := repo.GetRef("main"); err != nil {
		t.Fatalf("main should exist after merging an empty-repo RR: %v", err)
	}
}

// TestRRReviewCommentReadBack is the regression for the missing READ path: a
// review and a comment posted to an RR must come back from GET /rr/{iid}. Before
// this, reviews/comments were written into the legacy mr_* tables and never
// surfaced anywhere (write-only).
func TestRRReviewCommentReadBack(t *testing.T) {
	s := newTestServer(t, "secret")
	u, err := s.cs.GetUserByUsername("tester")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.cs.Create(store.NSRef{Owner: u.ID, Name: "team/app"})
	if err != nil {
		t.Fatal(err)
	}
	makePublic(t, repo)
	ws := revision.NewWorkspace(repo)
	blob, _ := ws.WriteBlob([]byte("hi\n"))
	tr := object.NewTree()
	tr.Entries["README.md"] = object.Entry{Name: "README.md", Kind: object.KindBlob, ID: blob}
	tid, _ := ws.WriteTree(tr)
	src, _, err := ws.Commit(revision.CommitParams{TreeID: tid, Description: "src", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}

	mux := s.Router()
	post := func(path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer secret")
		mux.ServeHTTP(rec, r)
		return rec
	}

	rec := post("/api/v1/namespaces/team/app/rr", map[string]any{
		"source_snapshot": src.RevisionHash.String(), "target_branch": "main", "title": "rr with discussion",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create rr: %d %s", rec.Code, rec.Body.String())
	}

	// A review and a comment.
	if rec := post("/api/v1/namespaces/team/app/rr/1/review", map[string]any{"state": "approved", "body": "lgtm"}); rec.Code != http.StatusOK {
		t.Fatalf("review: %d %s", rec.Code, rec.Body.String())
	}
	if rec := post("/api/v1/namespaces/team/app/rr/1/comment", map[string]any{"body": "nit here", "path": "README.md"}); rec.Code != http.StatusOK {
		t.Fatalf("comment: %d %s", rec.Code, rec.Body.String())
	}

	// GET /rr/{iid} must return both.
	grec := httptest.NewRecorder()
	greq := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/team/app/rr/1", nil)
	greq.Header.Set("Authorization", "Bearer secret")
	mux.ServeHTTP(grec, greq)
	if grec.Code != http.StatusOK {
		t.Fatalf("get rr: %d %s", grec.Code, grec.Body.String())
	}
	var got struct {
		Reviews []struct {
			State string `json:"state"`
			Body  string `json:"body"`
		} `json:"reviews"`
		Comments []struct {
			Body string `json:"body"`
			Path string `json:"path"`
		} `json:"comments"`
	}
	if err := json.Unmarshal(grec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get rr: %v", err)
	}
	if len(got.Reviews) != 1 || got.Reviews[0].State != "approved" || got.Reviews[0].Body != "lgtm" {
		t.Fatalf("reviews not read back: %+v", got.Reviews)
	}
	if len(got.Comments) != 1 || got.Comments[0].Body != "nit here" || got.Comments[0].Path != "README.md" {
		t.Fatalf("comments not read back: %+v", got.Comments)
	}
}

// TestRRCloseE2E: a close transitions an OPEN RR to `closed` (idempotent), the
// closed RR stays readable, and a closed RR can no longer be merged.
func TestRRCloseE2E(t *testing.T) {
	s := newTestServer(t, "secret")
	u, err := s.cs.GetUserByUsername("tester")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := s.cs.Create(store.NSRef{Owner: u.ID, Name: "team" + "/" + "closer"})
	if err != nil {
		t.Fatal(err)
	}
	makePublic(t, repo)
	ws := revision.NewWorkspace(repo)

	tree := func(content string) object.ID {
		tr := object.NewTree()
		blob, _ := ws.WriteBlob([]byte(content))
		tr.Entries["README.md"] = object.Entry{Name: "README.md", Kind: object.KindBlob, ID: blob}
		id, _ := ws.WriteTree(tr)
		return id
	}
	base, _, err := ws.Commit(revision.CommitParams{TreeID: tree("v1\n"), Description: "base", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("main", store.RefBranch, base.RevisionID); err != nil {
		t.Fatal(err)
	}
	src, _, err := ws.Commit(revision.CommitParams{
		Parents: []object.ID{base.RevisionHash}, TreeID: tree("v1\nrr\n"),
		Description: "rr", Author: store.Author{Name: "t"},
	})
	if err != nil {
		t.Fatal(err)
	}

	mux := s.Router()
	post := func(path string, body any) *httptest.ResponseRecorder {
		b, _ := json.Marshal(body)
		rec := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer secret")
		mux.ServeHTTP(rec, r)
		return rec
	}
	rec := post("/api/v1/namespaces/team/closer/rr", map[string]any{
		"source_snapshot": src.RevisionHash.String(), "target_branch": "main", "title": "close me",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("create rr: %d %s", rec.Code, rec.Body.String())
	}

	// Close it.
	rec = post("/api/v1/namespaces/team/closer/rr/1/close", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("close rr: %d %s", rec.Code, rec.Body.String())
	}
	var closed struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &closed); err != nil || closed.State != "closed" {
		t.Fatalf("close did not set state=closed: %s (err %v)", rec.Body.String(), err)
	}

	// Idempotent: closing again is still 200.
	rec = post("/api/v1/namespaces/team/closer/rr/1/close", map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("re-close: %d %s", rec.Code, rec.Body.String())
	}

	// A closed RR stays readable.
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/namespaces/team/closer/rr/1", nil)
	req.Header.Set("Authorization", "Bearer secret")
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get closed rr: %d", rec.Code)
	}

	// A closed RR can no longer be merged.
	rec = post("/api/v1/namespaces/team/closer/rr/1/merge", map[string]any{})
	if rec.Code == http.StatusOK {
		t.Fatalf("closed rr must not merge, got %d %s", rec.Code, rec.Body.String())
	}
}
