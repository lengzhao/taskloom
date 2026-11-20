package workflow

import (
	"context"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workflow"
)

type ControlDeps struct {
	Commands   command.Handler    `json:"commands"`
	Tasks      task.Reader        `json:"tasks"`
	Flow       flow.Controller    `json:"flow"`
	Executions execution.Store    `json:"executions"`
	Events     record.EventWriter `json:"events"`
}
type ControlModule struct{ d ControlDeps }

func init() { pluginkit.Register("commands/flow", NewControl) }

func NewControl(_ struct{}, d ControlDeps) (*ControlModule, error) {
	if d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Executions == nil || d.Events == nil {
		return nil, fault.ErrInput
	}
	return &ControlModule{d}, nil
}

func (m *ControlModule) Cancel(ctx context.Context, token, key string, in api.Cancel) (api.Ref, error) {
	return run(ctx, m.d.Commands, token, key, "task.cancel", in.TaskID, in, func(tx context.Context, p identity.Principal) (api.Ref, error) {
		t, _, e := load(tx, m.d.Tasks, m.d.Flow, p, in.TaskID)
		if e != nil {
			return api.Ref{}, e
		}
		if e = fence(tx, m.d.Executions, t); e != nil {
			return api.Ref{}, e
		}
		if _, e = m.d.Flow.Cancel(tx, t); e != nil {
			return api.Ref{}, e
		}
		return finish(tx, m.d.Flow, m.d.Events, t)
	})
}

// Restart reopens a cancelled task in a new round from the round the caller
// saw; flow rejects active and completed tasks. Cancelling already fenced the
// execution and a cancelled task takes no assignment or claim, so nothing is
// left to fence here.
func (m *ControlModule) Restart(ctx context.Context, token, key string, in api.Restart) (api.Ref, error) {
	return run(ctx, m.d.Commands, token, key, "task.restart", in.TaskID, in, func(tx context.Context, p identity.Principal) (api.Ref, error) {
		t, pos, e := load(tx, m.d.Tasks, m.d.Flow, p, in.TaskID)
		if e != nil {
			return api.Ref{}, e
		}
		if pos.RoundID != in.RoundID {
			return api.Ref{}, fault.ErrConflict
		}
		if _, e = m.d.Flow.Restart(tx, t); e != nil {
			return api.Ref{}, e
		}
		return finish(tx, m.d.Flow, m.d.Events, t)
	})
}
