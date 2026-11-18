package flow

import (
	"context"

	"github.com/lengzhao/taskloom/cap/task"
)

type DefinitionRef struct{ ID, Version, Digest string }
type Position struct {
	Tenant, TaskID, RoundID, EntryID, State, Waiting string
	Flow                                             DefinitionRef
	Complete                                         bool
}
type DefinitionStore interface {
	EnsureDefinition(ctx context.Context, ref DefinitionRef) error
}
type PositionStore interface {
	CreatePosition(ctx context.Context, value Position) error
	GetPosition(ctx context.Context, tenant, taskID string) (Position, error)
	SavePosition(ctx context.Context, value Position) error
}
type Description struct{ Key, Version string }
type Status string

const (
	Allow  Status = "allow"
	Wait   Status = "wait"
	Reject Status = "reject"
)

type Check struct {
	Status Status
	Reason string
}
type State interface {
	Describe() Description
	CheckEnter(ctx context.Context, value task.Task) (Check, error)
	Enter(ctx context.Context, value task.Task) error
	CheckComplete(ctx context.Context, value task.Task) (Check, error)
}
type Controller interface {
	Initialize(ctx context.Context, value task.Task) (Position, error)
	Refresh(ctx context.Context, value task.Task) (Position, error)
	Read(ctx context.Context, tenant, taskID string) (Position, error)
}
