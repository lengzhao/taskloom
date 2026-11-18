package workspace

import (
	"context"

	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/task"
)

type Create struct{ Title, Goal, Criteria string }
type Edit struct {
	TaskID, Title, Goal, Criteria string
	ExpectedVersion               int64
}
type Comment struct{ TaskID, Body, RefID string }
type Detail struct {
	Task                task.Task
	Position            flow.Position
	Commits             []commit.Commit
	CanEdit, CanComment bool
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
	SetParticipant(ctx context.Context, credential, idempotencyKey string, value Participation) error
	Create(ctx context.Context, credential, idempotencyKey string, value Create) (Detail, error)
	Edit(ctx context.Context, credential, idempotencyKey string, value Edit) (Detail, error)
	Comment(ctx context.Context, credential, idempotencyKey string, value Comment) (commit.Commit, error)
}
type Queries interface {
	List(ctx context.Context, credential string) ([]Detail, error)
	Get(ctx context.Context, credential, taskID string, after int64) (Detail, error)
}
type ChangeFeed interface {
	Changes(ctx context.Context, credential, cursor string) (Page, error)
}
