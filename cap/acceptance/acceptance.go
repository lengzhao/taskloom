// Package acceptance describes the owner's verdict on a result. A verdict is
// stored once, as an immutable commit on the task timeline: Kind is
// KindApproved or KindRejected, Body is the reason, RefID is the result
// commit and Author is the reviewer. Review is the read view of that commit;
// a result awaiting acceptance without a verdict is pending, which is derived.
package acceptance

import "context"

const (
	KindApproved = "review.approved"
	KindRejected = "review.rejected"

	OutcomePending  = "pending"
	OutcomeApproved = "approved"
	OutcomeRejected = "rejected"
)

type Review struct {
	Tenant, TaskID, ID, RoundID, SubmissionID, CommitID, Reviewer, Outcome, Reason string
	BasisVersion                                                                   int64
}

// Reader finds the latest verdict commit referring to a result commit.
type Reader interface {
	LatestReview(ctx context.Context, tenant, taskID, resultCommitID string) (Review, error)
}
type Verdict struct{ SubmissionID, CommitID, Outcome, Reason string }

// Kind returns the commit kind recording an outcome.
func Kind(outcome string) string {
	if outcome == OutcomeApproved {
		return KindApproved
	}
	return KindRejected
}
