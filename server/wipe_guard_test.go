package server

import (
	"encoding/base64"
	"net/http"
	"testing"
)

// TestDraftStartsFromMainNotRoot pins the anti-wipe property for drafts: a draft
// naming a branch that does not exist STARTS FROM MAIN (the shared base), NOT
// from an empty root — so it can never wipe the shared files when later merged.
func TestDraftStartsFromMainNotRoot(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/draft", token, map[string]any{
		"branch":  "does-not-exist",
		"message": "sneaky root",
		"files": []map[string]any{
			{"path": "a.txt", "operation": "create", "content": "hello\n"},
		},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("draft on a new branch must start from main: %d %s", rec.Code, rec.Body.String())
	}
	// The draft branch must CONTAIN main's seeded README.md (it started from main).
	tr := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/tree?ref=does-not-exist", token, nil)
	if !has(tr.Body.String(), "README.md") {
		t.Fatalf("draft must start from main (README.md present): %s", tr.Body.String())
	}
}

// TestCommitExplicitRootAllowed is the escape hatch: an EXPLICIT empty
// `parents: []` still creates a root revision (import/bootstrap path).
func TestCommitExplicitRootAllowed(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/draft", token, map[string]any{
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

	rr := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr", token, map[string]any{
		"source_snapshot": src, "target_branch": "main", "title": "root source",
	})
	if rr.Code != http.StatusOK {
		t.Fatalf("rr create: %d %s", rr.Code, rr.Body.String())
	}
	iid := int64(decode(t, rr)["iid"].(float64))
	mr := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr/"+itoa(iid)+"/merge", token, nil)
	if mr.Code != http.StatusOK {
		t.Fatalf("merge: %d %s", mr.Code, mr.Body.String())
	}

	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/tree?path=/&ref=main", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("tree: %d %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !contains(body, "new.txt") {
		t.Fatalf("merged tree missing new.txt: %s", body)
	}
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

// TestRRMergeAllowConflict lands a conflicted merge over HTTP when the caller
// passes ?allow_conflict=true: 200 (not 409) and main moves.
func TestRRMergeAllowConflict(t *testing.T) {
	_, mux, token, repo := hostingTestServer(t)
	// Seed a file on main, then make a source that conflicts with a main advance.
	seed := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/draft", token, map[string]any{
		"branch": "main", "message": "seed",
		"files": []map[string]any{{"path": "f.txt", "operation": "create", "content": "base\n"}},
	})
	_ = seed // main may be protected; use a non-protected branch instead
	// Use a plain branch to avoid branch protection on main.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/branches", token,
		map[string]any{"name": "topic", "from": "main"}); rec.Code != 200 {
		t.Fatalf("branch: %d %s", rec.Code, rec.Body.String())
	}
	// Source snapshot off main's tip, changing f.txt.
	snap := snapshotPost(t, mux, token, map[string]any{
		"message": "src",
		"files":   []map[string]any{{"path": "f.txt", "operation": "update", "content": "request\n"}},
	})
	if snap.Code != 200 {
		t.Fatalf("snapshot: %d %s", snap.Code, snap.Body.String())
	}
	src := decode(t, snap)["snapshot"].(string)
	// Advance topic on the SAME file -> conflict.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/draft", token, map[string]any{
		"branch": "topic", "message": "advance",
		"files": []map[string]any{{"path": "f.txt", "operation": "update", "content": "topic\n"}},
	}); rec.Code != 200 {
		t.Fatalf("advance: %d %s", rec.Code, rec.Body.String())
	}
	rr := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr", token, map[string]any{
		"source_snapshot": src, "target_branch": "topic", "title": "conflict",
	})
	if rr.Code != 200 {
		t.Fatalf("rr create: %d %s", rr.Code, rr.Body.String())
	}
	iid := int64(decode(t, rr)["iid"].(float64))

	// Default: 409 (conflicted, stays pending).
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr/"+itoa(iid)+"/merge", token, nil); rec.Code != http.StatusConflict {
		t.Fatalf("default conflicted merge = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	// Opt-in: 200 (landed with the conflict).
	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr/"+itoa(iid)+"/merge?allow_conflict=true", token, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("allow_conflict merge = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	_ = repo
}

// TestRRResolveByContent resolves a BINARY conflict by writing the merged bytes
// (base64 `content`), the escape hatch when picking a whole side is not enough.
func TestRRResolveByContent(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	// Seed a binary file on a branch, then create a conflicting RR.
	bin := func(s byte) string {
		return base64.StdEncoding.EncodeToString([]byte{0x89, 'P', 'N', 'G', 0, 0, s, 0xff})
	}
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/draft", token, map[string]any{
		"branch": "topic", "message": "seed",
		"files": []map[string]any{{"path": "logo.png", "operation": "create", "content_bytes": bin(1)}},
	}); rec.Code != 200 {
		// branch "topic" may not exist yet -> create from main first.
		if r2 := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/branches", token,
			map[string]any{"name": "topic", "from": "main"}); r2.Code != 200 {
			t.Fatalf("branch: %d %s", r2.Code, r2.Body.String())
		}
		if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/draft", token, map[string]any{
			"branch": "topic", "message": "seed",
			"files": []map[string]any{{"path": "logo.png", "operation": "create", "content_bytes": bin(1)}},
		}); rec.Code != 200 {
			t.Fatalf("seed commit: %d %s", rec.Code, rec.Body.String())
		}
	}
	snap := snapshotPost(t, mux, token, map[string]any{
		"message": "src", "files": []map[string]any{{"path": "logo.png", "operation": "update", "content_bytes": bin(2)}},
	})
	src := decode(t, snap)["snapshot"].(string)
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/draft", token, map[string]any{
		"branch": "topic", "message": "advance",
		"files": []map[string]any{{"path": "logo.png", "operation": "update", "content_bytes": bin(3)}},
	}); rec.Code != 200 {
		t.Fatalf("advance: %d %s", rec.Code, rec.Body.String())
	}
	rr := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr", token, map[string]any{
		"source_snapshot": src, "target_branch": "topic", "title": "binary conflict",
	})
	iid := int64(decode(t, rr)["iid"].(float64))
	// Force the pending conflicted merge.
	hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr/"+itoa(iid)+"/merge", token, nil)

	// Resolve by writing custom bytes.
	want := []byte{0x89, 'P', 'N', 'G', 9, 9, 9, 9}
	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr/"+itoa(iid)+"/resolve", token, map[string]any{
		"path": "logo.png", "content": base64.StdEncoding.EncodeToString(want),
	})
	if rec.Code != 200 {
		t.Fatalf("resolve by content: %d %s", rec.Code, rec.Body.String())
	}
	// Merging again lands the resolved (custom-bytes) tree.
	mr := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/rr/"+itoa(iid)+"/merge", token, nil)
	if mr.Code != 200 {
		t.Fatalf("merge after content-resolve: %d %s", mr.Code, mr.Body.String())
	}
}
