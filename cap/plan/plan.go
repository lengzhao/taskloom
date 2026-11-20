// Package plan defines the current work breakdown: which task a task belongs
// to and whether its parent's delivery needs it. Only the current relation is
// stored; history lives in the parent's commits.
package plan

import (
	"context"

	"github.com/lengzhao/taskloom/cap/identity"
)

// Link places a child under its one parent. Required means the parent cannot
// deliver until the child is completed.
type Link struct {
	Tenant, ParentID, ChildID string
	Required                  bool
}

// Reader is the read-only relation view used by completion and queries.
type Reader interface {
	// Parent returns the child's link, or fault.ErrNotFound for a root task.
	Parent(ctx context.Context, tenant, childID string) (Link, error)
	// Children lists the direct children of a task in a stable order.
	Children(ctx context.Context, tenant, parentID string) ([]Link, error)
}

// Store adds the writes of the plan command.
type Store interface {
	Reader
	// SetParent places or moves the child; a child has at most one parent.
	SetParent(ctx context.Context, value Link) error
}

// KindChanged is the commit kind recording a relation change on each parent it touches.
const KindChanged = "plan.changed"

// Placement sets where a task sits in the work breakdown. It pins only its
// own object, the child's current relation as the caller saw it; unrelated
// changes to a parent, such as another subtask or a new title, do not conflict.
type Placement struct {
	ChildID string
	// ParentID is the parent to place the child under. There is no detach: a
	// subtask no longer needed is made optional or cancelled, and history stays.
	ParentID string
	Required bool
	// SeenParentID and SeenRequired are the relation the caller read; an
	// empty SeenParentID means the caller saw a root task.
	SeenParentID string
	SeenRequired bool
}

// Placer applies a placement inside the caller's command transaction, so
// creating a task under a parent is one atomic command. The caller has
// authenticated and authorized the actor; Placer checks the relation facts
// and writes the relation, parent basis and commits, and change events.
type Placer interface {
	Apply(ctx context.Context, actor identity.Principal, in Placement) error
}

// Commands changes the plan. Attaching, toggling Required and moving are one
// named use case: placing a child.
type Commands interface {
	Place(ctx context.Context, token, key string, in Placement) error
}
