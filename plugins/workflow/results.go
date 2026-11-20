package workflow

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workflow"
)

// ResultDeps has seven fields: a result is a commit (commits) registered as
// the round's result (results) by a live execution (executions), besides
// the command lifecycle, the task, its flow position and the change event.
type ResultDeps struct {
	Commands   command.Handler        `json:"commands"`
	Tasks      task.Reader            `json:"tasks"`
	Flow       flow.Controller        `json:"flow"`
	Executions execution.ResultAccess `json:"executions"`
	Results    delivery.Store         `json:"results"`
	Commits    commit.Log             `json:"commits"`
	Events     record.EventWriter     `json:"events"`
}
type ResultModule struct{ d ResultDeps }

func init() { pluginkit.Register("commands/result", NewResults) }

func NewResults(_ struct{}, d ResultDeps) (*ResultModule, error) {
	if d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Executions == nil || d.Results == nil || d.Commits == nil || d.Events == nil {
		return nil, fault.ErrInput
	}
	return &ResultModule{d}, nil
}

func (m *ResultModule) Submit(ctx context.Context, token, key string, in api.Submit) (api.Ref, error) {
	return run(ctx, m.d.Commands, token, key, "result.submit", in.TaskID, in, func(tx context.Context, p identity.Principal) (api.Ref, error) {
		t, pos, e := load(tx, m.d.Tasks, m.d.Flow, p, in.TaskID)
		if e != nil {
			return api.Ref{}, e
		}
		v, e := m.owned(tx, p, t, in.ExecutionID)
		if e != nil {
			return api.Ref{}, e
		}
		if e = m.active(tx, t, pos, v); e != nil {
			return api.Ref{}, e
		}
		if strings.TrimSpace(in.Body) == "" || len(in.Body) > 20000 {
			return api.Ref{}, fault.ErrConflict
		}
		v.ProgressAt = time.Now() // a result is progress
		if e = m.d.Executions.SaveExecution(tx, v); e != nil {
			return api.Ref{}, e
		}
		current, currentErr := m.d.Results.CurrentResult(tx, t.Tenant, t.ID, v.RoundID)
		if currentErr == nil && current.ExecutionID == v.ID {
			old, e := m.d.Commits.Get(tx, t.Tenant, t.ID, current.CommitID)
			if e != nil {
				return api.Ref{}, e
			}
			if old.BasisVersion == t.BasisVersion && old.Body == in.Body {
				return api.Ref{TaskID: t.ID, RoundID: pos.RoundID}, nil
			}
		} else if currentErr != nil && !errors.Is(currentErr, fault.ErrNotFound) {
			return api.Ref{}, currentErr
		}
		c := commit.Commit{Tenant: t.Tenant, TaskID: t.ID, ID: id(), Kind: "result", Body: in.Body, Author: p.ID, OwnerID: p.OwnerID, BasisVersion: t.BasisVersion}
		if e = m.d.Commits.Append(tx, c); e != nil {
			return api.Ref{}, e
		}
		if e = m.d.Results.RegisterResult(tx, delivery.Result{Tenant: t.Tenant, TaskID: t.ID, RoundID: v.RoundID, ExecutionID: v.ID, CommitID: c.ID}); e != nil {
			return api.Ref{}, e
		}
		return finish(tx, m.d.Flow, m.d.Events, t)
	})
}

func (m *ResultModule) Review(ctx context.Context, token, key string, in api.Review) (api.Ref, error) {
	return run(ctx, m.d.Commands, token, key, "result.review", in.TaskID, in, func(tx context.Context, p identity.Principal) (api.Ref, error) {
		t, pos, e := load(tx, m.d.Tasks, m.d.Flow, p, in.TaskID)
		if e != nil {
			return api.Ref{}, e
		}
		if pos.Status != flow.PhaseActive || pos.Acceptance != flow.AcceptanceManual {
			return api.Ref{}, fault.ErrConflict
		}
		if p.Kind != "human" || p.ID != t.Owner {
			return api.Ref{}, az.ErrForbidden
		}
		if (in.Outcome != acceptance.OutcomeApproved && in.Outcome != acceptance.OutcomeRejected) || len(in.Reason) > 20000 {
			return api.Ref{}, fault.ErrInput
		}
		// A rejection returns work to the executor, which needs to know what to change.
		if in.Outcome == acceptance.OutcomeRejected && strings.TrimSpace(in.Reason) == "" {
			return api.Ref{}, fault.ErrInput
		}
		v, e := m.d.Results.CurrentResult(tx, t.Tenant, t.ID, pos.RoundID)
		if e != nil {
			return api.Ref{}, e
		}
		if v.CommitID != in.CommitID {
			return api.Ref{}, fault.ErrConflict
		}
		result, e := m.d.Commits.Get(tx, t.Tenant, t.ID, v.CommitID)
		if e != nil {
			return api.Ref{}, e
		}
		if result.BasisVersion != t.BasisVersion {
			return api.Ref{}, fault.ErrConflict
		}
		// The verdict is stored once, as an immutable commit referring to the result.
		c := commit.Commit{Tenant: t.Tenant, TaskID: t.ID, ID: id(), Kind: acceptance.Kind(in.Outcome), Body: in.Reason, RefID: v.CommitID, Author: p.ID, BasisVersion: t.BasisVersion}
		if e = m.d.Commits.Append(tx, c); e != nil {
			return api.Ref{}, e
		}
		return finish(tx, m.d.Flow, m.d.Events, t)
	})
}

// owned loads the execution and requires it to belong to this task and caller.
func (m *ResultModule) owned(ctx context.Context, p identity.Principal, t task.Task, executionID string) (execution.Execution, error) {
	v, e := m.d.Executions.GetExecution(ctx, p.Tenant, executionID)
	if e != nil {
		return v, e
	}
	if v.TaskID != t.ID || !ownExecution(p, v) {
		return v, az.ErrForbidden
	}
	return v, nil
}

// active requires the execution to be the task's latest, still writable, on
// the current active round and basis.
func (m *ResultModule) active(ctx context.Context, t task.Task, pos flow.Position, v execution.Execution) error {
	latest, e := m.d.Executions.Current(ctx, t.Tenant, t.ID)
	if e != nil {
		return e
	}
	if pos.Status != flow.PhaseActive || pos.RoundID != v.RoundID || t.BasisVersion != v.BasisVersion || latest.ID != v.ID || !v.Writable {
		return fault.ErrConflict
	}
	return nil
}
