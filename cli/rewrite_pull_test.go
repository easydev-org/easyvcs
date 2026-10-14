package main

import (
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/server"
	"github.com/easylab-platform/easyvcs/store"
)

// TestRewriteIncrementalPullDeliversNewSnapshot is the pull-direction mirror of
// TestRewriteIncrementalPushDeliversNewSnapshot.
//
// A revision id is stable across rewrites (rebase / amend / squash): the id
// stays the same while its snapshot hash changes. An incremental PULL that
// declared "have" by id alone would tell the server it already holds a revision
// the server has since rewritten, so the server would skip it in the fetch
// bundle and the client would keep the STALE snapshot — then rebase on top of
// it, silently diverging.
//
// This test drives the real client pull path (doPull) against an httptest
// server built from server.New: the client pulls once, the SERVER rewrites that
// revision (same id, new snapshot), the client pulls again, and the client must
// end up with the rewritten snapshot and content.
func TestRewriteIncrementalPullDeliversNewSnapshot(t *testing.T) {
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
	remoteRepo, err := cs.Create(store.NSRef{Owner: u.ID, Name: "team" + "/" + "app"})
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

	// ---- client store: a separate home; first pull gets r1 at the original ----
	localHome := t.TempDir()
	t.Setenv("EASYVCS_HOME", localHome)
	lcs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	localRepo, err := lcs.Create(store.NSRef{Name: "team" + "/" + "app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := localRepo.PutRemote("origin", ts.URL, token); err != nil {
		t.Fatal(err)
	}
	if err := doPull(localRepo, []string{"origin", "main"}); err != nil {
		t.Fatalf("initial pull: %v", err)
	}
	lrev, err := localRepo.GetRevision(r1.ID)
	if err != nil {
		t.Fatalf("pulled revision missing: %v", err)
	}
	oldHash := lrev.Hash
	if oldHash != r1.Hash {
		t.Fatalf("first pull snapshot = %s, want %s", oldHash, r1.Hash)
	}

	// ---- the SERVER rewrites the SAME revision id (amend: id stable, snapshot new) ----
	// wsR is bound to the remote store, so this writes server-side regardless of
	// the client's EASYVCS_HOME.
	_, rw, err := wsR.CommitFromChanges(oldHash,
		[]revision.FileChangeSpec{{Path: "README.md", Content: []byte("v2-rewritten\n")}},
		"rewrite", store.Author{Name: "t"}, r1.ID)
	if err != nil {
		t.Fatalf("server rewrite: %v", err)
	}
	if rw.ID != r1.ID {
		t.Fatalf("rewrite changed the revision id: got %s want %s", rw.ID, r1.ID)
	}
	if rw.Hash == oldHash {
		t.Fatalf("rewrite did not change the snapshot hash")
	}
	newHash := rw.Hash

	// ---- incremental pull: the client must receive the REWRITTEN snapshot ----
	if err := doPull(localRepo, []string{"origin", "main"}); err != nil {
		t.Fatalf("second pull: %v", err)
	}
	if _, err := localRepo.GetSnapshot(newHash); err != nil {
		t.Fatalf("client did not receive the rewritten snapshot %s (id-only have skipped it): %v", newHash, err)
	}
	// And r1 must now resolve to the rewritten content on the client.
	lrev2, err := localRepo.GetRevision(r1.ID)
	if err != nil {
		t.Fatalf("client revision missing after pull: %v", err)
	}
	lsnap, err := localRepo.GetSnapshot(lrev2.Hash)
	if err != nil {
		t.Fatal(err)
	}
	wsL := revision.NewWorkspace(localRepo)
	tree, err := wsL.ReadTree(lsnap.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := wsL.FindEntry(tree, "README.md")
	if err != nil {
		t.Fatalf("README.md missing in client tree: %v", err)
	}
	data, err := wsL.ReadBlob(entry.ID)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "v2-rewritten\n" {
		t.Fatalf("client content = %q, want the rewritten %q", string(data), "v2-rewritten\n")
	}
}

// TestClientHaveSetSkipsOnlyMatchingHashes unit-tests the pull-side delta
// decision (the mirror of TestServerHaveSetSkipsOnlyMatchingHashes): the client
// may only claim a revision as "have" when the server advertises it at the SAME
// snapshot hash; a rewritten revision (same id, different hash) is excluded so
// it is re-fetched, and an older peer (no hashes) falls back to id-only.
func TestClientHaveSetSkipsOnlyMatchingHashes(t *testing.T) {
	setHome(t)
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	repo, err := cs.Create(store.NSRef{Name: "n" + "/" + "r"})
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
			r1.ID: r1.Hash.String(), // server matches local -> safe to claim
			r2.ID: "deadbeef",       // server rewrote it -> must NOT claim (re-fetch)
		},
	}
	got := clientHaveSet(repo, adv)
	if len(got) != 1 || got[0] != r1.ID {
		t.Fatalf("clientHaveSet = %v, want [%s] (only the matching hash is claimable)", got, r1.ID)
	}

	// No advertised hashes (older server) => id-only fallback (all local ids).
	got2 := clientHaveSet(repo, advertiseResp{Changes: []string{r1.ID, r2.ID}})
	if len(got2) != 2 {
		t.Fatalf("fallback clientHaveSet = %v, want both local ids", got2)
	}
}
