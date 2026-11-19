// Package task defines task facts independently of timeline storage.
package task

import "context"

type Task struct {
	Tenant, ID, Title, Goal, Criteria, Owner, ProjectID string
	// BasisVersion is the task's only version: it changes with title, goal or
	// criteria. Executions, results, reviews and edits are pinned to it.
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

// Store is the task use-case consumer's cohesive read/write capability.
type Store interface {
	SetProject(ctx context.Context, tenant, taskID, projectID string) (Task, error)
	Lister
	CreateTask(ctx context.Context, value Task) error
	UpdateBasis(ctx context.Context, change BasisChange) (Task, error)
}
