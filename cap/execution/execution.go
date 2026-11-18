package execution

import (
	"context"
	"time"
)

type Execution struct {
	Tenant, TaskID, ID, RoundID, Subject, InstanceID, Incarnation string
	Generation, BasisVersion                                      int64
	Status, StopRequestID, EndEvidenceID                          string
	LeaseUntil                                                    time.Time
	Writable, Safe                                                bool
}
type Reader interface {
	AssignedAgent(ctx context.Context, tenant, taskID string) (string, error)
	Current(ctx context.Context, tenant, taskID string) (Execution, error)
	GetExecution(ctx context.Context, tenant, id string) (Execution, error)
	SafeToExit(ctx context.Context, tenant, taskID string) (bool, error)
}
type Store interface {
	RecordStopEvidence(ctx context.Context, value StopEvidence) error
	AssignAgent(ctx context.Context, tenant, taskID, subject string) error
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
