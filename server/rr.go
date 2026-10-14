package server

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/easylab-platform/easyvcs/object"
	"github.com/easylab-platform/easyvcs/rr"
	"github.com/easylab-platform/easyvcs/store"
	"github.com/easylab-platform/easyvcs/transfer"
)

// Revision Request endpoints. Unlike a git merge request (a mutable branch
// pointer), an RR pins a content-addressed snapshot; the review never drifts.

// canPropose reports whether the caller may open/update a request. Writing a
// request always requires a credential; developer+ (or an open instance for dev
// ergonomics) may propose.
func (s *Server) canPropose(rref store.NSRef, tk *store.Token) bool {
	if tk == nil {
		return false
	}
	role, err := s.cs.RoleOfRef(rref, userIDVal(tk))
	if err == nil && role.CanPropose() {
		return true
	}
	return s.cs.IsOpenInstance()
}

// canMerge reports whether the caller may merge a request (owner/maintainer).
func (s *Server) canMerge(rref store.NSRef, tk *store.Token) bool {
	role, err := s.cs.RoleOfRef(rref, userIDVal(tk))
	if err == nil && role.CanMerge() {
		return true
	}
	return s.cs.IsOpenInstance()
}

// callerAuthor builds the store.Author to attribute a server-side commit to the
// authenticated caller: the username resolved from the token. An anonymous
// caller (open instance / no token) is attributed to "anonymous". The Session
// is left empty here (a caller may supply it; the server does not invent one).
func (s *Server) callerAuthor(tk *store.Token) store.Author {
	if tk == nil {
		return store.Author{Name: "anonymous"}
	}
	u, err := s.cs.GetUser(tk.UserID)
	if err != nil || u.Username == "" {
		return store.Author{Name: "anonymous"}
	}
	return store.Author{Name: u.Username, Email: u.Username + "@easyvcs"}
}

// requirePropose guards a request-mutating handler with the propose capability.
func (s *Server) requirePropose(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
		tk, ok := s.authenticate(r)
		if !ok {
			s.auditDenied(r, nil, "bad token")
			writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
			return
		}
		if tk == nil {
			// Mutating a request always requires a credential.
			s.auditDenied(r, nil, "anonymous propose")
			writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: propose requires a token"))
			return
		}
		rref := store.NSRef{Name: nsRefName(r)}
		if !s.canPropose(rref, tk) {
			s.auditDenied(r, tk, "no propose access")
			writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: no propose access to %s", rref))
			return
		}
		next(w, r.WithContext(withToken(r.Context(), tk)))
	}
}

// requireMerge guards a merge handler with the merge capability.
func (s *Server) requireMerge(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, s.maxBody)
		tk, ok := s.authenticate(r)
		if !ok {
			s.auditDenied(r, nil, "bad token")
			writeErr(w, http.StatusUnauthorized, errors.New("unauthorized: missing or invalid token"))
			return
		}
		rref := store.NSRef{Name: nsRefName(r)}
		if !s.canMerge(rref, tk) {
			s.auditDenied(r, tk, "no merge access")
			writeErr(w, http.StatusForbidden, fmt.Errorf("forbidden: no merge access to %s", rref))
			return
		}
		next(w, r.WithContext(withToken(r.Context(), tk)))
	}
}

// mountRR registers the Revision Request routes on mux.
func (s *Server) mountRR(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/{name}/rr", s.requirePropose(s.handleRRCreate))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/{name}/rr/objects", s.requirePropose(s.handleRRObjects))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/{name}/rr", s.requireRepoAccess(false, s.handleRRList))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/{name}/rr/{iid}", s.requireRepoAccess(false, s.handleRRGet))
	mux.HandleFunc("GET /api/v1/namespaces/{ns}/{name}/rr/{iid}/diff", s.requireRepoAccess(false, s.handleRRDiff))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/{name}/rr/{iid}/update", s.requirePropose(s.handleRRUpdate))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/{name}/rr/{iid}/review", s.requirePropose(s.handleRRReview))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/{name}/rr/{iid}/comment", s.requirePropose(s.handleRRComment))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/{name}/rr/{iid}/resolve", s.requirePropose(s.handleRRResolve))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/{name}/rr/{iid}/merge", s.requireMerge(s.handleRRMerge))
	mux.HandleFunc("POST /api/v1/namespaces/{ns}/{name}/rr/{iid}/close", s.requireMerge(s.handleRRClose))
}

// rrService opens the RR service for the request's repo.
func (s *Server) rrService(w http.ResponseWriter, r *http.Request) (*rr.Service, *store.Repo, bool) {
	repo, ok := s.repo(w, r)
	if !ok {
		return nil, nil, false
	}
	return rr.New(s.cs, repo), repo, true
}

// rrIID parses the {iid} path value.
func rrIID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	iid, err := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid iid %q", r.PathValue("iid")))
		return 0, false
	}
	return iid, true
}

