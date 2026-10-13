package revision

import (
	"fmt"
	"strings"
	"testing"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/store"
)

// fixedSnapshot returns a deterministic snapshot used to pin the snapshotID
// byte layout. It deliberately has NO session.
func fixedSnapshot() *store.Snapshot {
	return &store.Snapshot{
		RevisionID:  "0123456789abcdef0123456789abcdef",
		TreeID:      object.BlobID([]byte("tree")),
		Description: "commit message",
		Author:      store.Author{Name: "a", Email: "b"},
		Parents:     []object.ID{object.BlobID([]byte("parent"))},
	}
}

// TestSnapshotIDSessionlessIsByteStable pins the hash of a session-less
// snapshot to the exact pre-session payload. If session ever becomes an
// unconditional line in the hash, this fails — which is the whole point: every
// existing snapshot id must stay valid.
func TestSnapshotIDSessionlessIsByteStable(t *testing.T) {
	s := fixedSnapshot()
	if s.Author.Session != "" {
		t.Fatal("fixture must have no session")
	}
	// The historical payload, rebuilt line-by-line (author renders as
	// "Name <email>"; session must NOT appear).
	var b strings.Builder
	fmt.Fprintf(&b, "revision=%s\n", s.RevisionID)
	fmt.Fprintf(&b, "tree=%s\n", s.TreeID)
	fmt.Fprintf(&b, "description=%s\n", s.Description)
	fmt.Fprintf(&b, "author=%s\n", s.Author)
	for _, p := range s.Parents {
		fmt.Fprintf(&b, "parent=%s\n", p)
	}
	want := object.BlobID([]byte(b.String()))
	if got := SnapshotID(s); got != want {
		t.Fatalf("sessionless snapshot id changed:\n got=%s\nwant=%s\npayload=%q", got, want, b.String())
	}
	// And the payload must not mention session.
	if strings.Contains(b.String(), "session=") {
		t.Fatalf("sessionless payload unexpectedly mentions session: %q", b.String())
	}
}

// TestSnapshotIDSessionChangesHash verifies that a non-empty session DOES enter
// the hash (so two otherwise-identical snapshots with different sessions differ).
func TestSnapshotIDSessionChangesHash(t *testing.T) {
	a := fixedSnapshot()
	b := fixedSnapshot()
	b.Author.Session = "org:repo:feat"
	if SnapshotID(a) == SnapshotID(b) {
		t.Fatal("a non-empty session must change the snapshot id")
	}
	// Session is the ONLY difference, so hashing must be deterministic.
	if SnapshotID(b) != SnapshotID(b) {
		t.Fatal("snapshot id must be deterministic")
	}
}
