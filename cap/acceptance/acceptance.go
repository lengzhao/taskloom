package acceptance

import (
	"context"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
)

type Review struct {
	Tenant, TaskID, ID, RoundID, SubmissionID, CommitID, Reviewer, Outcome, Reason string
	BasisVersion, PolicyVersion                                                    int64
}
type Reader interface {
	// A flow reaches the accepting step at most once per round, so a review is
	// identified by its round and submission.
	GetReview(ctx context.Context, tenant, taskID, roundID, submissionID string) (Review, error)
}
type Records interface {
	Reader
	EnsureReview(ctx context.Context, value Review) error
	SaveVerdict(ctx context.Context, value Review) error
}
type Verdict struct{ SubmissionID, CommitID, Outcome, Reason string }

// Actions is supplied by the accepting State, never by a parallel review engine.
type Actions interface {
	// Describe identifies the reached flow step that owns these actions.
	Describe() flow.Description
	RecordVerdict(ctx context.Context, current flow.Context, actor identity.Principal, value Verdict) error
}
