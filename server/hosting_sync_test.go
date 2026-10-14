package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// syncTestRepo builds a Server + repo `team/app` (owner "tester", token
// "alice-token") with `main` at one commit, and returns the mux, the token, and
// the repo handle. Protection is disabled so a feature branch may be created and
// synced; individual tests re-enable it where relevant.
func syncTestRepo(t *testing.T) (*Server, *http.ServeMux, string, *store.Repo) {
	t.Helper()
	s, mux, token, repo := hostingTestServer(t)
	if err := repo.SetProtectedBranches(nil); err != nil {
		t.Fatal(err)
	}
	return s, mux, token, repo
}

// commitFile is a tiny helper: commit one file on top of parentHash and return
// the new revision.
func commitFile(t *testing.T, ws *revision.Workspace, parentHash object.ID, path, content, msg string) *store.Revision {
	t.Helper()
	_, rev, err := ws.CommitFromChanges(parentHash,
		[]revision.FileChangeSpec{{Path: path, Content: []byte(content)}}, msg, store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return rev
}

// syncResp is the /branches/{branch}/sync response body.
type syncResp struct {
	OK         bool     `json:"ok"`
	Branch     string   `json:"branch"`
	Base       string   `json:"base"`
	Clean      bool     `json:"clean"`
	UpToDate   bool     `json:"up_to_date"`
	RevisionID string   `json:"revision_id"`
	Snapshot   string   `json:"snapshot"`
	Conflicts  []string `json:"conflicts"`
}

// TestBranchSyncClean integrates main into a feature branch when the two touch
// different files: the branch ends up with both, cleanly, on a NEW single-parent
// revision, and main is untouched.
func TestBranchSyncClean(t *testing.T) {
	_, mux, token, repo := syncTestRepo(t)
	ws := revision.NewWorkspace(repo)

	mainRef, _ := ws.GetRef("main")
	mainSnap, _ := repo.GetSnapshot(revHash(t, repo, mainRef.Target))
	mainTip := mainSnap.RevisionHash

	// Feature branch off main, adding feature.txt.
	feat := commitFile(t, ws, mainTip, "feature.txt", "feat\n", "feature work")
	if _, err := ws.SetRef("feat", store.RefBranch, feat.ID); err != nil {
		t.Fatal(err)
	}
	// main advances, adding base.txt (a different file -> clean merge).
	adv := commitFile(t, ws, mainTip, "base.txt", "base\n", "base work")
	if _, err := ws.SetRef("main", store.RefBranch, adv.ID); err != nil {
		t.Fatal(err)
	}
	mainBefore, _ := ws.GetRef("main")

	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/feat/sync", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body.String())
	}
	var got syncResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Clean || len(got.Conflicts) != 0 {
		t.Fatalf("expected clean sync, got %+v", got)
	}
	if got.UpToDate {
		t.Fatalf("expected a real sync, not up_to_date: %+v", got)
	}

	// The branch now contains BOTH files.
	tree := treeOfRef(t, ws, repo, "feat")
	if _, ok := tree.Entries["feature.txt"]; !ok {
		t.Fatalf("feat tree missing feature.txt: %v", tree.Entries)
	}
	if _, ok := tree.Entries["base.txt"]; !ok {
		t.Fatalf("feat tree missing base.txt (base was not integrated): %v", tree.Entries)
	}
	// The sync revision is single-parent (parent = the branch tip before sync).
	rev, err := repo.GetRevision(got.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	snap, _ := repo.GetSnapshot(rev.Hash)
	if len(snap.Parents) != 1 || snap.Parents[0] != feat.Hash {
		t.Fatalf("sync revision parents = %v, want [%s]", snap.Parents, feat.Hash)
	}
	// main is untouched.
	mainAfter, _ := ws.GetRef("main")
	if mainAfter.Target != mainBefore.Target {
		t.Fatalf("main moved during sync: %s -> %s", mainBefore.Target, mainAfter.Target)
	}
}

// TestBranchSyncConflict merges the SAME file changed on both sides: the result
// is a first-class conflict object (not text markers), and /raw renders markers.
func TestBranchSyncConflict(t *testing.T) {
	_, mux, token, repo := syncTestRepo(t)
	ws := revision.NewWorkspace(repo)

	mainRef, _ := ws.GetRef("main")
	mainSnap, _ := repo.GetSnapshot(revHash(t, repo, mainRef.Target))
	mainTip := mainSnap.RevisionHash

	// Branch edits README.md; main edits the same line.
	feat := commitFile(t, ws, mainTip, "README.md", "branch version\n", "branch edit")
	if _, err := ws.SetRef("feat", store.RefBranch, feat.ID); err != nil {
		t.Fatal(err)
	}
	adv := commitFile(t, ws, mainTip, "README.md", "main version\n", "main edit")
	if _, err := ws.SetRef("main", store.RefBranch, adv.ID); err != nil {
		t.Fatal(err)
	}

	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/feat/sync", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body.String())
	}
	var got syncResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Clean {
		t.Fatalf("expected a conflict, got clean: %+v", got)
	}
	if len(got.Conflicts) != 1 || got.Conflicts[0] != "README.md" {
		t.Fatalf("conflicts = %v, want [README.md]", got.Conflicts)
	}

	// The path in the branch tree is a first-class CONFLICT object.
	tree := treeOfRef(t, ws, repo, "feat")
	entry, ok := tree.Entries["README.md"]
	if !ok {
		t.Fatalf("README.md missing from feat tree")
	}
	if entry.Kind != object.KindConflict {
		t.Fatalf("README.md entry kind = %v, want conflict", entry.Kind)
	}

	// /raw renders the conflict as marker text.
	rrec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/raw?ref=feat&path=README.md", token, nil)
	if rrec.Code != http.StatusOK {
		t.Fatalf("raw: %d %s", rrec.Code, rrec.Body.String())
	}
	body := rrec.Body.String()
	if !strings.Contains(body, "<<<<<<<") || !strings.Contains(body, ">>>>>>>") {
		t.Fatalf("raw conflict should render markers, got %q", body)
	}
	// /blob also reads it back as text with conflict=true.
	brec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/blob?ref=feat&path=README.md", token, nil)
	if brec.Code != http.StatusOK {
		t.Fatalf("blob: %d %s", brec.Code, brec.Body.String())
	}
	var bl struct {
		Conflict bool   `json:"conflict"`
		IsText   bool   `json:"is_text"`
		Content  string `json:"content"`
	}
	_ = json.Unmarshal(brec.Body.Bytes(), &bl)
	if !bl.Conflict || !bl.IsText || !strings.Contains(bl.Content, "<<<<<<<") {
		t.Fatalf("blob conflict view = %+v", bl)
	}
}

