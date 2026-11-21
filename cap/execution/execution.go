package execution

import (
	"context"
	"time"

	"github.com/lengzhao/taskloom/cap/identity"
)

type Execution struct {
	Tenant, TaskID, ID, RoundID, Subject string
	// CredentialID binds an Agent execution to the credential that claimed it;
	// a different credential of the same Agent is a different instance.
	// CredentialID is empty exactly for human work, which binds no instance.
	CredentialID string
	BasisVersion int64
	// ProgressAt is when the executor last recorded progress (open, comment,
	// question or result). It is informational: nothing expires on it.
	ProgressAt time.Time
	// Writable is the only lifecycle fact Taskloom keeps: reassignment, a new
	// claim, return, reopen or cancel fence it at once. Whether an external
	// process still runs is unobservable here and is the Agent layer's concern.
	Writable bool
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
	// CredentialBound reports whether any execution of the task, current or
	// fenced, was bound to the credential: the only way Taskloom recognizes a
	// write from an old Agent instance.
	CredentialBound(ctx context.Context, tenant, taskID, credentialID string) (bool, error)
}

// ResultAccess lets result submission record progress on its execution in the same transaction.
type ResultAccess interface {
	Reader
	SaveExecution(ctx context.Context, value Execution) error
}

// Progress is called inside the transaction of a comment or question on the
// task. It stamps the caller's own writable execution with the time, and
// refuses with fault.ErrConflict a write from an Agent instance (credential)
// that executed the task but no longer holds a writable execution: a fenced
// execution may not comment or ask. People, and Agent instances that never
// executed the task, write as participants and are unaffected.
type Progress interface {
	RecordProgress(ctx context.Context, p identity.Principal, tenant, taskID string) error
}

// Executor is what a task command needs from the caller's own execution of
// that task, inside the command's transaction.
type Executor interface {
	Progress
	// Hold returns p's own writable execution of the task on its current round
	// and basis while the task is active, or fault.ErrConflict.
	Hold(ctx context.Context, p identity.Principal, tenant, taskID string) (Execution, error)
	// Follow pins a held execution to the task's current basis and records
	// progress. It is called after the executor's own change to the task, such
	// as a required subtask it created, advanced the basis: the author of a
	// change already knows it, so its execution is not fenced by it.
	Follow(ctx context.Context, v Execution) error
}
