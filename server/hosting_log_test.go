package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// logDescs performs a /log request and returns the commit descriptions in the
// order the server returned them (it asserts the request succeeded).
func logDescs(t *testing.T, rec *httptest.ResponseRecorder) []string {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("log: %d %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Commits []struct {
			Description string `json:"description"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode log %q: %v", rec.Body.String(), err)
	}
	out := make([]string, 0, len(body.Commits))
	for _, c := range body.Commits {
		out = append(out, c.Description)
	}
	return out
}

func hasDesc(ds []string, want string) bool {
	for _, d := range ds {
		if d == want {
			return true
		}
	}
	return false
}

// TestLogRefScopesToReachableHistory pins that a non-empty `ref` scopes /log to
// the revisions REACHABLE from that ref (the branch's own history), while an
// empty ref keeps the whole-repository history. Before the fix the ref was
// silently ignored on the non-path branch, so `/log?ref=main` returned another
// branch's commits.
func TestLogRefScopesToReachableHistory(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	ws := revision.NewWorkspace(repo)

	// hostingTestServer leaves `main` at the single "base" commit.
	mainRef, err := ws.GetRef("main")
	if err != nil {
		t.Fatal(err)
	}
	baseRev, err := repo.GetRevision(mainRef.Target)
	if err != nil {
		t.Fatal(err)
	}

	// `feat` branches off base; `main` advances on its own (a sibling of feat).
	feat := commitFile(t, ws, baseRev.Hash, "featonly.txt", "f\n", "FEATONLY")
	if _, err := ws.SetRef("feat", store.RefBranch, feat.ID); err != nil {
		t.Fatal(err)
	}
	mainAdv := commitFile(t, ws, baseRev.Hash, "mainonly.txt", "m\n", "MAIN")
	if _, err := ws.SetRef("main", store.RefBranch, mainAdv.ID); err != nil {
		t.Fatal(err)
	}

	// ref=main => MAIN + base, never FEATONLY.
	gotMain := logDescs(t, hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/log?ref=main", token, nil))
	if !hasDesc(gotMain, "MAIN") {
		t.Fatalf("ref=main missing MAIN: %v", gotMain)
	}
	if hasDesc(gotMain, "FEATONLY") {
		t.Fatalf("ref=main leaked another branch's commit: %v", gotMain)
	}

	// ref=feat => FEATONLY + base, never MAIN.
	gotFeat := logDescs(t, hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/log?ref=feat", token, nil))
	if !hasDesc(gotFeat, "FEATONLY") {
		t.Fatalf("ref=feat missing FEATONLY: %v", gotFeat)
	}
	if hasDesc(gotFeat, "MAIN") {
		t.Fatalf("ref=feat leaked main's commit: %v", gotFeat)
	}
	if !hasDesc(gotFeat, "base") {
		t.Fatalf("ref=feat should include the common ancestor: %v", gotFeat)
	}

	// No ref => the whole repository (backward compatible).
	gotAll := logDescs(t, hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/log", token, nil))
	if !hasDesc(gotAll, "MAIN") || !hasDesc(gotAll, "FEATONLY") {
		t.Fatalf("ref-less log should hold every branch's commits: %v", gotAll)
	}

	// ref=<revision id> resolves too.
	gotByID := logDescs(t, hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/log?ref="+feat.ID, token, nil))
	if !hasDesc(gotByID, "FEATONLY") || hasDesc(gotByID, "MAIN") {
		t.Fatalf("ref=<feat revision id> wrong: %v", gotByID)
	}

	// Unknown ref => 404.
	if rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team/app/log?ref=nope", token, nil); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ref: got %d %s, want 404", rec.Code, rec.Body.String())
	}
}
