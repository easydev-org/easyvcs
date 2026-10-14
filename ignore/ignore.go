// Package ignore implements gitignore/.vcsignore handling.
//
// It layers a single gitignore matcher that honors both .gitignore and
// .vcsignore files at every directory, mirroring how git exclude files from
// the tree. The matcher is path-based and supports the standard gitignore
// grammar (globs, directory-only, negation with `!`, nested files).
package ignore

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/sabhiram/go-gitignore"
)

// FileNames are the ignore files read at each directory level.
var FileNames = []string{".gitignore", ".vcsignore"}

// Matcher answers whether a repo-relative path (forward slashes) should be
// excluded from a snapshot/build.
type Matcher struct {
	ignores []*ignore.GitIgnore
	// dirs are the ABSOLUTE directories each layer's patterns are relative to,
	// ordered outermost-first (ancestors above the scan root, then the scan
	// root and its descendants). scanRoot is the absolute scan root that
	// Ignored's relative paths are anchored at.
	dirs     []string
	scanRoot string
}

// WorkspaceMarkerFile names the workspace pointer file. Its presence marks a
// directory as a workspace (namespace) root: the UPWARD ignore walk stops there.
// Kept as a literal here so the ignore package has no dependency on store.
const WorkspaceMarkerFile = ".easyvcs-workspace"

// New loads ignore files for the tree rooted at root, in git order:
//
//  1. UPWARD: the ignore files in root's ANCESTORS, from the outermost down to
//     root — but only while still INSIDE the workspace (the walk stops at the
//     directory holding the workspace marker, or immediately if none is found,
//     so unrelated parent directories are never consulted).
//  2. DOWNWARD: every .gitignore/.vcsignore at root and below.
//
// Later (deeper) layers take precedence, matching git's last-match-wins.
func New(root string) (*Matcher, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	m := &Matcher{scanRoot: abs}

	// 1. Ancestor layers (outermost first), stopping at the workspace root.
	for _, l := range ancestorLayers(abs) {
		if err := m.addLayer(l.dir, l.file); err != nil {
			return nil, err
		}
	}

	// 2. Downward layers from the scan root.
	err = filepath.Walk(abs, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info == nil || info.IsDir() {
			return nil
		}
		name := filepath.Base(path)
		if !isIgnoreFile(name) {
			return nil
		}
		return m.addLayer(filepath.Dir(path), path)
	})
	return m, err
}

// addLayer compiles the ignore file at filePath (whose patterns are relative to
// dir) and appends it as the next (deeper) layer.
func (m *Matcher) addLayer(dir, filePath string) error {
	lines, err := readLines(filePath)
	if err != nil {
		return err
	}
	m.ignores = append(m.ignores, ignore.CompileIgnoreLines(lines...))
	m.dirs = append(m.dirs, dir)
	return nil
}

// ignoreLayer is one ignore file: its directory (absolute) and file path.
type ignoreLayer struct{ dir, file string }

