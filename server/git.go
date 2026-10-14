package server

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/easylab-platform/easyvcs/gitserver"
	"github.com/easylab-platform/easyvcs/revision"
)

// isGitHTTPPath reports whether the request targets the git smart-HTTP
// endpoints (info/refs or git-upload-pack), which need a 401+WWW-Authenticate
// (not 403) so the git CLI retries with credentials.
func isGitHTTPPath(path string) bool {
	return strings.HasSuffix(path, "/info/refs") || strings.HasSuffix(path, "/git-upload-pack")
}

// gitHandler serves the git smart-HTTP protocol for a repository, INCLUDING a
// subdirectory: a monorepo `acme/mono` with a `pkg-a/` subdir is also a git
// remote at `.../acme/mono/pkg-a` (its own history = the commits that touch that
// path), so each subdir can be consumed as an independent package via a git+
// URL. It is mounted on the catch-all `.../{ns}/{name}/`.
//
// The clone URL is `<server>/api/v1/repos/{ns}/{name}[/<subdir>...]`.
func (s *Server) gitHandler(w http.ResponseWriter, r *http.Request) {
	// Path is /api/v1/namespaces/{ns}/<rest>: {ns} is the container (single
	// segment) and <rest> is "[<path...>/]info/refs|git-upload-pack". The scope
	// path is the leading segments of <rest> before the protocol suffix.
	trimmed := strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/")
	parts := strings.SplitN(trimmed, "/", 2)
	if len(parts) < 2 {
		http.NotFound(w, r)
		return
	}
	r.SetPathValue("ns", strings.TrimSuffix(parts[0], ".git"))
	rest := parts[1]

	// Go module vanity for a SUBDIRECTORY: `.../{ns}/pkg-a?go-get=1` tells `go`
	// to clone this subdir as its own module.
	if r.URL.Query().Get("go-get") == "1" {
		host := r.Host
		path := "/api/v1/namespaces/" + parts[0] + "/" + strings.Trim(rest, "/")
		path = strings.TrimSuffix(path, ".git")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!DOCTYPE html><html><head><meta name="go-import" content="%s%s git http://%s%s"></head><body>easyvcs</body></html>`,
			host, path, host, path)
		return
	}

	// `rest` is the path AFTER `{ns}/`. For the ROOT it is exactly "info/refs" /
	// "git-upload-pack" (no leading slash — the ServeMux pattern's trailing slash
	// is consumed), while for a subdir it is "<subdir>/info/refs". Trim a LEADING
	// slash first, then match the suffix.
	rest = strings.TrimPrefix(rest, "/")
	var sub string
	isInfoRefs := false
	switch {
	case strings.HasSuffix(rest, "info/refs"):
		sub = strings.TrimSuffix(rest, "info/refs")
		isInfoRefs = true
	case strings.HasSuffix(rest, "git-upload-pack"):
		sub = strings.TrimSuffix(rest, "git-upload-pack")
	default:
		http.NotFound(w, r)
		return
	}
	sub = strings.TrimSuffix(strings.Trim(sub, "/"), ".git")

	// Enforce the repo READ ACL (401+WWW-Authenticate on the git paths).
	s.requireRepoAccess(false, func(w http.ResponseWriter, r *http.Request) {
		repo, ok := s.repo(w, r)
		if !ok {
			return
		}
		sto, err := gitserver.Build(revision.NewWorkspace(repo), repo, sub)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		g := gitserver.New(sto)
		if isInfoRefs {
			g.InfoRefs(w, r)
		} else {
			g.UploadPack(w, r)
		}
	})(w, r)
}
