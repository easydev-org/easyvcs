package store

import (
	"testing"
)

// TestRRReviewCommentRoundTrip verifies reviews and comments on a Revision
// Request round-trip through the store's own rr_reviews / rr_comments tables
// (no longer the legacy mr_* tables), oldest-first.
func TestRRReviewCommentRoundTrip(t *testing.T) {
	cs := newTestCentral(t)
	repo, err := cs.Create(NSRef{Name: "n-r"})
	if err != nil {
		t.Fatal(err)
	}
	rr, err := cs.CreateRevisionRequest(repo.NamespaceID(), &RevisionRequest{
		SourceSnapshot: "aa", BaseSnapshot: "bb", TargetBranch: "main", Title: "t",
	})
	if err != nil {
		t.Fatal(err)
	}

	reviewer := int64(7)
	if _, err := cs.AddRRReview(rr.ID, &reviewer, "approved", "lgtm"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.AddRRReview(rr.ID, nil, "comment", "a note"); err != nil {
		t.Fatal(err)
	}
	reviews, err := cs.ListRRReviews(rr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reviews) != 2 {
		t.Fatalf("reviews = %d, want 2", len(reviews))
	}
	if reviews[0].State != "approved" || reviews[0].Body != "lgtm" || reviews[0].ReviewerID == nil || *reviews[0].ReviewerID != 7 {
		t.Fatalf("review[0] = %+v", reviews[0])
	}
	if reviews[1].State != "comment" || reviews[1].ReviewerID != nil {
		t.Fatalf("review[1] = %+v", reviews[1])
	}

	author := int64(9)
	if _, err := cs.AddRRComment(rr.ID, &author, "nit", "a.go"); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.AddRRComment(rr.ID, &author, "general", ""); err != nil {
		t.Fatal(err)
	}
	comments, err := cs.ListRRComments(rr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(comments) != 2 {
		t.Fatalf("comments = %d, want 2", len(comments))
	}
	if comments[0].Body != "nit" || comments[0].Path != "a.go" {
		t.Fatalf("comment[0] = %+v", comments[0])
	}
	if comments[1].Body != "general" || comments[1].Path != "" {
		t.Fatalf("comment[1] = %+v", comments[1])
	}

	// Isolation: another RR has none.
	rr2, err := cs.CreateRevisionRequest(repo.NamespaceID(), &RevisionRequest{
		SourceSnapshot: "cc", BaseSnapshot: "dd", TargetBranch: "main", Title: "t2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := cs.ListRRReviews(rr2.ID); len(got) != 0 {
		t.Fatalf("rr2 reviews = %d, want 0", len(got))
	}
	if got, _ := cs.ListRRComments(rr2.ID); len(got) != 0 {
		t.Fatalf("rr2 comments = %d, want 0", len(got))
	}
}
