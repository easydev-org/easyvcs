package revision

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"sort"
	"testing"
)

// TestWriteSubtreeTarGz verifies the subtree archive is rooted AT the requested
// directory: the subtree's own entries appear at the archive root, and paths
// outside the subtree are absent.
func TestWriteSubtreeTarGz(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, dir string) {
		buildFS(t, dir, "pkg/a.txt", "a\n")
		buildFS(t, dir, "pkg/sub/b.txt", "b\n")
		buildFS(t, dir, "top.txt", "t\n")
		treeID, err := ws.BuildTreeFromFS(dir)
		if err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		if err := ws.WriteSubtreeTarGz(treeID, "pkg", &buf); err != nil {
			t.Fatalf("WriteSubtreeTarGz: %v", err)
		}
		names := tarNames(t, buf.Bytes())
		sort.Strings(names)
		want := []string{"a.txt", "sub/", "sub/b.txt"}
		if len(names) != len(want) {
			t.Fatalf("entries = %v, want %v", names, want)
		}
		for i := range want {
			if names[i] != want[i] {
				t.Fatalf("entries = %v, want %v", names, want)
			}
		}

		// A non-directory path is an error.
		if err := ws.WriteSubtreeTarGz(treeID, "top.txt", io.Discard); err == nil {
			t.Fatal("expected error archiving a file path")
		}
		// The empty path equals the whole tree (4 entries: pkg/, pkg/a.txt,
		// pkg/sub/, pkg/sub/b.txt, top.txt → 5).
		buf.Reset()
		if err := ws.WriteSubtreeTarGz(treeID, "", &buf); err != nil {
			t.Fatal(err)
		}
		if n := len(tarNames(t, buf.Bytes())); n != 5 {
			t.Fatalf("whole-tree archive = %d entries, want 5", n)
		}
	})
}

func tarNames(t *testing.T, gz []byte) []string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	var names []string
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names = append(names, h.Name)
	}
	return names
}
