package server

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

// The web UI (Svelte 5 + Vite + Tailwind, built by ../webui → ./ui/dist) is
// embedded so the server binary serves it with no external files. `all:`
// includes the Vite hashed assets.
//
// The build is NOT committed: the Dockerfile runs `npm run build` before
// `go build`, and a local build embeds only the .gitkeep placeholder (so /ui/
// serves a placeholder, not a real SPA). See .gitignore.
//
//go:embed all:ui/dist
var uiDist embed.FS

// mountUI serves the embedded SPA at /ui/. It is unauthenticated: reads are
// anonymous on public repos, and the SPA sends an operator-pasted token as a
// Bearer header for private repos. Unknown sub-paths fall back to index.html.
func (s *Server) mountUI(mux *http.ServeMux) {
	sub, err := fs.Sub(uiDist, "ui/dist")
	if err != nil {
		return
	}
	fileServer := http.FileServerFS(sub)
	mux.HandleFunc("GET /ui", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/ui/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("GET /ui/", func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path[len("/ui/"):]
		if p != "" {
			if _, err := fs.Stat(sub, p); err != nil {
				// SPA fallback ONLY for client-side ROUTES (extension-less
				// paths). A missing FILE (has an extension, e.g. a stale
				// /ui/assets/foo.js) must 404 — serving index.html with 200 for
				// it breaks the browser (and PWA precache) with wrong-MIME
				// content instead of a clean cache miss.
				if path.Ext(p) != "" {
					http.NotFound(w, r)
					return
				}
				r = r.Clone(r.Context())
				r.URL.Path = "/ui/"
			}
		}
		http.StripPrefix("/ui/", fileServer).ServeHTTP(w, r)
	})
}
