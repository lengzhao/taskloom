package delivery

import (
	"context"
	"time"
)

type Submission struct {
	Tenant, TaskID, ID, RoundID, ExecutionID, CommitID, Author string
	Generation, BasisVersion                                   int64
	Withdrawn                                                  bool
	CreatedAt                                                  time.Time
}
type Reader interface {
	CurrentSubmission(ctx context.Context, tenant, taskID, roundID string) (Submission, error)
	GetSubmission(ctx context.Context, tenant, id string) (Submission, error)
}
type Store interface {
	Reader
	RegisterSubmission(ctx context.Context, value Submission) error
	WithdrawSubmission(ctx context.Context, tenant, id string) error
}
