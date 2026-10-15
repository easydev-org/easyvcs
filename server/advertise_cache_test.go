package server

import (
	"net/http"
	"testing"
	"time"
)

// The advertise cache serves a memoized response within its TTL and is cleared
// by any write (so a client never sees stale refs after its own push).
func TestAdvertiseCacheHitAndInvalidate(t *testing.T) {
	s, mux, tok, _ := hostingTestServer(t)
	// A long TTL so the second call would hit the cache if we did not invalidate.
	s.advCache = newAdvertiseCache(time.Minute)

	rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/advertise", tok, map[string]any{})
	if rec.Code != 200 {
		t.Fatalf("advertise: %d %s", rec.Code, rec.Body.String())
	}
	first := decode(t, rec)
	n1 := len(first["changes"].([]any))

	// A second advertise returns the SAME cached body (no recompute).
	rec = hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/advertise", tok, map[string]any{})
	second := decode(t, rec)
	if len(second["changes"].([]any)) != n1 {
		t.Fatalf("cached advertise changed: %d vs %d", len(second["changes"].([]any)), n1)
	}

	// A write (commit) must INVALIDATE the cache: the next advertise reflects it.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/branches", tok,
		map[string]any{"name": "feature", "from": "main"}); rec.Code != 200 {
		t.Fatalf("create branch: %d %s", rec.Code, rec.Body.String())
	}
	rec = hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/advertise", tok, map[string]any{})
	third := decode(t, rec)
	// The new branch must appear in refs.
	refs, _ := third["refs"].([]any)
	found := false
	for _, r := range refs {
		m, _ := r.(map[string]any)
		if m["Name"] == "feature" || m["name"] == "feature" {
			found = true
		}
	}
	if !found {
		t.Fatalf("advertise after write is stale (feature branch missing): %v", refs)
	}
}

// A zero TTL disables the cache (always recompute).
func TestAdvertiseCacheDisabled(t *testing.T) {
	c := newAdvertiseCache(0)
	if _, ok := c.get(1); ok {
		t.Fatal("zero-TTL cache must never hit")
	}
}
