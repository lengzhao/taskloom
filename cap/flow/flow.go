// Package flow defines a task's lifecycle. Only the phase, the round and
// whether the task needs human acceptance are stored; the current step and
// what it waits on are derived from task facts on every read, so they can
// never drift from those facts.
package flow

import (
	"context"

	"github.com/lengzhao/taskloom/cap/task"
)

// Phase is the task-level lifecycle owned by the flow controller.
type Phase string

const (
	PhaseActive    Phase = "active"
	PhaseCancelled Phase = "cancelled"
	PhaseCompleted Phase = "completed"
)

// Step is derived from facts, never stored.
type Step string

const (
	// StepReady: the goal or criteria are still missing; work cannot be claimed.
	StepReady Step = "ready"
	// StepExecuting: no valid result for the current basis yet.
	StepExecuting Step = "executing"
	// StepAccepting: a valid result waits for the owner's approval.
	StepAccepting Step = "accepting"
	// StepCompleted: the task is completed.
	StepCompleted Step = "completed"
)

// Acceptance is fixed per task at creation; deployment changes never alter it.
type Acceptance string

const (
	AcceptanceManual Acceptance = "manual"
	AcceptanceNone   Acceptance = "none"
)

// Position is a task's place in its lifecycle. Revision, Status, RoundID and
// Acceptance are stored; State and Waiting are derived on read.
type Position struct {
	// Revision changes only when the stored phase or round changes.
	Revision                int64
	Status                  Phase
	Tenant, TaskID, RoundID string
	Acceptance              Acceptance
	State                   Step
	Waiting                 string
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

// Controller is the only writer of phase and round. Every method runs inside
// the caller's command transaction.
type Controller interface {
	Initialize(ctx context.Context, value task.Task) (Position, error)
	// Read returns the stored position with its derived step.
	Read(ctx context.Context, value task.Task) (Position, error)
	// Refresh completes the task once its facts satisfy completion.
	Refresh(ctx context.Context, value task.Task) (Position, error)
	// CheckEdit allows basis changes while no live claim of this round exists
	// and no valid result waits for acceptance.
	CheckEdit(ctx context.Context, value task.Task) error
	// CheckOpen allows assignment and claims on an active task with a complete basis.
	CheckOpen(ctx context.Context, value task.Task) (Position, error)
	Cancel(ctx context.Context, value task.Task) (Position, error)
	// Return starts a new round of an active task; Reopen of a terminal one.
	Return(ctx context.Context, value task.Task) (Position, error)
	Reopen(ctx context.Context, value task.Task) (Position, error)
}
