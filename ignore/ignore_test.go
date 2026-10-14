package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIgnoredBasic(t *testing.T) {
	m := NewWithLines(".", "*.log", "build/", "!keep.log")
	if !m.Ignored("x.log") {
		t.Fatal("x.log should be ignored")
	}
	if m.Ignored("keep.log") {
		t.Fatal("keep.log should NOT be ignored (negation)")
	}
	if !m.Ignored("build/x.txt") {
		t.Fatal("build/x.txt should be ignored (dir rule)")
	}
	if m.Ignored("src/main.go") {
		t.Fatal("src/main.go should not be ignored")
	}
}

func TestNestedGitignoreLayer(t *testing.T) {
	root := t.TempDir()
	mk := func(p string) {
		_ = os.MkdirAll(filepath.Join(root, filepath.Dir(p)), 0o755)
		_ = os.WriteFile(filepath.Join(root, p), nil, 0o644)
	}
	_ = os.WriteFile(filepath.Join(root, ".gitignore"), []byte("*.tmp\n"), 0o644)
	_ = os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	// nested .gitignore ignores under sub/ (positive pattern in nested layer)
	_ = os.WriteFile(filepath.Join(root, "sub", ".gitignore"), []byte("*.build\n"), 0o644)
	mk("root.tmp")
	mk("sub/a.tmp")
	mk("sub/b.build")

	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Ignored("root.tmp") {
		t.Fatal("root.tmp should be ignored by root .gitignore")
	}
	if !m.Ignored("sub/a.tmp") {
		t.Fatal("sub/a.tmp should be ignored")
	}
	if !m.Ignored("sub/b.build") {
		t.Fatal("sub/b.build should be ignored by nested .gitignore")
	}
	if m.Ignored("other.txt") {
		t.Fatal("other.txt should not be ignored")
	}
}

func TestVcsIgnoreAlsoRespected(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, ".vcsignore"), []byte("secret*\n"), 0o644)
	m, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Ignored("secret.txt") {
		t.Fatal(".vcsignore pattern should be honored")
	}
	if m.Ignored("public.txt") {
		t.Fatal("public.txt should not be ignored")
	}
}

// The ignore walk is UPWARD too: scanning a SUBDIRECTORY must still honor the
// ignore files of its ANCESTORS — but only up to the workspace root (the dir
// holding the workspace marker). A marker-less parent tree is never consulted.
func TestUpwardIgnoreWithinWorkspace(t *testing.T) {
	ws := t.TempDir()
	// Workspace root: a marker + a root .gitignore.
	_ = os.WriteFile(filepath.Join(ws, ".easyvcs-workspace"), []byte("{}"), 0o644)
	_ = os.WriteFile(filepath.Join(ws, ".gitignore"), []byte("*.log\n"), 0o644)
	// A subdir with its own ignore, and a nested dir to scan.
	sub := filepath.Join(ws, "sub")
	_ = os.MkdirAll(filepath.Join(sub, "deep"), 0o755)
	_ = os.WriteFile(filepath.Join(sub, ".gitignore"), []byte("*.tmp\n"), 0o644)

	// Scan from sub/deep: it must see BOTH the root and sub ignore files.
	m, err := New(filepath.Join(sub, "deep"))
	if err != nil {
		t.Fatal(err)
	}
	if !m.Ignored("a.log") {
		t.Fatal("ancestor (workspace root) .gitignore should apply when scanning a subdir")
	}
	if !m.Ignored("b.tmp") {
		t.Fatal("ancestor (sub) .gitignore should apply when scanning a subdir")
	}
	if m.Ignored("keep.txt") {
		t.Fatal("keep.txt should not be ignored")
	}
}

// A parent directory OUTSIDE the workspace (no marker between them) is NEVER
// consulted, so an unrelated .gitignore above the workspace cannot leak in.
func TestUpwardIgnoreStopsAtWorkspace(t *testing.T) {
	outer := t.TempDir()
	// An unrelated .gitignore ABOVE the workspace.
	_ = os.WriteFile(filepath.Join(outer, ".gitignore"), []byte("*.secret\n"), 0o644)
	ws := filepath.Join(outer, "proj")
	_ = os.MkdirAll(ws, 0o755)
	_ = os.WriteFile(filepath.Join(ws, ".easyvcs-workspace"), []byte("{}"), 0o644)

	m, err := New(ws)
	if err != nil {
		t.Fatal(err)
	}
	if m.Ignored("x.secret") {
		t.Fatal("an ignore file OUTSIDE the workspace must not apply")
	}
}

// Without any workspace marker, NO ancestors are consulted (safe default).
func TestUpwardIgnoreNeedsMarker(t *testing.T) {
	outer := t.TempDir()
	_ = os.WriteFile(filepath.Join(outer, ".gitignore"), []byte("*.log\n"), 0o644)
	sub := filepath.Join(outer, "plain")
	_ = os.MkdirAll(sub, 0o755)

	m, err := New(sub)
	if err != nil {
		t.Fatal(err)
	}
	if m.Ignored("a.log") {
		t.Fatal("without a marker, parent .gitignore must not apply")
	}
}
