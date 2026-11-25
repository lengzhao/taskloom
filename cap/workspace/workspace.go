package workspace

import (
	"context"
	"github.com/lengzhao/taskloom/cap/authorization"

	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/task"
)

// Create may place the new task under ParentID in the same transaction; the
// creator then needs planning rights on the parent, or, for an Agent, the
// parent's current execution. Required says whether the parent's delivery
// waits for it. Owner, only for a subtask, names any active human member;
// empty means the creator, or the parent's owner when an Agent creates it.
// Acceptance fixes the task's acceptance mode; empty is the deployment
// default for a person and none for an Agent's subtask. Assignee assigns the
// new task in the same transaction, as the assign command would; a person
// needs assignment rights on the new task, an Agent is limited by the
// deployment's agent assignment policy.
type Create struct {
	Title, Goal, Criteria, ProjectID, ParentID string
	Owner                                      string          `json:",omitempty"`
	Required                                   bool            `json:",omitempty"`
	Acceptance                                 flow.Acceptance `json:",omitempty"`
	Assignee                                   string          `json:",omitempty"`
}

// Handover makes Owner, an active person, responsible for the task. It pins
// SeenOwner, the owner the caller saw, so two handovers cannot both apply.
type Handover struct {
	TaskID, Owner, SeenOwner, Reason string
}

// KindOwnerChanged is the timeline commit of a handover; its body is the
// JSON of OwnerChange.
const KindOwnerChanged = "task.owner"

type OwnerChange struct {
	From, To, Reason string
}

// Ownership hands a task over to another responsible person.
type Ownership interface {
	Handover(ctx context.Context, credential, idempotencyKey string, value Handover) (Ref, error)
}

// ProjectMove only re-files a task; the last move wins and pins no version.
type ProjectMove struct{ TaskID, ProjectID string }

// Edit pins the basis version the editor saw.
type Edit struct {
	TaskID, Title, Goal, Criteria string
	BasisVersion                  int64
}

// Comment may carry files of the same task and links; Body may then be empty.
type Comment struct {
	TaskID, Body, RefID string
	Files               []string      `json:",omitempty"`
	Links               []commit.Link `json:",omitempty"`
}
type Detail struct {
	Task                task.Task
	Position            flow.Position
	Commits             []commit.Commit
	CanEdit, CanComment bool
}

// Ref is what a task command fixed: the task and, for a comment, the commit it
// appended. Its receipt stores only this, so a replay returns the same Ref;
// callers read current facts through Queries.Get.
type Ref struct {
	TaskID   string
	CommitID string `json:",omitempty"`
}
type Change struct{ TaskID, Kind string }
type Page struct {
	Changes []Change
	Cursor  string
}
type Participation struct {
	TaskID, Subject string
	Present         bool
}
type Commands interface {
	SetProject(ctx context.Context, credential, idempotencyKey string, value ProjectMove) error
	SetParticipant(ctx context.Context, credential, idempotencyKey string, value Participation) error
	Create(ctx context.Context, credential, idempotencyKey string, value Create) (Ref, error)
	Edit(ctx context.Context, credential, idempotencyKey string, value Edit) (Ref, error)
	Comment(ctx context.Context, credential, idempotencyKey string, value Comment) (Ref, error)
}
type Viewer struct {
	Tenant, Subject, Kind string
	CanAdmin              bool
}

// PeoplePage is one page of the member directory; Next, when set, is the
// After of the following page.
type PeoplePage struct {
	Items []authorization.MemberOption
	Next  string `json:",omitempty"`
}

// Directory page sizes: Limit 0 means PeopleDefault, larger values are capped.
const (
	PeopleDefault = 50
	PeopleMax     = 200
)

type Queries interface {
	Viewer(ctx context.Context, credential string) (Viewer, error)
	Members(ctx context.Context, credential string) ([]authorization.DirectoryMember, error)
	// People pages the active member directory, readable by any active member,
	// Agents included: it is where an addressee or assignee is picked.
	People(ctx context.Context, credential string, q authorization.MemberQuery) (PeoplePage, error)
	// List returns readable tasks without their timelines; ids, when given,
	// limits it to those tasks (at most task.MaxPick).
	List(ctx context.Context, credential string, ids []string) ([]Detail, error)
	Get(ctx context.Context, credential, taskID string, after int64) (Detail, error)
}
type ChangeFeed interface {
	Changes(ctx context.Context, credential, cursor string) (Page, error)
}
