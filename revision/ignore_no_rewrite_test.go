package revision

import (
	"testing"

	"github.com/easylab-platform/easyvcs/object"
)

// TestIgnoreChangeDoesNotRewriteHistory pins the invariant that changing ignore
// rules NEVER rewrites existing history: a revision's snapshot sha must not
// change when a .gitignore that would match an already-committed file is added.
// (Ignore rules affect only NEW commits — like git, which never rewrites
// history when .gitignore changes.)
func TestIgnoreChangeDoesNotRewriteHistory(t *testing.T) {
	runBoth(t, func(t *testing.T, ws *Workspace, dir string) {
		// Commit 1 tracks a.txt + secret.key (no ignore rules yet).
		buildFS(t, dir, "a.txt", "a\n")
		buildFS(t, dir, "secret.key", "ss\n")
		snap1, ch1 := commitTree(t, ws, dir, "c1", nil)

		// Commit 2 on top.
		buildFS(t, dir, "b.txt", "b\n")
		snap2, ch2 := commitTree(t, ws, dir, "c2", []object.ID{snap1.RevisionHash})

		// Now add a .gitignore matching an already-committed file. Nothing in the
		// library rewrites history, so both snapshots must be byte-identical.
		buildFS(t, dir, ".gitignore", "*.key\n")

		got1, err := ws.GetRevision(ch1.ID)
		if err != nil {
			t.Fatal(err)
		}
		got2, err := ws.GetRevision(ch2.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got1.Hash != snap1.RevisionHash {
			t.Fatalf("revision 1 sha changed: %s -> %s (history was rewritten)", snap1.RevisionHash, got1.Hash)
		}
		if got2.Hash != snap2.RevisionHash {
			t.Fatalf("revision 2 sha changed: %s -> %s (history was rewritten)", snap2.RevisionHash, got2.Hash)
		}
		// And secret.key is still present in the (unmodified) tip snapshot.
		tipSnap, err := ws.GetSnapshot(got2.Hash)
		if err != nil {
			t.Fatal(err)
		}
		tr := ws.mustTree(tipSnap.TreeID)
		if _, ok := tr.Entries["secret.key"]; !ok {
			t.Fatal("secret.key must remain in history (ignore rules do not rewrite history)")
		}
	})
}
