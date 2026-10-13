package store

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// RR states.
const (
	RROpen       = "open"
	RRStale      = "stale"
	RRConflicted = "conflicted"
	RRMerged     = "merged"
	RRClosed     = "closed"
)

// RevisionRequest is the change-native collaboration unit. Its subject is a
// pinned, content-addressed snapshot (SourceSnapshot) plus the target tip it was
// opened against (BaseSnapshot). Both are immutable: a snapshot never changes,
// so the review never drifts; the target branch moving only means the merge is
// computed against the CURRENT tip. There is exactly one source form — a
// snapshot — so there is no source kind and no source revision identity.
type RevisionRequest struct {
	ID             int64
	RepoID         int64
	IID            int64
	SourceSnapshot string
	BaseSnapshot   string
	TargetBranch   string
	State          string
	Title          string
	Body           string
	AuthorID       *int64
	Created        time.Time
	Updated        time.Time
}

// RRUpdate records one explicit re-pin of a Revision Request.
type RRUpdate struct {
	ID             int64
	RRID           int64
	Seq            int64
	SourceSnapshot string
	BaseSnapshot   string
	AuthorID       *int64
	Created        time.Time
}

// RRMerge records the outcome of applying a Revision Request. MergedBy is the
// user id that triggered the merge (nil for an anonymous / open-instance merge);
// the landing commit's AUTHOR is the source snapshot's author, so MergedBy is
// where "who merged it" is kept.
type RRMerge struct {
	RRID             int64
	MergedSnapshot   string
	MergedRevisionID string
	State            string
	MergedBy         *int64
	Created          time.Time
	Updated          time.Time
}

// CreateRevisionRequest inserts a new RR, assigning the next per-repo IID. The
// caller supplies the already-pinned source/base snapshots.
func (s *CentralStore) CreateRevisionRequest(repoID int64, rr *RevisionRequest) (*RevisionRequest, error) {
	now := time.Now().UTC().UnixMilli()
	var maxIID *int64
	if err := s.d.gdb.Model(&revisionRequestRow{}).Where("repo_id=?", repoID).Select("COALESCE(MAX(iid),0)+1").Scan(&maxIID).Error; err != nil {
		return nil, fmt.Errorf("assign RR iid: %w", err)
	}
	iid := int64(1)
	if maxIID != nil {
		iid = *maxIID
	}
	if rr.SourceSnapshot == "" {
		return nil, fmt.Errorf("create revision request: source snapshot is required")
	}
	state := rr.State
	if state == "" {
		state = RROpen
	}
	target := rr.TargetBranch
	if target == "" {
		target = "main"
	}
	row := &revisionRequestRow{
		RepoID: repoID, IID: iid,
		SourceSnapshot: rr.SourceSnapshot,
		BaseSnapshot:   rr.BaseSnapshot, TargetBranch: target, State: state,
		Title: rr.Title, Body: rr.Body, AuthorID: rr.AuthorID, Created: now, Updated: now,
	}
	if err := s.d.gdb.Create(row).Error; err != nil {
		return nil, err
	}
	out := *rr
	out.ID = row.ID
	out.RepoID = repoID
	out.IID = iid
	out.State = state
	out.TargetBranch = target
	out.Created = time.UnixMilli(now)
	out.Updated = time.UnixMilli(now)
	return &out, nil
}

// GetRevisionRequest returns an RR by repo + iid.
func (s *CentralStore) GetRevisionRequest(repoID, iid int64) (*RevisionRequest, error) {
	var row revisionRequestRow
	err := s.d.gdb.Where("repo_id=? AND iid=?", repoID, iid).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return rrFromRow(&row), nil
}

// ListRevisionRequests lists a repo's RRs, optionally filtered by state.
func (s *CentralStore) ListRevisionRequests(repoID int64, state string) ([]*RevisionRequest, error) {
	q := s.d.gdb.Where("repo_id=?", repoID)
	// "" and "all" both mean "no state filter"; any other value filters by it.
	if state != "" && state != "all" {
		q = q.Where("state=?", state)
	}
	var rows []revisionRequestRow
	if err := q.Order("iid DESC").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*RevisionRequest, 0, len(rows))
	for i := range rows {
		out = append(out, rrFromRow(&rows[i]))
	}
	return out, nil
}

// UpdateRevisionRequestState sets an RR's state (open/stale/conflicted/merged/closed).
func (s *CentralStore) UpdateRevisionRequestState(repoID, iid int64, state string) error {
	return s.d.gdb.Model(&revisionRequestRow{}).Where("repo_id=? AND iid=?", repoID, iid).Updates(map[string]any{
		"state": state, "updated": time.Now().UTC().UnixMilli(),
	}).Error
}

// UpdateRevisionRequestPin re-pins an RR's source/base snapshots (the explicit
// update path) and resets its state to open.
func (s *CentralStore) UpdateRevisionRequestPin(repoID, iid int64, sourceSnapshot, baseSnapshot string) error {
	return s.d.gdb.Model(&revisionRequestRow{}).Where("repo_id=? AND iid=?", repoID, iid).Updates(map[string]any{
		"source_snapshot": sourceSnapshot, "base_snapshot": baseSnapshot,
		"state": RROpen, "updated": time.Now().UTC().UnixMilli(),
	}).Error
}

