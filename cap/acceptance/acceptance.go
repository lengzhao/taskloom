package acceptance

import "context"

// Review is the owner's recorded verdict on one submission. A submission that
// needs acceptance and has no review yet is pending; that state is derived,
// not stored.
type Review struct {
	Tenant, TaskID, ID, RoundID, SubmissionID, CommitID, Reviewer, Outcome, Reason string
	BasisVersion                                                                   int64
}
type Reader interface {
	GetReview(ctx context.Context, tenant, taskID, roundID, submissionID string) (Review, error)
}
type Records interface {
	Reader
	// SaveReview records a verdict, replacing any earlier one for the same
	// submission; every verdict is also kept as history.
	SaveReview(ctx context.Context, value Review) error
}
type Verdict struct{ SubmissionID, CommitID, Outcome, Reason string }

const (
	OutcomePending  = "pending"
	OutcomeApproved = "approved"
	OutcomeRejected = "rejected"
)
