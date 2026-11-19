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
	return r.q.Read(ctx, token, ref.TaskID)
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

func (f *fixture) questions() questionCommands {
	c, _ := build.GetByID[api.QuestionCommands](f.graph, "question-commands")
	return questionCommands{c, readBack{f.query}}
}

// taskCommands reads task details back after workspace commands.
type taskCommands struct {
	workspace.Commands
	q workspace.Queries
}

func (x taskCommands) Create(ctx context.Context, token, key string, in workspace.Create) (workspace.Detail, error) {
	ref, e := x.Commands.Create(ctx, token, key, in)
	if e != nil {
		return workspace.Detail{}, e
	}
	return x.q.Get(ctx, token, ref.TaskID, 0)
}
func (x taskCommands) Edit(ctx context.Context, token, key string, in workspace.Edit) (workspace.Detail, error) {
	ref, e := x.Commands.Edit(ctx, token, key, in)
	if e != nil {
		return workspace.Detail{}, e
	}
	return x.q.Get(ctx, token, ref.TaskID, 0)
}
