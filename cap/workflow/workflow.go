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

// Basis pins a flow command to the state the client saw. ExpectedRevision is the
// flow Position.Revision, distinct from task.Version used by basis edits.
type Basis struct {
	TaskID, RoundID                string
	BasisVersion, ExpectedRevision int64
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

// Actor names who must act next for the task to move. Empty means nothing is
// pending from a fixed role; Position.Waiting still carries step-specific text.
type Actor string

const (
	ActorOwner    Actor = "owner"
	ActorAgent    Actor = "agent"
	ActorReviewer Actor = "reviewer"
)

// Action keys and stable reason codes for Snapshot.Blocked. Codes explain a
// false Can* flag to the current viewer; commands still re-check on write.
const (
	ActionOpen   = "open"
	ActionRenew  = "renew"
	ActionSubmit = "submit"
	ActionReview = "review"

	ReasonTaskNotActive       = "task_not_active"
	ReasonCredential          = "business_credential_required"
	ReasonNotAssigned         = "not_assigned"
	ReasonExecutionInProgress = "execution_in_progress"
	ReasonPreviousNotStopped  = "previous_execution_not_stopped"
	ReasonStepNotOpen         = "step_not_open"
	ReasonNotYourExecution    = "not_your_execution"
	ReasonExecutionNotActive  = "execution_not_active"
	ReasonExecutionFenced     = "execution_fenced"
	ReasonRoundChanged        = "round_changed"
	ReasonBasisChanged        = "basis_changed"
	ReasonLeaseExpired        = "lease_expired"
	ReasonNotReviewer         = "not_reviewer"
)

type Snapshot struct {
	ResultCommit  *commit.Commit
	AssignedAgent string
	NextActor     Actor
	// Blocked holds, for actions relevant to this viewer, why each is not allowed now.
	Blocked                                                        map[string]string `json:",omitempty"`
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
