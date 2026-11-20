// Package task defines task facts independently of timeline storage.
package task

import "context"

type Task struct {
	Tenant, ID, Title, Goal, Criteria, Owner, ProjectID string
	// BasisVersion is the task's only delivery-basis version: it changes with
	// title, goal, criteria or the set of required subtasks. Executions,
	// results, reviews and edits are pinned to it.
	BasisVersion int64
}

// BasisChange contains only fields the named basis update may change.
type BasisChange struct {
	Tenant, TaskID, Title, Goal, Criteria string
}

// Reader is used by access checks without write authority.
type Reader interface {
	GetTask(ctx context.Context, tenant, taskID string) (Task, error)
}

// Lister adds tenant-wide listing for read-only aggregations.
type Lister interface {
	Reader
	ListTasks(ctx context.Context, tenant string) ([]Task, error)
}

// BasisAdvancer is used by the plan command: a change to the required
// subtasks alters what a delivery must satisfy without touching the text.
type BasisAdvancer interface {
	Reader
	AdvanceBasis(ctx context.Context, tenant, taskID string) (Task, error)
}

// Store is the task use-case consumer's cohesive read/write capability.
type Store interface {
	SetProject(ctx context.Context, tenant, taskID, projectID string) (Task, error)
	Lister
	CreateTask(ctx context.Context, value Task) error
	UpdateBasis(ctx context.Context, change BasisChange) (Task, error)
}
