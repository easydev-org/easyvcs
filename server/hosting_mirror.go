package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/easylab-platform/easyvcs/mirror"
	"github.com/easylab-platform/easyvcs/store"
)

// ---- push mirrors ----
//
// A push mirror force-pushes ONE branch's tree to an external git destination
// (see mirror.Push). It replaces the Forgejo push_mirrors surface the workspace
// gateway used: create/list/delete + an explicit sync, plus a background
// scheduler (cmd/server) that periodically re-pushes every mirror.
//
// The mirror token is a secret: it is stored encrypted and is NEVER echoed in a
// response or written to a log.

// pushMirrorReq is the create/replace body.
type pushMirrorReq struct {
	Name   string `json:"name"`
	URL    string `json:"url"`
	Branch string `json:"branch"`
	Token  string `json:"token"`
}

// pushMirrorView renders a mirror WITHOUT its token.
func pushMirrorView(m *store.PushMirror) map[string]any {
	return map[string]any{
		"name":       m.Name,
		"url":        m.URL,
		"branch":     m.Branch,
		"last_rev":   m.LastRev,
		"last_error": m.LastError,
	}
}

func (s *Server) handleCreatePushMirror(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	req, err := decodeJSONBody[pushMirrorReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.URL = strings.TrimSpace(req.URL)
	if req.Name == "" || req.URL == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name and url are required"))
		return
	}
	if req.Branch == "" {
		req.Branch = "main"
	}
	m := &store.PushMirror{Name: req.Name, URL: req.URL, Branch: req.Branch, Token: req.Token}
	if err := repo.PutPushMirror(m); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	stored, err := repo.GetPushMirror(req.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "mirror": pushMirrorView(stored)})
}

func (s *Server) handleListPushMirrors(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	mirrors, err := repo.ListPushMirrors()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(mirrors))
	for _, m := range mirrors {
		out = append(out, pushMirrorView(m))
	}
	writeJSON(w, http.StatusOK, map[string]any{"push_mirrors": out})
}

func (s *Server) handleDeletePushMirror(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	name := r.PathValue("mirror")
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("mirror name is required"))
		return
	}
	if err := repo.DeletePushMirror(name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// handleSyncPushMirror force-pushes one mirror immediately and records the
// outcome (last_rev on success, last_error on failure). A failed push never
// affects the server: the error is reported to the caller and stored.
func (s *Server) handleSyncPushMirror(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	name := r.PathValue("mirror")
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("mirror name is required"))
		return
	}
	m, err := repo.GetPushMirror(name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	res, perr := mirror.Push(r.Context(), repo, mirror.PushTarget{
		Name: m.Name, URL: m.URL, Branch: m.Branch, Token: m.Token, LastRev: m.LastRev,
	})
	if perr != nil {
		// Record the failure (best-effort) and report it. The message never
		// contains the token: mirror.run injects the credential via the git
		// config env, not the command line.
		_ = repo.UpdatePushMirrorSync(m.Name, m.LastRev, perr.Error())
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": perr.Error()})
		return
	}
	if err := repo.UpdatePushMirrorSync(m.Name, res.RevID, ""); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "revision": res.RevID})
}

// RunPushMirrorScheduler periodically force-pushes every configured push mirror.
// It is a thin loop over store.AllPushMirrors + mirror.Push: one mirror's
// failure is recorded in its last_error and never stops the others. It returns
// when ctx is cancelled (graceful shutdown). An interval <= 0 disables it.
func RunPushMirrorScheduler(ctx context.Context, cs *store.CentralStore, interval, timeout time.Duration) {
	if interval <= 0 {
		return
	}
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			SyncAllPushMirrors(ctx, cs, timeout)
		}
	}
}

// SyncAllPushMirrors runs one scheduler pass over every mirror. It is exported
// so a test can drive a single pass without waiting on a ticker.
func SyncAllPushMirrors(ctx context.Context, cs *store.CentralStore, timeout time.Duration) {
	mirrors, err := cs.AllPushMirrors()
	if err != nil {
		log.Printf("push-mirror scheduler: list: %v", err)
		return
	}
	for _, m := range mirrors {
		// Resolve the repository from the mirror's NamespaceID (no compound label to
		// split: AllPushMirrors returns the mirror's own Name + NamespaceID).
		repoRef, err := cs.NSRefByID(m.NamespaceID)
		if err != nil {
			log.Printf("push-mirror scheduler: repo %d: %v", m.NamespaceID, err)
			continue
		}
		repo, err := cs.OpenRepo(repoRef)
		if err != nil {
			log.Printf("push-mirror scheduler: open %s: %v", repoRef, err)
			continue
		}
		runCtx := ctx
		var cancel context.CancelFunc
		if timeout > 0 {
			runCtx, cancel = context.WithTimeout(ctx, timeout)
		}
		res, perr := mirror.Push(runCtx, repo, mirror.PushTarget{
			Name: m.Name, URL: m.URL, Branch: m.Branch, Token: m.Token, LastRev: m.LastRev,
		})
		if cancel != nil {
			cancel()
		}
		if perr != nil {
			_ = repo.UpdatePushMirrorSync(m.Name, m.LastRev, perr.Error())
			log.Printf("push-mirror %s/%s: %v", repoRef, m.Name, perr)
			continue
		}
		_ = repo.UpdatePushMirrorSync(m.Name, res.RevID, "")
	}
}
