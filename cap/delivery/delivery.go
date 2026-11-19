package delivery

import (
	"context"
	"time"
)

type Submission struct {
	Tenant, TaskID, ID, RoundID, ExecutionID, CommitID, Author string
	BasisVersion                                               int64
	CreatedAt                                                  time.Time
}
type Reader interface {
	CurrentSubmission(ctx context.Context, tenant, taskID, roundID string) (Submission, error)
	GetSubmission(ctx context.Context, tenant, id string) (Submission, error)
}
type Store interface {
	Reader
	RegisterSubmission(ctx context.Context, value Submission) error
}
