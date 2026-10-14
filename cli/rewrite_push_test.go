package main

import (
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/server"
	"github.com/easylab-platform/easyvcs/store"
)

// TestRewriteIncrementalPushDeliversNewSnapshot is the regression test for the
// "rewrite then incremental push silently drops the new snapshot" defect.
//
// A revision id is stable across rewrites (rebase / amend / squash): the id
// stays the same while its snapshot hash changes. An incremental push that
// skipped revisions by id alone would therefore treat a rewritten revision as
// "already present" and never send its new snapshot, leaving the server on the
// old content while the client believes the push succeeded.
//
// This test drives the real client push path (doPushHTTP) against an httptest
// server built from server.New, rewrites a revision locally (same id, new
// snapshot), pushes, and asserts the server ends up with the rewritten
// snapshot and content.
func TestRewriteIncrementalPushDeliversNewSnapshot(t *testing.T) {
	// ---- remote (server) store: user + token + team/app with one revision ----
	remoteHome := t.TempDir()
	t.Setenv("EASYVCS_HOME", remoteHome)
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.SetWAL(); err != nil {
		t.Fatal(err)
	}
	u, err := cs.CreateUser("tester", "test")
	if err != nil {
		t.Fatal(err)
	}
	const token = "secret"
	if _, err := cs.CreateToken(token, u.ID, "write"); err != nil {
		t.Fatal(err)
	}
	if err := cs.EnsureNamespace(u.ID, "team"); err != nil {
		t.Fatal(err)
	}
	remoteRepo, err := cs.Create(store.NSRef{Owner: u.ID, Name: "team-app"})
	if err != nil {
		t.Fatal(err)
	}
	wsR := revision.NewWorkspace(remoteRepo)
	_, r1, err := wsR.CommitFromChanges(object.ID{},
		[]revision.FileChangeSpec{{Path: "README.md", Content: []byte("v1\n")}},
		"c1", store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wsR.SetRef("main", store.RefBranch, r1.ID); err != nil {
		t.Fatal(err)
	}

	ts := httptest.NewServer(server.New(cs, nil).Handler())
	defer ts.Close()

	// ---- local store: a separate home, clone the remote's main ----
	localHome := t.TempDir()
	t.Setenv("EASYVCS_HOME", localHome)
	lcs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	localRepo, err := lcs.Create(store.NSRef{Name: "team-app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := localRepo.PutRemote("origin", ts.URL, token); err != nil {
		t.Fatal(err)
	}
	if err := doPull(localRepo, []string{"origin", "main"}); err != nil {
		t.Fatalf("initial pull: %v", err)
	}

	// The pulled revision must be r1 at the original snapshot.
	lrev, err := localRepo.GetRevision(r1.ID)
	if err != nil {
		t.Fatalf("pulled revision missing: %v", err)
	}
	origHash := lrev.Hash

	// ---- rewrite the SAME revision id to a different snapshot ----
	wsL := revision.NewWorkspace(localRepo)
	_, rw, err := wsL.CommitFromChanges(origHash,
		[]revision.FileChangeSpec{{Path: "README.md", Content: []byte("v2-rewritten\n")}},
		"rewrite", store.Author{Name: "t"}, r1.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rw.ID != r1.ID {
		t.Fatalf("rewrite changed the revision id: got %s want %s", rw.ID, r1.ID)
	}
	if rw.Hash == origHash {
		t.Fatalf("rewrite did not change the snapshot hash")
	}
	if _, err := wsL.SetRef("main", store.RefBranch, r1.ID); err != nil {
		t.Fatal(err)
	}

	// ---- incremental push of the rewrite ----
	rem, err := localRepo.GetRemote("origin")
	if err != nil {
		t.Fatal(err)
	}
	if err := doPushHTTP(localRepo, rem); err != nil {
		t.Fatalf("push: %v", err)
	}

	// ---- the server's r1 must now point at the rewritten snapshot ----
	srev, err := remoteRepo.GetRevision(r1.ID)
	if err != nil {
		t.Fatalf("server revision missing: %v", err)
	}
	if srev.Hash != rw.Hash {
		t.Fatalf("server revision %s hash = %s, want rewritten %s (new snapshot was not delivered)",
			r1.ID, srev.Hash, rw.Hash)
	}
	ssnap, err := remoteRepo.GetSnapshot(srev.Hash)
	if err != nil {
		t.Fatalf("server snapshot missing after push: %v", err)
	}
	tree, err := wsR.ReadTree(ssnap.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := wsR.FindEntry(tree, "README.md")
	if err != nil {
		t.Fatalf("README.md missing in server tree: %v", err)
	}
	data, err := wsR.ReadBlob(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v2-rewritten\n" {
		t.Fatalf("server content = %q, want %q", string(data), "v2-rewritten\n")
	}
	// The server's main branch must also resolve to r1.
	mainRef, err := remoteRepo.GetRef("main")
	if err != nil || mainRef == nil {
		t.Fatalf("server main ref missing: %v", err)
	}
	if mainRef.Target != r1.ID {
		t.Fatalf("server main -> %s, want %s", mainRef.Target, r1.ID)
	}
}

// TestServerHaveSetSkipsOnlyMatchingHashes unit-tests the client-side delta
// decision: a revision whose advertised (server) snapshot hash differs from the
// local one must be EXCLUDED from the have set so it is re-sent.
func TestServerHaveSetSkipsOnlyMatchingHashes(t *testing.T) {
	setHome(t)
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := cs.Create(store.NSRef{Name: "n-r"})
	if err != nil {
		t.Fatal(err)
	}
	ws := revision.NewWorkspace(repo)
	_, r1, err := ws.CommitFromChanges(object.ID{}, []revision.FileChangeSpec{{Path: "a", Content: []byte("1")}}, "c1", store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, r2, err := ws.CommitFromChanges(r1.Hash, []revision.FileChangeSpec{{Path: "b", Content: []byte("2")}}, "c2", store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}

	adv := advertiseResp{
		Changes: []string{r1.ID, r2.ID},
		ChangeHashes: map[string]string{
			r1.ID: r1.Hash.String(), // matches local -> skippable
			r2.ID: "deadbeef",       // differs from local -> must be re-sent
		},
	}
	got := serverHaveSet(repo, adv)
	if len(got) != 1 || got[0] != r1.ID {
		t.Fatalf("serverHaveSet = %v, want [%s] (only the matching hash is skippable)", got, r1.ID)
	}

	// No advertised hashes (older server) => id-only fallback.
	got2 := serverHaveSet(repo, advertiseResp{Changes: []string{r1.ID, r2.ID}})
	if len(got2) != 2 {
		t.Fatalf("fallback serverHaveSet = %v, want both ids", got2)
	}
}
