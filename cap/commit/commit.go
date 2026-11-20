// Package commit defines immutable task timeline facts, not command dispatch.
package commit

import (
	"context"
	"time"
)

type Commit struct {
	Tenant, TaskID, ID, Author, OwnerID string
	Kind, Body, RefID                   string
	BasisVersion                        int64
	CreatedAt                           time.Time
	Sequence                            int64
}

// Append is internal: named domain commands authorize and validate kind/refs.
// There is intentionally no update, edit, delete or arbitrary Execute method.
type Lookup interface {
	Get(ctx context.Context, tenant, taskID, commitID string) (Commit, error)
}
// Reader adds the newest commit to lookups: a task list shows when a task's
// timeline last changed. Last returns fault.ErrNotFound for an empty timeline.
type Reader interface {
	Lookup
	Last(ctx context.Context, tenant, taskID string) (Commit, error)
}
type Log interface {
	Lookup
	Append(ctx context.Context, value Commit) error
	List(ctx context.Context, tenant, taskID string, after int64, limit int) ([]Commit, error)
}