// ancestorLayers returns the ancestor layers of root, OUTERMOST first, walking
// up from root's parent only while inside the workspace (stopping at the
// directory that holds the workspace marker). If no marker is found, no
// ancestors are returned — an unrelated parent tree is never consulted.
func ancestorLayers(root string) []ignoreLayer {
	var chain []string
	cur := filepath.Dir(root)
	foundWorkspace := false
	for {
		chain = append(chain, cur)
		if fileExists(filepath.Join(cur, WorkspaceMarkerFile)) {
			foundWorkspace = true
			break
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			break // reached the filesystem root
		}
		cur = parent
	}
	if !foundWorkspace {
		return nil
	}
	// Outermost-first, keeping only layers that actually carry an ignore file.
	var out []ignoreLayer
	for i := len(chain) - 1; i >= 0; i-- {
		for _, f := range FileNames {
			p := filepath.Join(chain[i], f)
			if fileExists(p) {
				out = append(out, ignoreLayer{dir: chain[i], file: p})
			}
		}
	}
	return out
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// NewWithLines is a convenience for tests: build a matcher from a single set of
// patterns rooted at the given dir prefix (default ".").
func NewWithLines(prefix string, lines ...string) *Matcher {
	return &Matcher{ignores: []*ignore.GitIgnore{ignore.CompileIgnoreLines(lines...)}, dirs: []string{prefix}}
}

func isIgnoreFile(name string) bool {
	for _, f := range FileNames {
		if name == f {
			return true
		}
	}
	return false
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, ln := range strings.Split(string(data), "\n") {
		ln = strings.TrimRight(ln, "\r")
		out = append(out, ln)
	}
	return out, nil
}

// Ignored reports whether the repo-relative path (forward slashes) is ignored,
// applying each layer in order and honoring the last-match-wins semantics. A
// path is ignored if any layer matches it and no later layer explicitly
// un-ignores it.
func (m *Matcher) Ignored(rel string) bool {
	if m == nil {
		return false
	}
	targetAbs := filepath.Join(m.scanRoot, filepath.FromSlash(rel))
	result := false
	// Layers are ordered outermost-first; deeper layers take precedence. A layer
	// that mentions the path (positively or via negation) overrides the result.
	for i, gi := range m.ignores {
		candidate, ok := layerCandidate(m.dirs[i], targetAbs)
		if !ok {
			continue // this layer is not an ancestor of (or at) the target
		}
		applies, ignored := layerApplies(gi, candidate)
		if applies {
			result = ignored
		}
	}
	return result
}

// layerCandidate returns the target path relative to a layer's directory, or
// ("", false) when the layer directory is not an ancestor of (or equal to) the
// target (so its patterns cannot apply).
func layerCandidate(layerDir, targetAbs string) (string, bool) {
	rel, err := filepath.Rel(layerDir, targetAbs)
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}

// HasAnyMatchUnder reports whether any ignore pattern could possibly match a
// path inside (or equal to) the given directory prefix. It is a conservative
// over-approximation: returning false guarantees no pattern applies to that
// subtree, so the caller can skip rewriting it altogether. Returning true does
// not mean a path matches — it only means the subtree must be inspected.
//
// A pattern compiled at layer dir L applies to paths under L. Therefore a
// directory `dir` can only be affected if some layer's L is an ancestor of, a
// descendant of, or equal to `dir`. A root layer (dir "." or "") matches
// everything and so always yields true.
func (m *Matcher) HasAnyMatchUnder(dir string) bool {
	if m == nil {
		return false
	}
	targetAbs := filepath.Join(m.scanRoot, filepath.FromSlash(dir))
	for i := range m.ignores {
		L := m.dirs[i]
		if L == "" || L == "." {
			return true // root layer matches the whole tree
		}
		// A layer applies to the subtree iff its dir is an ancestor of, a
		// descendant of, or equal to the target dir.
		rel, err := filepath.Rel(L, targetAbs)
		if err != nil {
			return true
		}
		rel = filepath.ToSlash(rel)
		if rel == ".." || strings.HasPrefix(rel, "../") {
			// L is NOT under target; maybe target is under L (layer above).
			rel2, err2 := filepath.Rel(targetAbs, L)
			if err2 == nil {
				r2 := filepath.ToSlash(rel2)
				if r2 != ".." && !strings.HasPrefix(r2, "../") {
					return true // target is an ancestor of L
				}
			}
			continue
		}
		return true // target is under L (or equal)
	}
	return false
}

// layerApplies reports whether a gitignore matcher mentions the candidate path
// at all (so it can override an outer layer), and whether the path ends up
// ignored after honoring negation within that file. A matching error from the
// pattern engine is treated as "does not apply" and the final verdict falls
// back to the plain MatchesPath evaluation, so a broken pattern never silently
// ignores (excludes) a file it should not.
func layerApplies(gi *ignore.GitIgnore, candidate string) (applies, ignored bool) {
	matched, err := gi.MatchesPathHow(candidate)
	if err != nil {
		// Fall back to the non-negation evaluation; conservatively "applies"
		// so the final MatchesPath verdict decides.
		return true, gi.MatchesPath(candidate)
	}
	if !matched {
		return false, false
	}
	// Re-evaluate ignoring with full negation semantics.
	return true, gi.MatchesPath(candidate)
}
