package execution

import (
	"context"
	"time"

	"github.com/lengzhao/taskloom/cap/identity"
)

type Execution struct {
	Tenant, TaskID, ID, RoundID, Subject, InstanceID, Incarnation string
	Generation, BasisVersion                                      int64
	Status, StopRequestID, EndEvidenceID                          string
	// ProgressAt is when the executor last recorded progress (open, comment,
	// question or result). It is informational: nothing expires on it.
	ProgressAt     time.Time
	Writable, Safe bool
	// Manual means human work, without a managed Agent runtime or lease.
	Manual bool
}
type Reader interface {
	Assignee(ctx context.Context, tenant, taskID string) (string, error)
	Current(ctx context.Context, tenant, taskID string) (Execution, error)
	GetExecution(ctx context.Context, tenant, id string) (Execution, error)
	SafeToExit(ctx context.Context, tenant, taskID string) (bool, error)
}
type Store interface {
	RecordStopEvidence(ctx context.Context, value StopEvidence) error
	AssignSubject(ctx context.Context, tenant, taskID, subject string) error
	Reader
	OpenExecution(ctx context.Context, value Execution) (Execution, error)
	SaveExecution(ctx context.Context, value Execution) error
}

type StopEvidence struct {
	Source, Reason, Scope                                                        string
	Tenant, TaskID, ID, ExecutionID, Challenge, Subject, InstanceID, Incarnation string
	Generation                                                                   int64
	MainStopped, ChildrenStopped, ExternalSettled                                bool
	ReceivedAt                                                                   time.Time
}

// ResultAccess lets submission mark a human's work ended in the same transaction.
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
