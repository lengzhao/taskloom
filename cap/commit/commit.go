// Package commit defines immutable task timeline facts, not command dispatch.
package commit

import (
	"context"
	"time"
)

type Commit struct {
	Tenant, TaskID, ID, Author, OwnerID, InstanceID, Incarnation string
	Kind, Body, RefID                                            string
	BasisVersion                                                 int64
	CreatedAt                                                    time.Time
	Sequence                                                     int64
}

// Append is internal: named domain commands authorize and validate kind/refs.
// There is intentionally no update, edit, delete or arbitrary Execute method.
type Log interface {
	Append(ctx context.Context, value Commit) error
	Get(ctx context.Context, tenant, taskID, commitID string) (Commit, error)
	List(ctx context.Context, tenant, taskID string, after int64, limit int) ([]Commit, error)
}
