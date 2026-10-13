package store

import (
	"time"
)

// rr_reviews table: a reviewer's verdict on a Revision Request. Reviews are
// append-only (a new verdict is a new row); the RR's current review history is
// the ordered list.
type rrReviewRow struct {
	ID         int64 `gorm:"primaryKey;autoIncrement"`
	RRID       int64 `gorm:"column:rr_id;not null;index"`
	ReviewerID *int64
	State      string `gorm:"not null"`
	Body       string `gorm:"not null;default:''"`
	Created    int64  `gorm:"not null"`
}

func (rrReviewRow) TableName() string { return "rr_reviews" }

// rr_comments table: a discussion line on a Revision Request (optionally
// anchored to a path).
type rrCommentRow struct {
	ID       int64 `gorm:"primaryKey;autoIncrement"`
	RRID     int64 `gorm:"column:rr_id;not null;index"`
	AuthorID *int64
	Body     string `gorm:"not null"`
	Path     *string
	Created  int64 `gorm:"not null"`
}

func (rrCommentRow) TableName() string { return "rr_comments" }

// RRReview is a reviewer's verdict on a Revision Request.
type RRReview struct {
	ID         int64
	RRID       int64
	ReviewerID *int64
	State      string // "approved" | "request_changes" | "comment"
	Body       string
	Created    time.Time
}

// RRComment is a discussion line on a Revision Request.
type RRComment struct {
	ID       int64
	RRID     int64
	AuthorID *int64
	Body     string
	Path     string
	Created  time.Time
}

// AddRRReview appends a review to a Revision Request.
func (s *CentralStore) AddRRReview(rrID int64, reviewerID *int64, state, body string) (*RRReview, error) {
	now := time.Now().UTC().UnixMilli()
	row := &rrReviewRow{RRID: rrID, ReviewerID: reviewerID, State: state, Body: body, Created: now}
	if err := s.d.gdb.Create(row).Error; err != nil {
		return nil, err
	}
	return &RRReview{ID: row.ID, RRID: rrID, ReviewerID: reviewerID, State: state, Body: body, Created: time.UnixMilli(now)}, nil
}

// ListRRReviews returns a Revision Request's reviews, oldest first.
func (s *CentralStore) ListRRReviews(rrID int64) ([]*RRReview, error) {
	var rows []rrReviewRow
	if err := s.d.gdb.Where("rr_id=?", rrID).Order("created").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*RRReview, 0, len(rows))
	for i := range rows {
		out = append(out, &RRReview{
			ID: rows[i].ID, RRID: rows[i].RRID, ReviewerID: rows[i].ReviewerID,
			State: rows[i].State, Body: rows[i].Body, Created: time.UnixMilli(rows[i].Created),
		})
	}
	return out, nil
}

// AddRRComment appends a comment to a Revision Request.
func (s *CentralStore) AddRRComment(rrID int64, authorID *int64, body, path string) (*RRComment, error) {
	now := time.Now().UTC().UnixMilli()
	row := &rrCommentRow{RRID: rrID, AuthorID: authorID, Body: body, Path: strPtr(path), Created: now}
	if err := s.d.gdb.Create(row).Error; err != nil {
		return nil, err
	}
	return &RRComment{ID: row.ID, RRID: rrID, AuthorID: authorID, Body: body, Path: path, Created: time.UnixMilli(now)}, nil
}

// ListRRComments returns a Revision Request's comments, oldest first.
func (s *CentralStore) ListRRComments(rrID int64) ([]*RRComment, error) {
	var rows []rrCommentRow
	if err := s.d.gdb.Where("rr_id=?", rrID).Order("created").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*RRComment, 0, len(rows))
	for i := range rows {
		path := ""
		if rows[i].Path != nil {
			path = *rows[i].Path
		}
		out = append(out, &RRComment{
			ID: rows[i].ID, RRID: rows[i].RRID, AuthorID: rows[i].AuthorID,
			Body: rows[i].Body, Path: path, Created: time.UnixMilli(rows[i].Created),
		})
	}
	return out, nil
}

// strPtr returns a pointer to s, or nil when s is empty (so an absent optional
// field is stored as NULL rather than "").
func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
