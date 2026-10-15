package gitserver

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

func mc(t *testing.T, ws *revision.Workspace, parent object.ID, files map[string]string, msg string) *store.Revision {
	t.Helper()
	var cs []revision.FileChangeSpec
	for p, c := range files {
		cs = append(cs, revision.FileChangeSpec{Path: p, Content: []byte(c)})
	}
	_, r, err := ws.CommitFromChanges(parent, cs, msg, store.Author{Name: "t"}, "")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// TestConflictCloneServesMarkers pins how a CONFLICTED tree is served to git:
// the conflicted file is a regular blob whose content is the conflict MARKER
// text (same as /raw), both at the root and — crucially — when a SUBPATH
// containing the conflict is cloned as its own repository. So `git clone`
// succeeds and the user sees git-style conflict markers in the file.
func TestConflictCloneServesMarkers(t *testing.T) {
	t.Setenv("EASYVCS_HOME", t.TempDir())
	cs, _ := store.OpenDefault()
	defer cs.Close()
	repo, _ := cs.Create(store.NSRef{Name: "cf"})
	ws := revision.NewWorkspace(repo)

	base := mc(t, ws, object.ID{}, map[string]string{"pkg/f.txt": "base\n", "other.txt": "x\n"}, "base")
	_, _ = ws.SetRef("main", store.RefBranch, base.ID)
	s1 := mc(t, ws, base.Hash, map[string]string{"pkg/f.txt": "ours\n", "other.txt": "x\n"}, "s1")
	s2 := mc(t, ws, base.Hash, map[string]string{"pkg/f.txt": "theirs\n", "other.txt": "x\n"}, "s2")

	bs, _ := ws.GetSnapshot(base.Hash)
	s1s, _ := ws.GetSnapshot(s1.Hash)
	s2s, _ := ws.GetSnapshot(s2.Hash)
	merged, atoms, err := ws.Merge(bs.TreeID, s1s.TreeID, s2s.TreeID)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("merge atoms=%d", len(atoms))

	// Land the conflicted tree as a commit on main.
	_, mc2, err := ws.Commit(revision.CommitParams{Parents: []object.ID{base.Hash}, TreeID: merged, Description: "conflicted"})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = ws.SetRef("main", store.RefBranch, mc2.ID)
	t.Logf("main -> conflicted commit %s", mc2.ID)

	clone := func(sub string, dst string) {
		sto, err := Build(ws, repo, sub)
		if err != nil {
			t.Fatalf("Build(%q): %v", sub, err)
		}
		srv := New(sto)
		mux := http.NewServeMux()
		mux.HandleFunc("/r.git/info/refs", srv.InfoRefs)
		mux.HandleFunc("/r.git/git-upload-pack", srv.UploadPack)
		ts := httptest.NewServer(mux)
		defer ts.Close()
		out, err := exec.Command("git", "clone", ts.URL+"/r.git", dst).CombinedOutput()
		t.Logf("clone sub=%q err=%v", sub, err)
		if err != nil {
			t.Logf("  out=%s", out)
		}
	}

	root := filepath.Join(t.TempDir(), "root")
	clone("", root)
	if d, e := os.ReadFile(filepath.Join(root, "pkg", "f.txt")); e == nil {
		t.Logf("ROOT  pkg/f.txt = %q", string(d))
	} else {
		t.Logf("ROOT  pkg/f.txt MISSING: %v", e)
	}

	// Root clone: the conflicted file is served as marker text.
	rd, err := os.ReadFile(filepath.Join(root, "pkg", "f.txt"))
	if err != nil {
		t.Fatalf("root clone missing pkg/f.txt: %v", err)
	}
	if !strings.Contains(string(rd), "<<<<<<< conflict") || !strings.Contains(string(rd), ">>>>>>> conflict ends") {
		t.Fatalf("root conflicted file is not marker text: %q", string(rd))
	}

	// SUBPATH clone: the same conflict, projected into the sub-repo as f.txt.
	sub := filepath.Join(t.TempDir(), "sub")
	clone("pkg", sub)
	sd, err := os.ReadFile(filepath.Join(sub, "f.txt"))
	if err != nil {
		t.Fatalf("subpath clone missing f.txt: %v", err)
	}
	if !strings.Contains(string(sd), "<<<<<<< conflict") || !strings.Contains(string(sd), ">>>>>>> conflict ends") {
		t.Fatalf("subpath conflicted file is not marker text: %q", string(sd))
	}
	if string(sd) != string(rd) {
		t.Fatalf("subpath marker text differs from root: %q vs %q", string(sd), string(rd))
	}
}
