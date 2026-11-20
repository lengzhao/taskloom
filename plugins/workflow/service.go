package workflow

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"

	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/runtime/command"
)

// Each command module owns its declared Deps; the helpers below take only the
// narrow capabilities they use, so no module carries another module's fields.

func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return fmt.Sprintf("%x", b)
}

// run wraps a task command in the shared lifecycle. The receipt stores only the
// Ref; current facts are read separately, so a replay never returns stale facts.
func run[T any](ctx context.Context, h capcommand.Handler, token, key, operation, taskID string, in T, apply func(context.Context, identity.Principal) (api.Ref, error)) (api.Ref, error) {
	var out api.Ref
	b, e := command.Run(ctx, h, capcommand.Request{Token: token, Key: key, Operation: operation, Resource: taskID}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		ref, e := apply(tx, p)
		if e != nil {
			return nil, e
		}
		return json.Marshal(ref)
	})
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}

// load reads the task and its position; commands pin their own targets.
func load(ctx context.Context, tasks task.Reader, fl flow.Controller, p identity.Principal, taskID string) (task.Task, flow.Position, error) {
	t, e := tasks.GetTask(ctx, p.Tenant, taskID)
	if e != nil {
		return t, flow.Position{}, e
	}
	pos, e := fl.Read(ctx, t)
	return t, pos, e
}

// finish lets the flow derive completion from the facts just written and
// announces the change, also to the direct parent whose view of its subtasks
// may change with it; the Ref carries the round the task is now in.
func finish(ctx context.Context, fl flow.Controller, events record.EventWriter, t task.Task) (api.Ref, error) {
	pos, e := fl.Refresh(ctx, t)
	if e != nil {
		return api.Ref{}, e
	}
	parent, e := fl.Parent(ctx, t)
	if e != nil {
		return api.Ref{}, e
	}
	for _, id := range []string{t.ID, parent} {
		if id == "" {
			continue
		}
		if e = events.AppendEvent(ctx, record.Event{Tenant: t.Tenant, Resource: id, Kind: record.EventTaskChanged}); e != nil {
			return api.Ref{}, e
		}
	}
	return api.Ref{TaskID: t.ID, RoundID: pos.RoundID}, nil
}

type executionWriter interface {
	Current(ctx context.Context, tenant, taskID string) (execution.Execution, error)
	SaveExecution(ctx context.Context, value execution.Execution) error
}

// fence revokes the current execution's write access; nothing else about it
// changes, and nothing waits for the executor to acknowledge.
func fence(ctx context.Context, executions executionWriter, t task.Task) error {
	v, e := executions.Current(ctx, t.Tenant, t.ID)
	if errors.Is(e, fault.ErrNotFound) {
		return nil
	}
	if e != nil || !v.Writable {
		return e
	}
	v.Writable = false
	return executions.SaveExecution(ctx, v)
}
