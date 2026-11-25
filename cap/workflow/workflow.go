package workflow

import (
	"context"
	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/question"
	"github.com/lengzhao/taskloom/cap/task"
	"time"
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

// Submit delivers text, files of the same task and links; any one suffices.
type Submit struct {
	TaskID, ExecutionID, Body string
	Files                     []string      `json:",omitempty"`
	Links                     []commit.Link `json:",omitempty"`
}
type Review struct {
	TaskID string
	acceptance.Verdict
}
type Cancel struct{ TaskID string }

// Restart reopens a cancelled task in a new round, from the round the caller
// saw. Active and completed tasks reject it: active work is sent back by
// review, reassignment or a basis edit; a completed task is final and rework
// is a new task.
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
// OpenQuestion is an unanswered question with what its commit says.
type OpenQuestion struct {
	question.Question
	Asker, Body string
	AskedAt     time.Time
}

// Reason explains why an action is not available to the viewer now.
type Reason struct{ Code, Message string }

type PlanTask struct {
	TaskID, Title string
	Required      bool
	Status        flow.Phase
	State         flow.Step
}

type Snapshot struct {
	// Viewer is the caller's subject; the other per-viewer fields are judged for it.
	Viewer       string
	ResultCommit *commit.Commit
	Assignee     string
	NextActor    Actor
	// Blocked holds, for actions relevant to this viewer, why each is not
	// allowed now: a stable code and its text from fault.Message.
	Blocked                                             map[string]Reason `json:",omitempty"`
	CanOpen, CanSubmit, CanReview, CanManage, CanAssign bool
	Position                                            flow.Position
	Execution                                           *execution.Execution
	Result                                              *delivery.Result
	Review                                              *acceptance.Review
	// Questions are the open requests for input of the current active round,
	// with the asking commit's author, text and time so a reader need not
	// find that commit in the timeline.
	Questions []OpenQuestion `json:",omitempty"`
	// Parent and Children are the direct relations the viewer may read; a
	// relation grants no access. HiddenRequired says only that a required
	// subtask exists that the viewer cannot read.
	Parent         *PlanTask  `json:",omitempty"`
	Children       []PlanTask `json:",omitempty"`
	HiddenRequired bool       `json:",omitempty"`
	// Participants are the task's current participants, the subjects a
	// question may name as Addressee besides the owner.
	Participants []string `json:",omitempty"`
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

// CloseQuestion ends an open question without its answer, so it no longer
// holds completion. Only the asker or the task owner may close it, with a
// reason; the closing commit settles the question as an answer would.
type CloseQuestion struct {
	TaskID, QuestionID, Reason string
}

// KindQuestionClosed is the commit kind of a question closed without an answer.
const KindQuestionClosed = "question.closed"

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
	Close(ctx context.Context, token, key string, in CloseQuestion) (Ref, error)
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

// Summary is one row of the task list, all derived from facts on read: the
// task, its lifecycle position, who handles it, its direct parent (an ID is
// not a secret; its title shows only if readable), who must act next and when
// its timeline last changed. ForMe says the caller is that next actor.
type Summary struct {
	Task      task.Task
	Position  flow.Position
	Assignee  string `json:",omitempty"`
	ParentID  string `json:",omitempty"`
	NextActor Actor  `json:",omitempty"`
	ForMe     bool   `json:",omitempty"`
	UpdatedAt time.Time
}

// ListFilter narrows the task list. IDs, when given, limits it to those tasks
// (at most task.MaxPick), so a client can refresh only what changed. Mine
// keeps the caller's work: tasks waiting on the caller first, then tasks the
// caller owns or handles that wait on their required subtasks.
type ListFilter struct {
	IDs  []string
	Mine bool
}

// View is everything one task page shows: the task, the caller's snapshot and
// one page of the timeline, the newest unless an earlier one was asked for.
// Earlier says older commits precede the page.
type View struct {
	Task task.Task
	Snapshot
	Commits             []commit.Commit
	Earlier             bool `json:",omitempty"`
	CanEdit, CanComment bool
}

// TimelinePage is how many commits one View carries.
const TimelinePage = 100

type Queries interface {
	// Read returns the task the caller may read with one timeline page: the
	// newest commits, or those just before the given sequence.
	Read(ctx context.Context, token, taskID string, before int64) (View, error)
	List(ctx context.Context, token string, filter ListFilter) ([]Summary, error)
}
