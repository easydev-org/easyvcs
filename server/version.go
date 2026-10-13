package server

import "os"

// appVersion is the deployed image tag, injected as APP_VERSION by the chart
// (falls back to "dev"). Surfaced in /api/v1/healthz + /readyz.
func appVersion() string {
	if v := os.Getenv("APP_VERSION"); v != "" {
		return v
	}
	return "dev"
}
