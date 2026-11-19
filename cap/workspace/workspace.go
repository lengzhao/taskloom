package workspace

import (
	"context"
	"github.com/lengzhao/taskloom/cap/authorization"

	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/task"
)

type Create struct{ Title, Goal, Criteria, ProjectID string }

// ProjectMove only re-files a task; the last move wins and pins no version.
type ProjectMove struct{ TaskID, ProjectID string }

// Edit pins the basis version the editor saw.
type Edit struct {
	TaskID, Title, Goal, Criteria string
	BasisVersion                  int64
}
type Comment struct{ TaskID, Body, RefID string }
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
type Queries interface {
	Viewer(ctx context.Context, credential string) (Viewer, error)
	Members(ctx context.Context, credential string) ([]authorization.DirectoryMember, error)
	Candidates(ctx context.Context, credential, taskID string) ([]authorization.MemberOption, error)
	List(ctx context.Context, credential string) ([]Detail, error)
	Get(ctx context.Context, credential, taskID string, after int64) (Detail, error)
}
type ChangeFeed interface {
	Changes(ctx context.Context, credential, cursor string) (Page, error)
}