// rrCreateReq is the create/update request body. A Revision Request's source is
// always a pinned snapshot.
type rrCreateReq struct {
	SourceSnapshot string `json:"source_snapshot"`
	TargetBranch   string `json:"target_branch"`
	// Path scopes the RR to a subdirectory (docs/subrepo-scopes.md §7.3).
	Path  string `json:"path"`
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (s *Server) handleRRCreate(w http.ResponseWriter, r *http.Request) {
	svc, _, ok := s.rrService(w, r)
	if !ok {
		return
	}
	req, err := decodeJSONBody[rrCreateReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.SourceSnapshot == "" {
		writeErr(w, http.StatusBadRequest, errors.New("source_snapshot is required"))
		return
	}
	id, herr := object.HexToID(req.SourceSnapshot)
	if herr != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid source_snapshot: %w", herr))
		return
	}
	src := rr.Source{Snapshot: id}
	tk := tokenFrom(r.Context())
	created, err := svc.CreateScoped(src, req.TargetBranch, req.Path, req.Title, req.Body, userID(tk))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, rrView(created))
}

// handleRRObjects accepts a bundle carrying only objects + snapshots (NO refs)
// and applies it to the repo. It is the propose-level upload channel for a
// request's source snapshot: a developer (not just an owner) can push the
// content of a request without the owner-only /push path.
func (s *Server) handleRRObjects(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	b, err := decodeBundleBody(r)
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeErr(w, http.StatusRequestEntityTooLarge, fmt.Errorf("request body exceeds limit (%d bytes)", mbe.Limit))
			return
		}
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	// A request upload must never move refs: reject any bundle that carries
	// ref changes so this channel cannot be used to bypass /push.
	if len(b.Refs) > 0 {
		writeErr(w, http.StatusBadRequest, errors.New("rr/objects must not carry refs"))
		return
	}
	n, err := transfer.Apply(repo, b)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"applied": n})
}