// TestBranchSyncIdempotent: a second sync after a clean one is a no-op — the
// merged tree already equals the branch tree, so it reports up_to_date and adds
// no revision (single-parent model: the base tip is NOT an ancestor).
func TestBranchSyncIdempotent(t *testing.T) {
	_, mux, token, repo := syncTestRepo(t)
	ws := revision.NewWorkspace(repo)

	mainRef, _ := ws.GetRef("main")
	mainSnap, _ := repo.GetSnapshot(revHash(t, repo, mainRef.Target))
	mainTip := mainSnap.RevisionHash

	feat := commitFile(t, ws, mainTip, "feature.txt", "feat\n", "feature")
	if _, err := ws.SetRef("feat", store.RefBranch, feat.ID); err != nil {
		t.Fatal(err)
	}
	adv := commitFile(t, ws, mainTip, "base.txt", "base\n", "base")
	if _, err := ws.SetRef("main", store.RefBranch, adv.ID); err != nil {
		t.Fatal(err)
	}

	// First sync integrates base.txt cleanly.
	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/feat/sync", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("first sync: %d %s", rec.Code, rec.Body.String())
	}
	var first syncResp
	_ = json.Unmarshal(rec.Body.Bytes(), &first)
	if !first.Clean || first.UpToDate {
		t.Fatalf("first sync unexpected: %+v", first)
	}
	before, _ := repo.ListRevisions()

	// Second sync: base already integrated -> up_to_date, no new revision.
	rec = hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/feat/sync", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("second sync: %d %s", rec.Code, rec.Body.String())
	}
	var second syncResp
	_ = json.Unmarshal(rec.Body.Bytes(), &second)
	if !second.UpToDate {
		t.Fatalf("second sync should be up_to_date, got %+v", second)
	}
	after, _ := repo.ListRevisions()
	if len(after) != len(before) {
		t.Fatalf("idempotent sync must not add a revision: %d -> %d", len(before), len(after))
	}
}

