package server

import (
	"net/http"
)

// Liveness and readiness probes. They are intentionally UNAUTHENTICATED (a k8s
// probe or a reverse proxy has no credential) and are NOT audited. Both live
// under /api/v1 alongside every other route.
//
//	GET /api/v1/healthz  liveness  — the process is up; never touches the store
//	GET /api/v1/readyz   readiness — a cheap store round-trip; 503 when it fails
//
// They are mounted in router() directly (not wrapped by requireRepoAccess), so
// they carry no repo ACL and no token requirement.

// handleHealthz is the liveness probe: it reports 200 as long as the process is
// running and serving. It deliberately does not query the store — a liveness
// failure should mean "restart me", not "the DB is briefly slow".
func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": appVersion()})
}

// handleReadyz is the readiness probe: it performs one cheap, read-only store
// round-trip and reports 503 when the store is unreachable (so a load balancer
// stops routing to this instance until it recovers). The error text is generic
// because this endpoint is unauthenticated.
func (s *Server) handleReadyz(w http.ResponseWriter, _ *http.Request) {
	if _, err := s.cs.List(); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "version": appVersion(), "error": "store unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": appVersion()})
}
