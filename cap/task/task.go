// Package task defines task facts independently of timeline storage.
package task

import (
	"context"
	"errors"
	"sort"

	"github.com/lengzhao/taskloom/cap/fault"
)

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

// MaxPick bounds how many tasks one list request may name.
const MaxPick = 100

// Pick reads the named tasks, or every task of the tenant when ids is empty,
// in ID order. A named task that does not exist is left out, like one the
// caller may not read, so a list filtered by ID says nothing more than the
// full list would.
func Pick(ctx context.Context, l Lister, tenant string, ids []string) ([]Task, error) {
	if len(ids) == 0 {
		return l.ListTasks(ctx, tenant)
	}
	if len(ids) > MaxPick {
		return nil, fault.ErrInput
	}
	seen := map[string]bool{}
	out := []Task{}
	for _, id := range ids {
		if id == "" {
			return nil, fault.ErrInput
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		t, err := l.GetTask(ctx, tenant, id)
		if errors.Is(err, fault.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
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
