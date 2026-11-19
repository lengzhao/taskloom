package flow

import (
	"context"

	"github.com/lengzhao/taskloom/cap/task"
)

type DefinitionRef struct{ ID, Version, Digest string }

// Phase is the task-level lifecycle owned by the flow controller.
type Phase string

const (
	PhaseActive    Phase = "active"
	PhaseCancelled Phase = "cancelled"
	PhaseCompleted Phase = "completed"
)

// Role is the fixed contract a State declares to the controller. Only these
// roles carry controller semantics; any other step leaves Role empty.
//   - RoleReady must be first in a multi-step flow; task edits are allowed there.
//   - RoleExecuting is the only step where OpenExecution may be checked.
//   - RoleCompleted must be last; completion checks results, acceptance and open questions only.
//   - RoleAccepting marks the human acceptance step (informational).
type Role string

const (
	RoleReady     Role = "ready"
	RoleExecuting Role = "executing"
	RoleAccepting Role = "accepting"
	RoleCompleted Role = "completed"
)

// Position is a task's place in its bound flow. Steps run in order and only
// move forward within a round, so the reached steps are the definition's first
// Index+1 states and each is entered at most once per RoundID.
type Position struct {
	// Revision changes only when the stored position actually changes.
	Revision                                int64
	Status                                  Phase
	Index                                   int
	Tenant, TaskID, RoundID, State, Waiting string
	Flow                                    DefinitionRef
	Complete                                bool
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
	// SavePosition fails with fault.ErrConflict unless the stored revision equals expected.
	SavePosition(ctx context.Context, value Position, expected int64) error
}
type Description struct {
	Key, Version string
	Role         Role `json:",omitempty"`
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
	// Step returns the State context of an already reached step with this key in
	// the current active round, so step-owned actions never rebuild positions.
	Step(ctx context.Context, value task.Task, key string) (Context, error)
}
