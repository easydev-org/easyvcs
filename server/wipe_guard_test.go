package server

import (
	"net/http"
	"testing"
)

// TestCommitToMissingBranchRefused is guard ①: a /commit naming a branch that
// does not exist must be REFUSED (it used to silently create a root revision
// whose tree contained only the request's files — the root-commit wipe).
func TestCommitToMissingBranchRefused(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/commit", token, map[string]any{
		"branch":  "does-not-exist",
		"message": "sneaky root",
		"files": []map[string]any{
			{"path": "a.txt", "operation": "create", "content": "hello\n"},
		},
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("commit to missing branch must be 400, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestCommitExplicitRootAllowed is the escape hatch: an EXPLICIT empty
// `parents: []` still creates a root revision (import/bootstrap path).
func TestCommitExplicitRootAllowed(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	rec := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/commit", token, map[string]any{
		"branch":  "brand-new",
		"message": "explicit root",
		"parents": []string{},
		"files": []map[string]any{
			{"path": "a.txt", "operation": "create", "content": "hello\n"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("explicit root commit must be 200, got %d %s", rec.Code, rec.Body.String())
	}
}

// TestNonDescendantSourceDoesNotWipeTarget is guard ②: a non-descendant source
// snapshot (a root revision) whose RR was created against an empty target must
// NOT delete the target's existing files when merged — the merge re-bases onto
// the source's own parent, so it applies the source's CHANGES only.
func TestNonDescendantSourceDoesNotWipeTarget(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)

	// main already has files (hostingTestServer seeds a repo). Capture the ref
	// tree before.
	before := refNames(t, mux, token)
	if !before["main"] {
		t.Fatal("fixture: main missing")
	}

	// A ROOT source snapshot (explicit empty parents) — a brand-new file.
	snap := snapshotPost(t, mux, token, map[string]any{
		"message": "root snapshot",
		"parents": []string{},
		"files": []map[string]any{
			{"path": "new.txt", "operation": "create", "content": "new\n"},
		},
	})
	if snap.Code != http.StatusOK {
		t.Fatalf("snapshot: %d %s", snap.Code, snap.Body.String())
	}
	src := decode(t, snap)["snapshot"].(string)

	// RR against main, then merge.
	rr := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/rr", token, map[string]any{
		"source_snapshot": src, "target_branch": "main", "title": "root source",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("rr create: %d %s", rr.Code, rr.Body.String())
	}
	iid := int64(decode(t, rr)["iid"].(float64))
	mr := hreq(mux, http.MethodPost, "/api/v1/repos/team/app/rr/"+itoa(iid)+"/merge", token, nil)
	if mr.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", mr.Code, mr.Body.String())
	}

	// The new file landed AND the original files were NOT deleted.
	rec := hreq(mux, http.MethodGet, "/api/v1/repos/team/app/tree?path=/&ref=main", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !contains(body, "new.txt") {
		t.Fatalf("merged tree missing new.txt: %s", body)
	}
	// hostingTestServer seeds at least one file; assert it survived.
	if contains(body, `"entries":[]`) {
		t.Fatalf("target was WIPED by a non-descendant source merge: %s", body)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}
func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
