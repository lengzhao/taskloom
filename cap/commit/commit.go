// Package commit defines immutable task timeline facts, not command dispatch.
package commit

import (
	"context"
	"time"
)

type Commit struct {
	Tenant, TaskID, ID, Author, OwnerID string
	Kind, Body, RefID                   string
	// Files are file versions of the same task (see cap/file), kept as long as
	// this commit; Links are external URLs, not snapshots of their pages.
	Files        []string `json:",omitempty"`
	Links        []Link   `json:",omitempty"`
	BasisVersion int64
	CreatedAt    time.Time
	Sequence     int64
}

// Link is an external reference. Its target may change or vanish; Taskloom
// keeps only the URL and the title the author gave it.
type Link struct {
	URL   string
	Title string `json:",omitempty"`
}

// Append is internal: named domain commands authorize and validate kind/refs.
// It rejects files that are not registered on the same task or that the
// author may not see (another principal's unreferenced upload).
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
