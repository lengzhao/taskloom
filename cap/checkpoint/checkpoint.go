package checkpoint

import (
	"context"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
)

type Record struct {
	Tenant, TaskID, RoundID, EntryID, SubmissionID, CommitID, Actor, Note string
	BasisVersion                                                          int64
	Confirmed                                                             bool
}
type Records interface {
	EnsureCheckpoint(ctx context.Context, value Record) error
	ReadCheckpoint(ctx context.Context, value Record) (Record, error)
	ConfirmCheckpoint(ctx context.Context, value Record) error
}
type Input struct{ SubmissionID, CommitID, Note string }
type Actions interface {
	StepKey() string
	Confirm(ctx context.Context, current flow.Context, actor identity.Principal, value Input) error
}
