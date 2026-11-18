package flow

import (
	"context"

	"github.com/lengzhao/taskloom/cap/task"
)

type DefinitionRef struct{ ID, Version, Digest string }
type Entry struct{ Key, ID string }
type Position struct {
	Revision                                         int64
	Status                                           string
	Entries                                          []Entry
	Index                                            int
	Tenant, TaskID, RoundID, EntryID, State, Waiting string
	Flow                                             DefinitionRef
	Complete                                         bool
}
type DefinitionStore interface {
	EnsureDefinition(ctx context.Context, ref DefinitionRef) error
}
type PositionReader interface {
	GetPosition(ctx context.Context, tenant, taskID string) (Position, error)
}
type PositionStore interface {
	PositionReader
	CreatePosition(ctx context.Context, value Position) error
	SavePosition(ctx context.Context, value Position) error
}
type Description struct {
	Key, Version string
	Role         string `json:",omitempty"`
	Rules        string `json:",omitempty"`
}
type Context struct {
	Task     task.Task
	Position Position
}
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
	CheckEnter(ctx context.Context, value Context) (Check, error)
	Enter(ctx context.Context, value Context) error
	CheckComplete(ctx context.Context, value Context) (Check, error)
}
type Definition interface {
	Controller
	Reference() DefinitionRef
}
type Controller interface {
	CheckEdit(ctx context.Context, value task.Task) error
	CheckOpen(ctx context.Context, value task.Task) (Position, error)
	Cancel(ctx context.Context, value task.Task) (Position, error)
	Return(ctx context.Context, value task.Task) (Position, error)
	Reopen(ctx context.Context, value task.Task) (Position, error)
	Initialize(ctx context.Context, value task.Task) (Position, error)
	Refresh(ctx context.Context, value task.Task) (Position, error)
	Read(ctx context.Context, tenant, taskID string) (Position, error)
}
