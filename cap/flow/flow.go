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
	// StepReady: the goal is still missing; work cannot be claimed. Criteria
	// are optional: without them the owner's acceptance, or for none the
	// handler's delivery, is the judgment.
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

// Position is a task's place in its lifecycle. Status, RoundID and Acceptance
// are stored; State, Waiting and WaitingOnSubtasks are derived on read.
type Position struct {
	Status          Phase
	Tenant          string `json:"-"`
	TaskID, RoundID string
	Acceptance      Acceptance
	State           Step
	Waiting         string
	// WaitingOnSubtasks: the active task cannot deliver because a direct required
	// subtask is not completed. It is a wait cause, not a step: work may still
	// be claimed, discussed and prepared; Waiting says why.
	WaitingOnSubtasks bool `json:",omitempty"`
}

type PositionStore interface {
	GetPosition(ctx context.Context, tenant, taskID string) (Position, error)
	CreatePosition(ctx context.Context, value Position) error
	// SavePosition replaces the stored position; writes are serialized by the
	// global write lock, so no compare-and-swap is needed.
	SavePosition(ctx context.Context, value Position) error
}

// Controller is the only writer of phase and round. Every method runs inside
// the caller's command transaction.
type Controller interface {
	// Initialize creates the position with the given acceptance mode, fixed
	// from then on; empty takes the deployment default, an unknown mode is
	// fault.ErrInput.
	Initialize(ctx context.Context, value task.Task, acceptance Acceptance) (Position, error)
	// Read returns the stored position with its derived step.
	Read(ctx context.Context, value task.Task) (Position, error)
	// Refresh completes the task once its facts satisfy completion.
	Refresh(ctx context.Context, value task.Task) (Position, error)
	// CheckOpen allows assignment and claims on an active task with a complete basis.
	CheckOpen(ctx context.Context, value task.Task) (Position, error)
	// CheckSubmit allows a result once every direct required subtask is
	// completed; otherwise it returns fault.ErrConflict.
	CheckSubmit(ctx context.Context, value task.Task) error
	// Parent names the direct parent whose derived view changes with this
	// task, or "" for a root task. Commands use it to announce the change.
	Parent(ctx context.Context, value task.Task) (string, error)
	Cancel(ctx context.Context, value task.Task) (Position, error)
	// Restart reopens a cancelled task in a new active round. An active task
	// rejects it (send a result back by review, a person by reassigning, a
	// requirement by editing the basis); a completed task is final.
	Restart(ctx context.Context, value task.Task) (Position, error)
}
