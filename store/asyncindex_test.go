package store

import (
	"fmt"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// With the async index, a revision's path rows land shortly after the commit,
// and RevisionsTouching still sees them (read-your-writes via flush).
func TestAsyncIndexReadYourWrites(t *testing.T) {
	cs := newTestCentral(t)
	cs.WithAsyncIndex()
	repo, _ := cs.Create(NSRef{Name: "async"})

	for i := 0; i < 20; i++ {
		rev := &Revision{ID: fmt.Sprintf("r%02d", i), Hash: object.BlobID([]byte(fmt.Sprintf("h%d", i))), Created: now(), ChangedPaths: []string{fmt.Sprintf("pkg/file%d.txt", i)}}
		if err := repo.PutRevision(rev); err != nil {
			t.Fatal(err)
		}
	}
	// Immediately query: the flush must make every just-written revision visible.
	for i := 0; i < 20; i++ {
		ids, err := repo.RevisionsTouching(fmt.Sprintf("pkg/file%d.txt", i))
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != fmt.Sprintf("r%02d", i) {
			t.Fatalf("read-your-writes failed for file%d: %v", i, ids)
		}
	}
	// A directory query sees ALL revisions that touched it.
	ids, err := repo.RevisionsTouching("pkg")
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 20 {
		t.Fatalf("pkg should touch 20 revisions, got %d", len(ids))
	}
}

// The async queue drains even without a subsequent read (background worker).
func TestAsyncIndexBackgroundDrain(t *testing.T) {
	cs := newTestCentral(t)
	cs.WithAsyncIndex()
	repo, _ := cs.Create(NSRef{Name: "drain"})
	_ = repo.PutRevision(&Revision{ID: "r1", Hash: object.BlobID([]byte("h")), Created: now(), ChangedPaths: []string{"a/b.txt"}})
	// flush waits for the worker to apply; then a DIRECT table read (bypassing
	// the queue) must see the row.
	cs.idxQueue.flush(repo.NamespaceID())
	var n int64
	if err := cs.d.gdb.Model(&revisionPathDirRow{}).Where("namespace_id=? AND revision_id=?", repo.NamespaceID(), "r1").Count(&n).Error; err != nil {
		t.Fatal(err)
	}
	if n != 3 { // dirs: "", "a", "a/b.txt"
		t.Fatalf("index rows = %d, want 3", n)
	}
}
