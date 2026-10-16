package server

import (
	"net/http"
	"testing"
)

// TestSearchNameAndContent pins the search endpoint: ?name= matches file paths,
// ?content= matches file contents (text only).
func TestSearchNameAndContent(t *testing.T) {
	_, mux, token, _ := hostingTestServer(t)
	// The fixture repo has a file; add a known one.
	if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/draft", token, map[string]any{
		"branch": "searchbr", "message": "seed",
		"files": []map[string]any{
			{"path": "pkg/util.go", "operation": "create", "content": "package pkg\n\n// Add returns a+b.\nfunc Add(a, b int) int { return a + b }\n"},
		},
	}); rec.Code != 200 {
		if r2 := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/branches", token, map[string]any{"name": "searchbr", "from": "main"}); r2.Code != 200 {
			t.Fatalf("branch: %d", r2.Code)
		}
		if rec := hreq(mux, http.MethodPost, "/api/v1/namespaces/team-app/draft", token, map[string]any{
			"branch": "searchbr", "message": "seed",
			"files": []map[string]any{{"path": "pkg/util.go", "operation": "create", "content": "package pkg\n\n// Add returns a+b.\nfunc Add(a, b int) int { return a + b }\n"}},
		}); rec.Code != 200 {
			t.Fatalf("seed: %d %s", rec.Code, rec.Body.String())
		}
	}
	rec := hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/search?ref=searchbr&name=util", token, nil)
	if rec.Code != 200 {
		t.Fatalf("search by name: %d %s", rec.Code, rec.Body.String())
	}
	res, _ := decode(t, rec)["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("name search results = %v", res)
	}
	rec = hreq(mux, http.MethodGet, "/api/v1/namespaces/team-app/search?ref=searchbr&content=Add", token, nil)
	if rec.Code != 200 {
		t.Fatalf("search by content: %d", rec.Code)
	}
	res, _ = decode(t, rec)["results"].([]any)
	if len(res) != 1 {
		t.Fatalf("content search results = %v", res)
	}
}