// TestBranchSyncUpToDate: when the base tip is already an ancestor of the branch
// tip, sync reports up_to_date and creates NO new revision.
func TestBranchSyncUpToDate(t *testing.T) {
	_, mux, token, repo := syncTestRepo(t)
	ws := revision.NewWorkspace(repo)

	mainRef, _ := ws.GetRef("main")
	mainSnap, _ := repo.GetSnapshot(revHash(t, repo, mainRef.Target))
	mainTip := mainSnap.RevisionHash

	// The branch is AHEAD of main (built on the main tip).
	feat := commitFile(t, ws, mainTip, "feature.txt", "feat\n", "feature work")
	if _, err := ws.SetRef("feat", store.RefBranch, feat.ID); err != nil {
		t.Fatal(err)
	}
	before, _ := repo.ListRevisions()

	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/feat/sync", token, map[string]any{})
	if rec.Code != http.StatusOK {
		t.Fatalf("sync: %d %s", rec.Code, rec.Body.String())
	}
	var got syncResp
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if !got.Clean || !got.UpToDate {
		t.Fatalf("expected up_to_date clean, got %+v", got)
	}
	if got.RevisionID != feat.ID {
		t.Fatalf("up_to_date revision = %s, want the branch tip %s", got.RevisionID, feat.ID)
	}
	after, _ := repo.ListRevisions()
	if len(after) != len(before) {
		t.Fatalf("up_to_date must not create a revision: %d -> %d", len(before), len(after))
	}
}

// TestBranchSyncErrors covers the 400/403/404 cases.
func TestBranchSyncErrors(t *testing.T) {
	_, mux, token, repo := syncTestRepo(t)

	// base == branch -> 400.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/main/sync", token, map[string]any{"base": "main"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("base==branch = %d, want 400", rec.Code)
	}
	// unknown branch -> 404.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/nope/sync", token, map[string]any{}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown branch = %d, want 404", rec.Code)
	}
	// unknown base -> 404.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/main/sync", token, map[string]any{"base": "nope"}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown base = %d, want 404", rec.Code)
	}
	// protected TARGET (main) -> 403.
	if err := repo.SetProtectedBranches([]string{"main"}); err != nil {
		t.Fatal(err)
	}
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/main/sync", token, map[string]any{"base": "feat"}); rec.Code != http.StatusForbidden {
		t.Fatalf("protected target = %d, want 403", rec.Code)
	}
}

// TestBranchSyncAuth: a non-owner is refused (403); an anonymous caller is 401.
func TestBranchSyncAuth(t *testing.T) {
	s, mux, _, repo := syncTestRepo(t)
	ws := revision.NewWorkspace(repo)
	mainRef, _ := ws.GetRef("main")
	mainSnap, _ := repo.GetSnapshot(revHash(t, repo, mainRef.Target))
	feat := commitFile(t, ws, mainSnap.RevisionHash, "f.txt", "f\n", "feat")
	if _, err := ws.SetRef("feat", store.RefBranch, feat.ID); err != nil {
		t.Fatal(err)
	}

	// Anonymous (no token) -> 401 (write requires a token).
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/feat/sync", "", map[string]any{}); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous sync = %d, want 401", rec.Code)
	}
	// A second user with no role on team/app -> 403.
	u2, err := s.cs.CreateUser("intruder", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.cs.CreateToken("intruder-token", u2.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team/app/branches/feat/sync", "intruder-token", map[string]any{}); rec.Code != http.StatusForbidden {
		t.Fatalf("non-owner sync = %d, want 403", rec.Code)
	}
}

// ---- helpers ----

// revHash returns the current snapshot hash of a revision id.
func revHash(t *testing.T, repo *store.Repo, revID string) object.ID {
	t.Helper()
	rev, err := repo.GetRevision(revID)
	if err != nil {
		t.Fatal(err)
	}
	return rev.Hash
}

// treeOfRef returns the root tree a branch points at.
func treeOfRef(t *testing.T, ws *revision.Workspace, repo *store.Repo, branch string) *object.Tree {
	t.Helper()
	ref, err := ws.GetRef(branch)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := repo.GetRevision(ref.Target)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := repo.GetSnapshot(rev.Hash)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := ws.ReadTree(snap.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}
