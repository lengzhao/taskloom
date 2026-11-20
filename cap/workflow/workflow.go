package workflow

import (
	"context"
	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/question"
)

// Command inputs carry only what each command must pin. Executions,
// results and commits already identify what a write applies to, so most
// commands need no client-side version; a fenced or superseded target is a conflict.
type Assignment struct{ TaskID, Subject string }

// Open pins the basis the caller read, so work never starts on unseen criteria.
type Open struct {
	TaskID       string
	BasisVersion int64
}
type Submit struct{ TaskID, ExecutionID, Body string }
type Review struct {
	TaskID string
	acceptance.Verdict
}
type Cancel struct{ TaskID string }

// Restart starts a new round from the round the caller saw: on an active task
// it sends work back, on a cancelled one it reopens the task. A completed task
// is final; rework is a new task.
type Restart struct{ TaskID, RoundID string }

// Actor names who must act next for the task to move. Empty means nothing is
// pending from a fixed role; Position.Waiting still carries step-specific text.
type Actor string

const (
	ActorOwner    Actor = "owner"
	ActorAssignee Actor = "assignee"
	ActorReviewer Actor = "reviewer"
	// ActorAddressee means whoever an open question of this round was asked of.
	ActorAddressee Actor = "addressee"
)

// Action keys and stable reason codes for Snapshot.Blocked. Codes explain a
// false Can* flag to the current viewer; commands still re-check on write.
const (
	ActionOpen   = "open"
	ActionSubmit = "submit"
	ActionReview = "review"

	ReasonTaskNotActive       = "task_not_active"
	ReasonCredential          = "business_credential_required"
	ReasonNotAssigned         = "not_assigned"
	ReasonExecutionInProgress = "execution_in_progress"
	ReasonStepNotOpen         = "step_not_open"
	ReasonNotYourExecution    = "not_your_execution"
	ReasonExecutionFenced     = "execution_fenced"
	ReasonRoundChanged        = "round_changed"
	ReasonBasisChanged        = "basis_changed"
	ReasonNotReviewer         = "not_reviewer"
	// ReasonChildrenPending: a direct required subtask is not completed.
	ReasonChildrenPending = "required_children_pending"
)

// PlanTask is a readable neighbour in the work breakdown. For a parent,
// Required says whether this task is required by it.
type PlanTask struct {
	TaskID, Title string
	Required      bool
	Status        flow.Phase
	State         flow.Step
}

type Snapshot struct {
	ResultCommit *commit.Commit
	Assignee     string
	NextActor    Actor
	// Blocked holds, for actions relevant to this viewer, why each is not allowed now.
	Blocked                                             map[string]string `json:",omitempty"`
	CanOpen, CanSubmit, CanReview, CanManage, CanAssign bool
	Position                                            flow.Position
	Execution                                           *execution.Execution
	Result                                              *delivery.Result
	Review                                              *acceptance.Review
	// Questions are the open requests for input of the current active round.
	Questions []question.Question `json:",omitempty"`
	// Parent and Children are the direct relations the viewer may read; a
	// relation grants no access. HiddenRequired says only that a required
	// subtask exists that the viewer cannot read.
	Parent         *PlanTask  `json:",omitempty"`
	Children       []PlanTask `json:",omitempty"`
	HiddenRequired bool       `json:",omitempty"`
}

// Ask raises a request for input to the task owner inside the round the
// caller saw; Answer replies to one open question of the current round.
// Addressee is optional: empty asks the task owner; otherwise it must be a
// current task participant and active member other than the asker.
type Ask struct {
	TaskID, RoundID, Body, Addressee string
}
type Answer struct {
	TaskID, QuestionID, Body string
}

// Ref is what a command fixed: the task, the round it left the task in and,
// when it opened one, the execution. Its receipt stores only this, so a replay
// returns the same Ref; callers read current facts through Queries.
type Ref struct {
	TaskID, RoundID string
	ExecutionID     string `json:",omitempty"`
}

type QuestionCommands interface {
	Ask(ctx context.Context, token, key string, in Ask) (Ref, error)
	Answer(ctx context.Context, token, key string, in Answer) (Ref, error)
}
type ExecutionCommands interface {
	Assign(ctx context.Context, token, key string, in Assignment) (Ref, error)
	Open(ctx context.Context, token, key string, in Open) (Ref, error)
}
type ResultCommands interface {
	Submit(ctx context.Context, token, key string, in Submit) (Ref, error)
	Review(ctx context.Context, token, key string, in Review) (Ref, error)
}
type ControlCommands interface {
	Cancel(ctx context.Context, token, key string, in Cancel) (Ref, error)
	Restart(ctx context.Context, token, key string, in Restart) (Ref, error)
}

// Pending is one task whose NextActor is the caller, or, with WaitingOnSubtasks, one
// the caller owns or handles that waits on its required subtasks: still the
// caller's, but nothing on it can move until they complete.
type Pending struct {
	TaskID, Title     string
	NextActor         Actor
	WaitingOnSubtasks bool `json:",omitempty"`
}
type Queries interface {
	Read(ctx context.Context, token, taskID string) (Snapshot, error)
	// Inbox lists readable active tasks currently waiting on the caller, then
	// the caller's own tasks marked WaitingOnSubtasks with no NextActor.
	Inbox(ctx context.Context, token string) ([]Pending, error)
}
