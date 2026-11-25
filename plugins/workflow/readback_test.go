package workflow_test

import (
	"context"

	"github.com/lengzhao/pluginkit/build"

	api "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/cap/workspace"
)

// Commands return only a Ref; the API answers with the caller's current
// snapshot. These adapters do the same, so tests assert on read-back facts.
type readBack struct{ q api.Queries }

func (r readBack) after(ctx context.Context, token string, ref api.Ref, e error) (api.Snapshot, error) {
	if e != nil {
		return api.Snapshot{}, e
	}
	v, e := r.q.Read(ctx, token, ref.TaskID, 0)
	return v.Snapshot, e
}

// pending is one inbox row as the tests assert on it.
type pending struct {
	TaskID, Title     string
	NextActor         api.Actor
	WaitingOnSubtasks bool
}

// inbox lists the caller's work (List with Mine).
func inbox(ctx context.Context, q api.Queries, token string, ids []string) ([]pending, error) {
	rows, _, e := q.List(ctx, token, api.ListFilter{IDs: ids, Mine: true})
	out := []pending{}
	for _, r := range rows {
		x := pending{TaskID: r.Task.ID, Title: r.Task.Title, WaitingOnSubtasks: !r.ForMe}
		if r.ForMe {
			x.NextActor = r.NextActor
		}
		out = append(out, x)
	}
	return out, e
}

type executionCommands struct {
	c api.ExecutionCommands
	readBack
}

func (x executionCommands) Assign(ctx context.Context, token, key string, in api.Assignment) (api.Snapshot, error) {
	ref, e := x.c.Assign(ctx, token, key, in)
	return x.after(ctx, token, ref, e)
}
func (x executionCommands) Open(ctx context.Context, token, key string, in api.Open) (api.Snapshot, error) {
	ref, e := x.c.Open(ctx, token, key, in)
	return x.after(ctx, token, ref, e)
}

type resultCommands struct {
	c api.ResultCommands
	readBack
}

func (x resultCommands) Submit(ctx context.Context, token, key string, in api.Submit) (api.Snapshot, error) {
	ref, e := x.c.Submit(ctx, token, key, in)
	return x.after(ctx, token, ref, e)
}
func (x resultCommands) Review(ctx context.Context, token, key string, in api.Review) (api.Snapshot, error) {
	ref, e := x.c.Review(ctx, token, key, in)
	return x.after(ctx, token, ref, e)
}

type controlCommands struct {
	c api.ControlCommands
	readBack
}

func (x controlCommands) Cancel(ctx context.Context, token, key string, in api.Cancel) (api.Snapshot, error) {
	ref, e := x.c.Cancel(ctx, token, key, in)
	return x.after(ctx, token, ref, e)
}
func (x controlCommands) Restart(ctx context.Context, token, key string, in api.Restart) (api.Snapshot, error) {
	ref, e := x.c.Restart(ctx, token, key, in)
	return x.after(ctx, token, ref, e)
}

type questionCommands struct {
	c api.QuestionCommands
	readBack
}

func (x questionCommands) Ask(ctx context.Context, token, key string, in api.Ask) (api.Snapshot, error) {
	ref, e := x.c.Ask(ctx, token, key, in)
	return x.after(ctx, token, ref, e)
}
func (x questionCommands) Answer(ctx context.Context, token, key string, in api.Answer) (api.Snapshot, error) {
	ref, e := x.c.Answer(ctx, token, key, in)
	return x.after(ctx, token, ref, e)
}
func (x questionCommands) Close(ctx context.Context, token, key string, in api.CloseQuestion) (api.Snapshot, error) {
	ref, e := x.c.Close(ctx, token, key, in)
	return x.after(ctx, token, ref, e)
}

func (f *fixture) questions() questionCommands {
	c, _ := build.GetByID[api.QuestionCommands](f.graph, "question-commands")
	return questionCommands{c, readBack{f.query}}
}

// taskCommands reads task views back after workspace commands.
type taskCommands struct {
	workspace.Commands
	q api.Queries
}

func (x taskCommands) Create(ctx context.Context, token, key string, in workspace.Create) (api.View, error) {
	ref, e := x.Commands.Create(ctx, token, key, in)
	if e != nil {
		return api.View{}, e
	}
	return x.q.Read(ctx, token, ref.TaskID, 0)
}
func (x taskCommands) Edit(ctx context.Context, token, key string, in workspace.Edit) (api.View, error) {
	ref, e := x.Commands.Edit(ctx, token, key, in)
	if e != nil {
		return api.View{}, e
	}
	return x.q.Read(ctx, token, ref.TaskID, 0)
}

// snap reads the caller's snapshot of a task.
func (f *fixture) snap(token, id string) (api.Snapshot, error) {
	v, e := f.query.Read(f.ctx, token, id, 0)
	return v.Snapshot, e
}