func (s *Server) handleRRList(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	list, err := s.cs.ListRevisionRequests(repo.NamespaceID(), r.URL.Query().Get("state"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, x := range list {
		out = append(out, rrView(x))
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": out})
}

func (s *Server) handleRRGet(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	iid, ok := rrIID(w, r)
	if !ok {
		return
	}
	x, err := s.cs.GetRevisionRequest(repo.NamespaceID(), iid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	reviews, err := s.cs.ListRRReviews(x.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	comments, err := s.cs.ListRRComments(x.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// merged_by (nil when the RR is not merged yet) — "who triggered the merge".
	var mergedBy *int64
	if pm, merr := s.cs.GetRRMerge(x.ID); merr == nil {
		mergedBy = pm.MergedBy
	}
	writeJSON(w, http.StatusOK, rrViewDetail(x, reviews, comments, mergedBy))
}

func (s *Server) handleRRDiff(w http.ResponseWriter, r *http.Request) {
	svc, repo, ok := s.rrService(w, r)
	if !ok {
		return
	}
	iid, ok := rrIID(w, r)
	if !ok {
		return
	}
	x, err := s.cs.GetRevisionRequest(repo.NamespaceID(), iid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	diffs, err := svc.Diff(x)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	out := make([]map[string]any, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, map[string]any{
			"path": d.Path, "status": string(d.Status),
			"added": d.AddedLines, "removed": d.RemovedLines, "content": d.Content,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": out})
}

func (s *Server) handleRRUpdate(w http.ResponseWriter, r *http.Request) {
	svc, repo, ok := s.rrService(w, r)
	if !ok {
		return
	}
	iid, ok := rrIID(w, r)
	if !ok {
		return
	}
	req, err := decodeJSONBody[rrCreateReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.SourceSnapshot == "" {
		writeErr(w, http.StatusBadRequest, errors.New("source_snapshot is required"))
		return
	}
	id, herr := object.HexToID(req.SourceSnapshot)
	if herr != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("invalid source_snapshot: %w", herr))
		return
	}
	x, err := s.cs.GetRevisionRequest(repo.NamespaceID(), iid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	tk := tokenFrom(r.Context())
	updated, err := svc.Update(x, id, userID(tk))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, rrView(updated))
}

// rrReviewReq is the review body.
type rrReviewReq struct {
	State string `json:"state"` // approved | request_changes | comment
	Body  string `json:"body"`
}

func (s *Server) handleRRReview(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	iid, ok := rrIID(w, r)
	if !ok {
		return
	}
	x, err := s.cs.GetRevisionRequest(repo.NamespaceID(), iid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	req, err := decodeJSONBody[rrReviewReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.State == "" {
		req.State = "comment"
	}
	tk := tokenFrom(r.Context())
	rv, err := s.cs.AddRRReview(x.ID, userID(tk), req.State, req.Body)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "review": rv.ID, "state": rv.State})
}

// rrCommentReq is the comment body.
type rrCommentReq struct {
	Body string `json:"body"`
	Path string `json:"path"`
}

func (s *Server) handleRRComment(w http.ResponseWriter, r *http.Request) {
	repo, ok := s.repo(w, r)
	if !ok {
		return
	}
	iid, ok := rrIID(w, r)
	if !ok {
		return
	}
	x, err := s.cs.GetRevisionRequest(repo.NamespaceID(), iid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	req, err := decodeJSONBody[rrCommentReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	tk := tokenFrom(r.Context())
	c, err := s.cs.AddRRComment(x.ID, userID(tk), req.Body, req.Path)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "comment": c.ID})
}

// rrResolveReq is the resolve body.
type rrResolveReq struct {
	Path string `json:"path"`
	Side int    `json:"side"`
}

func (s *Server) handleRRResolve(w http.ResponseWriter, r *http.Request) {
	svc, repo, ok := s.rrService(w, r)
	if !ok {
		return
	}
	iid, ok := rrIID(w, r)
	if !ok {
		return
	}
	x, err := s.cs.GetRevisionRequest(repo.NamespaceID(), iid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	req, err := decodeJSONBody[rrResolveReq](r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	updated, err := svc.Resolve(x, req.Path, req.Side, s.callerAuthor(tokenFrom(r.Context())), userID(tokenFrom(r.Context())))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, rrView(updated))
}

func (s *Server) handleRRMerge(w http.ResponseWriter, r *http.Request) {
	svc, repo, ok := s.rrService(w, r)
	if !ok {
		return
	}
	iid, ok := rrIID(w, r)
	if !ok {
		return
	}
	x, err := s.cs.GetRevisionRequest(repo.NamespaceID(), iid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	// Serialize pushes/merges per repo so the merge + ref move is atomic with
	// respect to concurrent writers (no TOCTOU), matching handlePush.
	tk := tokenFrom(r.Context())
	var res rr.MergeResult
	err = s.withRepoLock(repo, func() error {
		var merr error
		res, merr = svc.ApplyMerge(x, s.callerAuthor(tk), userID(tk))
		return merr
	})
	if err != nil {
		// A closed or already-merged request is a client error, not a server one.
		if errors.Is(err, rr.ErrNotMergeable) {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !res.Clean {
		writeJSON(w, http.StatusConflict, map[string]any{
			"error": "conflicted", "conflicts": res.Conflicts,
			"message": "the request has conflicts; resolve them and merge again",
		})
		return
	}
	s.auditOK(r, tk, fmt.Sprintf("rr-merge iid=%d", iid))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"merged_snapshot": res.MergedSnapshot.String(), "merged_revision": res.MergedRevisionID,
		"merged_by": userID(tk),
	})
}

// handleRRClose closes an RR without merging it (requireMerge: the same
// authority that may merge may also close). Idempotent for an already-closed
// request; a merged request is refused (it is terminal).
func (s *Server) handleRRClose(w http.ResponseWriter, r *http.Request) {
	svc, repo, ok := s.rrService(w, r)
	if !ok {
		return
	}
	iid, ok := rrIID(w, r)
	if !ok {
		return
	}
	x, err := s.cs.GetRevisionRequest(repo.NamespaceID(), iid)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	tk := tokenFrom(r.Context())
	updated, err := svc.Close(x, userID(tk))
	if err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	s.auditOK(r, tk, fmt.Sprintf("rr-close iid=%d", iid))
	writeJSON(w, http.StatusOK, rrView(updated))
}

// rrView renders an RR for JSON.
func rrView(x *store.RevisionRequest) map[string]any {
	v := map[string]any{
		"iid":             x.IID,
		"source_snapshot": x.SourceSnapshot,
		"base_snapshot":   x.BaseSnapshot,
		"target_branch":   x.TargetBranch,
		"state":           x.State,
		"title":           x.Title, "body": x.Body,
		"created": x.Created.UnixMilli(), "updated": x.Updated.UnixMilli(),
	}
	if x.Path != "" {
		v["path"] = x.Path // the scope this RR is limited to
	}
	return v
}

// rrViewDetail is rrView plus the RR's reviews and comments, which the detail
// endpoint (GET .../rr/{iid}) returns so a reviewer's verdict and the discussion
// are actually readable back (the list endpoint omits them to avoid N+1). It also
// surfaces merged_by (the user who triggered the merge; nil when not merged).
func rrViewDetail(x *store.RevisionRequest, reviews []*store.RRReview, comments []*store.RRComment, mergedBy *int64) map[string]any {
	out := rrView(x)
	rv := make([]map[string]any, 0, len(reviews))
	for _, r := range reviews {
		rv = append(rv, map[string]any{
			"id": r.ID, "state": r.State, "body": r.Body,
			"reviewer_id": r.ReviewerID, "created": r.Created.UnixMilli(),
		})
	}
	cm := make([]map[string]any, 0, len(comments))
	for _, c := range comments {
		cm = append(cm, map[string]any{
			"id": c.ID, "body": c.Body, "path": c.Path,
			"author_id": c.AuthorID, "created": c.Created.UnixMilli(),
		})
	}
	out["reviews"] = rv
	out["comments"] = cm
	out["merged_by"] = mergedBy
	return out
}
