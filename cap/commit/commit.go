// Package commit defines immutable task timeline facts, not command dispatch.
package commit

import (
	"context"
	"time"
)

// Tenant is not serialized: stores and callers already know it from the
// task key and authentication. OwnerID is set only for an Agent's commit.
type Commit struct {
	Tenant             string `json:"-"`
	TaskID, ID, Author string
	OwnerID            string `json:",omitempty"`
	Kind, Body, RefID  string
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

// Reader is read-only timeline access: lookups, the newest commit (a task list
// shows when a task's timeline last changed; Last returns fault.ErrNotFound
// for an empty timeline) and pages read from the newest end.
type Reader interface {
	Lookup
	Last(ctx context.Context, tenant, taskID string) (Commit, error)
	// Before returns up to limit commits with a sequence below before (0 is
	// the newest end), oldest first, so a page reads top to bottom.
	Before(ctx context.Context, tenant, taskID string, before int64, limit int) ([]Commit, error)
}
type Log interface {
	Lookup
	Append(ctx context.Context, value Commit) error
	List(ctx context.Context, tenant, taskID string, after int64, limit int) ([]Commit, error)
}
