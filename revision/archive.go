package revision

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/easylab-platform/easyvcs/object"
)

// WriteTreeTarGz streams a tree as a gzipped tar archive to out. The archive is
// rooted at the tree itself (no wrapper directory): each entry appears at its
// repo-relative path. Directory entries are written with mode 0755 and files
// with 0644 (the object model does not record modes); a conflict object is
// rendered as conflict-marker text, exactly as Materialize does.
//
// It streams: at most one blob is held in memory at a time and no temporary
// files are used, so it is safe for large trees. treeID may be the empty tree,
// which yields a valid but empty archive. This is the "download the repository
// at a ref" primitive behind the /archive endpoint.
func (w *Workspace) WriteTreeTarGz(treeID object.ID, out io.Writer) error {
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	if err := w.writeTreeTar(tw, "", w.mustTree(treeID)); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

// WriteSubtreeTarGz streams the subtree at `path` (a repo-relative directory)
// as a gzipped tar rooted AT that directory: the subtree's own entries appear at
// the archive root (so a client can `tar -x` it as a standalone tree). An empty
// path is the whole tree (same as WriteTreeTarGz). A non-directory path returns
// an error. This backs `GET /archive/{ref}.tar.gz?path=<dir>`.
func (w *Workspace) WriteSubtreeTarGz(treeID object.ID, path string, out io.Writer) error {
	root := w.mustTree(treeID)
	path = strings.Trim(strings.TrimSpace(path), "/")
	if path != "" {
		entry, err := w.FindEntry(root, path)
		if err != nil {
			return err
		}
		if entry.Kind != object.KindTree {
			return fmt.Errorf("archive path %q is not a directory", path)
		}
		if root, err = w.ReadTree(entry.ID); err != nil {
			return err
		}
	}
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	if err := w.writeTreeTar(tw, "", root); err != nil {
		_ = tw.Close()
		_ = gz.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		_ = gz.Close()
		return err
	}
	return gz.Close()
}

// writeTreeTar walks a tree depth-first, writing each entry to the tar stream.
// prefix is the repo-relative directory of t ("" at the root).
func (w *Workspace) writeTreeTar(tw *tar.Writer, prefix string, t *object.Tree) error {
	for _, e := range t.SortedEntries() {
		name := joinPath(prefix, e.Name)
		if e.Kind == object.KindTree {
			if err := tw.WriteHeader(&tar.Header{
				Typeflag: tar.TypeDir,
				Name:     name + "/",
				Mode:     0o755,
				ModTime:  time.Unix(0, 0),
			}); err != nil {
				return err
			}
			sub, err := w.ReadTree(e.ID)
			if err != nil {
				return err
			}
			if err := w.writeTreeTar(tw, name, sub); err != nil {
				return err
			}
			continue
		}
		data, err := w.leafArchiveBytes(e)
		if err != nil {
			return err
		}
		if err := tw.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(data)),
			ModTime:  time.Unix(0, 0),
		}); err != nil {
			return err
		}
		if _, err := tw.Write(data); err != nil {
			return err
		}
	}
	return nil
}

// leafArchiveBytes returns the bytes to store for a non-directory entry: a
// blob's raw content, or the rendered conflict-marker text for a conflict
// object (matching Materialize's on-disk rendering).
func (w *Workspace) leafArchiveBytes(e object.Entry) ([]byte, error) {
	o, err := w.store.ReadObject(e.ID)
	if err != nil {
		return nil, err
	}
	switch o.Kind {
	case object.KindBlob:
		return o.Blob, nil
	case object.KindConflict:
		text, err := w.renderConflict(e.ID)
		if err != nil {
			return nil, err
		}
		return []byte(text), nil
	default:
		return nil, fmt.Errorf("archive: entry %q is neither a blob nor a conflict", e.Name)
	}
}
