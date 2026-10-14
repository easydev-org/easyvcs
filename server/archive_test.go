package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/revision"
	"github.com/easylab-platform/easyvcs/store"
)

// archiveRepo builds a server + repo whose main tree has a root file, a nested
// directory, and a binary file (with a NUL byte) so the archive test can assert
// structure + exact bytes.
func archiveRepo(t *testing.T) (*Server, *http.ServeMux, string, *store.Repo) {
	t.Helper()
	s, mux, token, repo := hostingTestServer(t)
	ws := revision.NewWorkspace(repo)

	root := object.NewTree()
	root.Entries["README.md"] = object.Entry{
		Name: "README.md", Kind: object.KindBlob, ID: mustBlob(t, ws, []byte("# app\n")),
	}
	sub := object.NewTree()
	sub.Entries["main.go"] = object.Entry{
		Name: "main.go", Kind: object.KindBlob, ID: mustBlob(t, ws, []byte("package main\n")),
	}
	root.Entries["src"] = object.Entry{
		Name: "src", Kind: object.KindTree, ID: mustTree(t, ws, sub),
	}
	bin := []byte{0x00, 0x01, 0x02, 0xff, 0x00, 0x42}
	root.Entries["logo.bin"] = object.Entry{
		Name: "logo.bin", Kind: object.KindBlob, ID: mustBlob(t, ws, bin),
	}
	tid, err := ws.WriteTree(root)
	if err != nil {
		t.Fatal(err)
	}
	_, rev, err := ws.Commit(revision.CommitParams{TreeID: tid, Description: "tree", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("main", store.RefBranch, rev.ID); err != nil {
		t.Fatal(err)
	}
	return s, mux, token, repo
}

func mustBlob(t *testing.T, ws *revision.Workspace, data []byte) object.ID {
	t.Helper()
	id, err := ws.WriteBlob(data)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustTree(t *testing.T, ws *revision.Workspace, tree *object.Tree) object.ID {
	t.Helper()
	id, err := ws.WriteTree(tree)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// untarGz reads a gzipped tar stream into a path->bytes map plus a set of dir
// paths (directories are recorded with a trailing slash in tar names).
func untarGz(t *testing.T, data []byte) (files map[string][]byte, dirs map[string]bool) {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gzip: %v", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	files = map[string][]byte{}
	dirs = map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		if h.Typeflag == tar.TypeDir {
			dirs[h.Name] = true
			continue
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatalf("tar read %s: %v", h.Name, err)
		}
		files[h.Name] = b
	}
	return files, dirs
}

func TestArchiveMainTarGz(t *testing.T) {
	_, mux, token, _ := archiveRepo(t)

	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/archive/main.tar.gz", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive: %d %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Fatalf("content-type = %q, want application/gzip", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd == "" {
		t.Fatalf("missing Content-Disposition")
	}

	files, dirs := untarGz(t, rec.Body.Bytes())
	if got := string(files["README.md"]); got != "# app\n" {
		t.Fatalf("README.md = %q", got)
	}
	if got := string(files["src/main.go"]); got != "package main\n" {
		t.Fatalf("src/main.go = %q", got)
	}
	if !bytes.Equal(files["logo.bin"], []byte{0x00, 0x01, 0x02, 0xff, 0x00, 0x42}) {
		t.Fatalf("logo.bin bytes = %v", files["logo.bin"])
	}
	if !dirs["src/"] {
		t.Fatalf("expected a src/ directory entry, got dirs=%v files=%v", dirs, keys(files))
	}
	if len(files) != 3 {
		t.Fatalf("expected 3 files, got %d: %v", len(files), keys(files))
	}
}

func TestArchiveEmptyRepoIsValidTarGz(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	// hostingTestServer gives main one commit; strip it to an empty tree.
	ws := revision.NewWorkspace(repo)
	tid, err := ws.WriteTree(object.NewTree())
	if err != nil {
		t.Fatal(err)
	}
	_, rev, err := ws.Commit(revision.CommitParams{TreeID: tid, Description: "empty", Author: store.Author{Name: "t"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("main", store.RefBranch, rev.ID); err != nil {
		t.Fatal(err)
	}

	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/archive/main.tar.gz", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("empty archive: %d %s", rec.Code, rec.Body.String())
	}
	files, dirs := untarGz(t, rec.Body.Bytes())
	if len(files) != 0 || len(dirs) != 0 {
		t.Fatalf("empty archive should hold nothing, got files=%v dirs=%v", keys(files), dirs)
	}
}

func TestArchiveUnknownRef404(t *testing.T) {
	_, mux, token, _ := archiveRepo(t)
	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/archive/nope.tar.gz", token, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown ref = %d, want 404", rec.Code)
	}
}

func TestArchiveMissingSuffix400(t *testing.T) {
	_, mux, token, _ := archiveRepo(t)
	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/archive/main", token, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing .tar.gz = %d, want 400", rec.Code)
	}
}

func TestArchivePrivateRepoAnonymousForbidden(t *testing.T) {
	_, mux, _, repo := archiveRepo(t)
	if err := repo.UpdateRepoMeta(store.RepoMeta{Visibility: "private", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/archive/main.tar.gz", "", nil)
	if rec.Code != http.StatusUnauthorized && rec.Code != http.StatusForbidden {
		t.Fatalf("anonymous private archive = %d, want 401/403", rec.Code)
	}
}

// TestArchiveReadsByRefAndRevision covers a non-default ref: a tag and a raw
// revision id both resolve.
func TestArchiveReadsByRefAndRevision(t *testing.T) {
	_, mux, token, repo := archiveRepo(t)
	ws := revision.NewWorkspace(repo)
	tip, err := ws.GetRef("main")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SetRef("v1", store.RefTag, tip.Target); err != nil {
		t.Fatal(err)
	}
	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/archive/v1.tar.gz", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive by tag: %d %s", rec.Code, rec.Body.String())
	}
	files, _ := untarGz(t, rec.Body.Bytes())
	if _, ok := files["README.md"]; !ok {
		t.Fatalf("archive by tag missing README.md")
	}

	// And by raw revision id.
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/archive/"+tip.Target+".tar.gz", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("archive by revision id: %d %s", rec.Code, rec.Body.String())
	}
}

func keys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
