package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/easylab-platform/easyvcs/store"
)

// TestHealthzUnauthenticated verifies the liveness probe answers 200 with no
// credential (a k8s probe carries none) and never touches the store.
func TestHealthzUnauthenticated(t *testing.T) {
	s := newTestServer(t, "secret")
	mux := s.Router()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d %s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("healthz body: %v", err)
	}
	if m["ok"] != true {
		t.Fatalf("healthz ok = %v, want true", m["ok"])
	}
}

// TestReadyzUnauthenticated verifies the readiness probe answers 200 with no
// credential when the store is reachable.
func TestReadyzUnauthenticated(t *testing.T) {
	s := newTestServer(t, "secret")
	mux := s.Router()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("readyz: %d %s", rec.Code, rec.Body.String())
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if m["ok"] != true {
		t.Fatalf("readyz ok = %v, want true", m["ok"])
	}
}

// TestReadyzFailsWhenStoreClosed verifies readiness reports 503 once the store
// is unusable (a closed DB), so a load balancer stops routing to this instance.
func TestReadyzFailsWhenStoreClosed(t *testing.T) {
	s := newTestServer(t, "secret")
	mux := s.Router()

	// Closing the underlying store makes the readiness round-trip fail.
	if err := s.cs.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/readyz", nil)
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("readyz on closed store: got %d, want 503", rec.Code)
	}
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if m["ok"] != false {
		t.Fatalf("readyz ok = %v, want false", m["ok"])
	}
}

// TestProbesNotAuditedOrGuarded ensures the probes are neither ACL-guarded nor
// recorded: an anonymous healthz on an otherwise-closed instance is 200, and no
// audit event is written for it.
func TestProbesNotAuditedOrGuarded(t *testing.T) {
	cs := openStoreForTest(t)
	sink := &mockSink{}
	s := New(cs, sink)
	mux := s.Router()

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz: %d", rec.Code)
	}
	if len(sink.events) != 0 {
		t.Fatalf("probes must not be audited, got %d events", len(sink.events))
	}
}

// openStoreForTest opens a fresh central store in a temp home.
func openStoreForTest(t *testing.T) *store.CentralStore {
	t.Helper()
	t.Setenv("EASYVCS_HOME", t.TempDir())
	cs, err := store.OpenDefault()
	if err != nil {
		t.Fatal(err)
	}
	if err := cs.SetWAL(); err != nil {
		t.Fatal(err)
	}
	return cs
}
