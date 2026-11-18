package workflow

import (
	"context"
	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/task"
)

type Basis struct {
	TaskID, RoundID               string
	BasisVersion, ExpectedVersion int64
}
type Assignment struct {
	Basis
	Subject string
}
type Open struct{ Basis }
type ExecutionRef struct {
	TaskID, ExecutionID string
	Generation          int64
}
type Submit struct {
	Basis
	ExecutionID, Body string
	Generation        int64
}
type Review struct {
	Basis
	acceptance.Verdict
}
type StopEvidence struct {
	ExecutionRef
	Challenge                                     string
	MainStopped, ChildrenStopped, ExternalSettled bool
}
type Isolation struct {
	Basis
	ExecutionID, Reason, Scope string
	Generation                 int64
}
type Withdraw struct {
	Basis
	SubmissionID string
}
type Snapshot struct {
	ResultCommit                                                   *commit.Commit
	AssignedAgent                                                  string
	CanOpen, CanSubmit, CanReview, CanManage, CanIsolate, CanRenew bool
	SafeToExit                                                     bool
	Position                                                       flow.Position
	Execution                                                      *execution.Execution
	Submission                                                     *delivery.Submission
	Review                                                         *acceptance.Review
}
type ExecutionCommands interface {
	Assign(ctx context.Context, token, key string, in Assignment) (Snapshot, error)
	Open(ctx context.Context, token, key string, in Open) (Snapshot, error)
	Renew(ctx context.Context, token, key string, in ExecutionRef) (Snapshot, error)
	End(ctx context.Context, token, key string, in ExecutionRef) (Snapshot, error)
	ProveStopped(ctx context.Context, token, key string, in StopEvidence) (Snapshot, error)
}
type ResultCommands interface {
	Submit(ctx context.Context, token, key string, in Submit) (Snapshot, error)
	Review(ctx context.Context, token, key string, in Review) (Snapshot, error)
	Withdraw(ctx context.Context, token, key string, in Withdraw) (Snapshot, error)
}
type ControlCommands interface {
	Isolate(ctx context.Context, token, key string, in Isolation) (Snapshot, error)
	Cancel(ctx context.Context, token, key string, in Basis) (Snapshot, error)
	Return(ctx context.Context, token, key string, in Basis) (Snapshot, error)
	Reopen(ctx context.Context, token, key string, in Basis) (Snapshot, error)
}
type Queries interface {
	Read(ctx context.Context, token, taskID string) (Snapshot, error)
}

type FactsReader interface {
	ReadFacts(ctx context.Context, value task.Task) (Snapshot, error)
}