// AddRRUpdate appends one explicit re-pin record and returns it.
func (s *CentralStore) AddRRUpdate(rrID int64, sourceSnapshot, baseSnapshot string, authorID *int64) (*RRUpdate, error) {
	now := time.Now().UTC().UnixMilli()
	var maxSeq *int64
	if err := s.d.gdb.Model(&rrUpdateRow{}).Where("rr_id=?", rrID).Select("COALESCE(MAX(seq),0)+1").Scan(&maxSeq).Error; err != nil {
		return nil, fmt.Errorf("assign rr_update seq: %w", err)
	}
	seq := int64(1)
	if maxSeq != nil {
		seq = *maxSeq
	}
	row := &rrUpdateRow{RRID: rrID, Seq: seq, SourceSnapshot: sourceSnapshot, BaseSnapshot: baseSnapshot, AuthorID: authorID, Created: now}
	if err := s.d.gdb.Create(row).Error; err != nil {
		return nil, err
	}
	return &RRUpdate{ID: row.ID, RRID: rrID, Seq: seq, SourceSnapshot: sourceSnapshot, BaseSnapshot: baseSnapshot, AuthorID: authorID, Created: time.UnixMilli(now)}, nil
}

// ListRRUpdates returns an RR's update history, oldest first.
func (s *CentralStore) ListRRUpdates(rrID int64) ([]*RRUpdate, error) {
	var rows []rrUpdateRow
	if err := s.d.gdb.Where("rr_id=?", rrID).Order("seq").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*RRUpdate, 0, len(rows))
	for i := range rows {
		out = append(out, &RRUpdate{
			ID: rows[i].ID, RRID: rows[i].RRID, Seq: rows[i].Seq,
			SourceSnapshot: rows[i].SourceSnapshot, BaseSnapshot: rows[i].BaseSnapshot,
			AuthorID: rows[i].AuthorID, Created: time.UnixMilli(rows[i].Created),
		})
	}
	return out, nil
}

// PutRRMerge upserts the merge outcome for an RR. mergedBy is the user id that
// triggered the merge (nil for anonymous / open-instance merges).
func (s *CentralStore) PutRRMerge(rrID int64, mergedSnapshot, mergedRevisionID, state string, mergedBy *int64) error {
	now := time.Now().UTC().UnixMilli()
	row := &rrMergeRow{RRID: rrID, MergedSnapshot: mergedSnapshot, MergedRevisionID: mergedRevisionID, State: state, MergedBy: mergedBy, Created: now, Updated: now}
	return s.d.gdb.Clauses(gormConflictUpdateAll()).Create(row).Error
}

// GetRRMerge returns an RR's merge outcome (ErrNotFound when absent).
func (s *CentralStore) GetRRMerge(rrID int64) (*RRMerge, error) {
	var row rrMergeRow
	err := s.d.gdb.Where("rr_id=?", rrID).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &RRMerge{
		RRID: row.RRID, MergedSnapshot: row.MergedSnapshot, MergedRevisionID: row.MergedRevisionID,
		State: row.State, MergedBy: row.MergedBy, Created: time.UnixMilli(row.Created), Updated: time.UnixMilli(row.Updated),
	}, nil
}

// ListOpenRRSnapshots returns every pinned snapshot (source + base) of the RRs
// whose state is open/stale/conflicted across ALL repositories. It is the GC
// root set addition: a pinned snapshot of a live RR must never be pruned even
// when no branch points at it.
func (s *CentralStore) ListOpenRRSnapshots() ([]string, error) {
	var rows []revisionRequestRow
	if err := s.d.gdb.Where("state IN ?", []string{RROpen, RRStale, RRConflicted}).Find(&rows).Error; err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, r := range rows {
		for _, s := range []string{r.SourceSnapshot, r.BaseSnapshot} {
			if s != "" && !seen[s] {
				seen[s] = true
				out = append(out, s)
			}
		}
	}
	return out, nil
}

// RepoIDForRR returns the repo id owning an RR (for cross-repo GC resolution).
func (s *CentralStore) RepoIDForRR(rrID int64) (int64, error) {
	var row revisionRequestRow
	if err := s.d.gdb.Select("repo_id").First(&row, rrID).Error; err != nil {
		return 0, err
	}
	return row.RepoID, nil
}

func rrFromRow(row *revisionRequestRow) *RevisionRequest {
	return &RevisionRequest{
		ID: row.ID, RepoID: row.RepoID, IID: row.IID,
		SourceSnapshot: row.SourceSnapshot,
		BaseSnapshot:   row.BaseSnapshot, TargetBranch: row.TargetBranch, State: row.State,
		Title: row.Title, Body: row.Body, AuthorID: row.AuthorID,
		Created: time.UnixMilli(row.Created), Updated: time.UnixMilli(row.Updated),
	}
}
