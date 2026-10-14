package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
)

// snapshotPost sends a /snapshot body to team/app.
func snapshotPost(t *testing.T, mux *http.ServeMux, token string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	return hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/snapshot", token, body)
}

// refNames returns the repo's ref names as a set.
func refNames(t *testing.T, mux *http.ServeMux, token string) map[string]bool {
	t.Helper()
	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/refs", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("refs: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Refs []struct {
			Name string `json:"name"`
		} `json:"refs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refs %q: %v", rec.Body.String(), err)
	}
	out := map[string]bool{}
	for _, r := range body.Refs {
		out[r.Name] = true
	}
	return out
}

// TestSnapshotBuildsRevisionAndMovesNoRef is the core contract: /snapshot makes
// a revision + snapshot but leaves EVERY ref untouched.
func TestSnapshotBuildsRevisionAndMovesNoRef(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	before := refNames(t, mux, token)

	rec := snapshotPost(t, mux, token, map[string]any{
		"message": "propose from sandbox",
		"files": []map[string]any{
			{"path": "a.txt", "operation": "create", "content": "hello\n"},
			{"path": "sub/b.txt", "operation": "create", "content": "world\n"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}
	got := decode(t, rec)
	if got["revision_id"] == nil || got["revision_id"] == "" {
		t.Fatalf("no revision_id: %+v", got)
	}
	if got["snapshot"] == nil || got["snapshot"] == "" {
		t.Fatalf("no snapshot: %+v", got)
	}
	// Refs are unchanged (nothing moved).
	after := refNames(t, mux, token)
	if len(after) != len(before) {
		t.Fatalf("ref set changed: before=%v after=%v", before, after)
	}
	for name := range before {
		if !after[name] {
			t.Fatalf("ref %q disappeared", name)
		}
	}
}

// TestSnapshotProposeRoundTrip: a snapshot from /snapshot can be handed straight
// to POST /rr and merged into main — the "sandbox -> snapshot -> RR -> merge"
// flow with NO branch.
func TestSnapshotProposeRoundTrip(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)

	rec := snapshotPost(t, mux, token, map[string]any{
		"message": "propose",
		"files":   []map[string]any{{"path": "new.txt", "operation": "create", "content": "from snapshot\n"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("snapshot: %d %s", rec.Code, rec.Body.String())
	}
	snap := decode(t, rec)["snapshot"].(string)

	rr := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr", token, map[string]any{
		"source_snapshot": snap, "target_branch": "main", "title": "from snapshot",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("rr create: %d %s", rr.Code, rr.Body.String())
	}
	iid := int64(decode(t, rr)["iid"].(float64))

	mr := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr/"+itoa(iid)+"/merge", token, nil)
	if mr.Code != http.StatusOK {
		t.Fatalf("rr merge: %d %s", mr.Code, mr.Body.String())
	}
	tr := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/tree?ref=main", token, nil)
	if !has(tr.Body.String(), "new.txt") {
		t.Fatalf("merged main must contain new.txt: %s", tr.Body.String())
	}
}

// TestSnapshotBinaryAndDelete covers content_bytes round-trip and delete, applied
// on top of a base snapshot (parents).
func TestSnapshotBinaryAndDelete(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)

	raw := []byte{0x00, 0x01, 0xff, 0xfe, 0x7f, 0x80}
	rec := snapshotPost(t, mux, token, map[string]any{
		"message": "base",
		"files": []map[string]any{
			{"path": "keep.txt", "operation": "create", "content": "k\n"},
			{"path": "blob.bin", "operation": "create", "content_bytes": base64.StdEncoding.EncodeToString(raw)},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("base snapshot: %d %s", rec.Code, rec.Body.String())
	}
	base := decode(t, rec)["snapshot"].(string)

	// Child snapshot: delete keep.txt, on top of the base snapshot.
	rec = snapshotPost(t, mux, token, map[string]any{
		"message": "child", "parents": []string{base},
		"files": []map[string]any{{"path": "keep.txt", "operation": "delete"}},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("child snapshot: %d %s", rec.Code, rec.Body.String())
	}
	childSnap := decode(t, rec)["snapshot"].(string)

	// The child snapshot's tree has no keep.txt but still has blob.bin.
	ws := revision.NewWorkspace(repo)
	id, err := object.HexToID(childSnap)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.GetSnapshot(id)
	if err != nil {
		t.Fatal(err)
	}
	tree := ws.MustTree(snap.TreeID)
	if _, ok := tree.Entries["keep.txt"]; ok {
		t.Fatalf("keep.txt must be deleted")
	}
	if _, ok := tree.Entries["blob.bin"]; !ok {
		t.Fatalf("blob.bin must survive")
	}
}

// TestSnapshotUnsafePathRejected mirrors /commit's path safety.
func TestSnapshotUnsafePathRejected(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	for _, p := range []string{"../escape.txt", "/abs.txt", "a/../../b.txt", "a/./b.txt", "a\\b.txt"} {
		rec := snapshotPost(t, mux, token, map[string]any{
			"message": "bad",
			"files":   []map[string]any{{"path": p, "operation": "create", "content": "x\n"}},
		})
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("unsafe path %q should be 400, got %d %s", p, rec.Code, rec.Body.String())
		}
	}
}

// TestSnapshotBadParentAndEmptyFiles covers the 400 paths.
func TestSnapshotBadParentAndEmptyFiles(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)

	if rec := snapshotPost(t, mux, token, map[string]any{"message": "x", "files": []map[string]any{}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty files should be 400, got %d", rec.Code)
	}
	if rec := snapshotPost(t, mux, token, map[string]any{
		"message": "x", "parents": []string{"00000000000000000000000000000000"},
		"files": []map[string]any{{"path": "a.txt", "operation": "create", "content": "a\n"}},
	}); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown parent should be 400, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestSnapshotAuthProposeLevel: a developer (repo member) may propose, an
// outsider/reader may not, and an owner may (>= developer). Unlike /commit this
// is NOT owner-only.
func TestSnapshotAuthProposeLevel(t *testing.T) {
	s, mux, ownerTok, repo := hostingTestServer(t)

	body := map[string]any{
		"message": "x",
		"files":   []map[string]any{{"path": "a.txt", "operation": "create", "content": "a\n"}},
	}

	// Anonymous -> 401.
	if rec := snapshotPost(t, mux, "", body); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous should be 401, got %d %s", rec.Code, rec.Body.String())
	}

	// A developer member -> 200 (propose level).
	dev, err := s.cs.CreateUser("dev", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cs.SetRepoMember(repo.NamespaceID(), dev.ID, "developer", nil); err != nil {
		t.Fatal(err)
	}
	devTok := "dev-token"
	if _, err := s.cs.CreateToken(devTok, dev.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if rec := snapshotPost(t, mux, devTok, body); rec.Code != http.StatusOK {
		t.Fatalf("developer should be 200, got %d %s", rec.Code, rec.Body.String())
	}

	// A maintainer member -> 200 (>= developer).
	mnt, err := s.cs.CreateUser("mnt", "mnt")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.cs.SetRepoMember(repo.NamespaceID(), mnt.ID, "maintainer", nil); err != nil {
		t.Fatal(err)
	}
	mntTok := "mnt-token"
	if _, err := s.cs.CreateToken(mntTok, mnt.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if rec := snapshotPost(t, mux, mntTok, body); rec.Code != http.StatusOK {
		t.Fatalf("maintainer should be 200, got %d %s", rec.Code, rec.Body.String())
	}

	// The owner -> 200.
	if rec := snapshotPost(t, mux, ownerTok, body); rec.Code != http.StatusOK {
		t.Fatalf("owner should be 200, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestSnapshotAuthNoAccessOnPrivate: on a PRIVATE repo a user with no grant is
// refused (403) — the public-repo developer fallback must not apply.
func TestSnapshotAuthNoAccessOnPrivate(t *testing.T) {
	s, mux, _, repo := hostingTestServer(t)
	meta, _ := repo.RepoMeta()
	meta.Visibility = "private"
	if err := repo.UpdateRepoMeta(meta); err != nil {
		t.Fatal(err)
	}
	out, err := s.cs.CreateUser("outsider", "outsider")
	if err != nil {
		t.Fatal(err)
	}
	tok := "outsider-token"
	if _, err := s.cs.CreateToken(tok, out.ID, "write"); err != nil {
		t.Fatal(err)
	}
	rec := snapshotPost(t, mux, tok, map[string]any{
		"message": "x",
		"files":   []map[string]any{{"path": "a.txt", "operation": "create", "content": "a\n"}},
	})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-member on private repo should be 403, got %d %s", rec.Code, rec.Body.String())
	}
}
