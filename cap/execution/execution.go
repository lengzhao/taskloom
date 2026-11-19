package execution

import (
	"context"
	"time"

	"github.com/lengzhao/taskloom/cap/identity"
)

type Execution struct {
	Tenant, TaskID, ID, RoundID, Subject, InstanceID, Incarnation string
	BasisVersion                                                  int64
	// ProgressAt is when the executor last recorded progress (open, comment,
	// question or result). It is informational: nothing expires on it.
	ProgressAt time.Time
	// Writable is the only lifecycle fact Taskloom keeps: reassignment, a new
	// claim, return, reopen or cancel fence it at once. Whether an external
	// process still runs is unobservable here and is the Agent layer's concern.
	Writable bool
	// Manual means human work, without an Agent instance binding.
	Manual bool
}
type Reader interface {
	Assignee(ctx context.Context, tenant, taskID string) (string, error)
	Current(ctx context.Context, tenant, taskID string) (Execution, error)
	GetExecution(ctx context.Context, tenant, id string) (Execution, error)
}
type Store interface {
	AssignSubject(ctx context.Context, tenant, taskID, subject string) error
	Reader
	OpenExecution(ctx context.Context, value Execution) (Execution, error)
	SaveExecution(ctx context.Context, value Execution) error
}

// ResultAccess lets submission record progress on its execution in the same transaction.
type ResultAccess interface {
	Reader
	SaveExecution(ctx context.Context, value Execution) error
}

// Progress stamps the caller's own writable execution with the time it
// recorded progress (a comment or question) on the task, inside the caller's
// transaction. It is a no-op for anyone else and for fenced executions.
type Progress interface {
	RecordProgress(ctx context.Context, p identity.Principal, tenant, taskID string) error
}
