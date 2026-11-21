package workflow

import (
	"context"
	"errors"
	"time"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workflow"
)

// ExecutionDeps has eight fields: assignment resolves the subject
// (principals), checks it is an active member (members) and makes it a
// participant (participants), besides the command lifecycle, the task, its
// flow position, the execution records and the change event.
type ExecutionDeps struct {
	Principals   identity.PrincipalReader `json:"principals"`
	Participants az.ParticipantWriter     `json:"participants"`
	Members      az.MemberReader          `json:"members"`
	Commands     command.Handler          `json:"commands"`
	Tasks        task.Reader              `json:"tasks"`
	Flow         flow.Controller          `json:"flow"`
	Executions   execution.Store          `json:"executions"`
	Events       record.EventWriter       `json:"events"`
}
type ExecutionModule struct{ d ExecutionDeps }

func init() { pluginkit.Register("commands/execution", NewExecution) }

func NewExecution(_ struct{}, d ExecutionDeps) (*ExecutionModule, error) {
	if d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Executions == nil || d.Principals == nil || d.Members == nil || d.Participants == nil || d.Events == nil {
		return nil, fault.ErrInput
	}
	return &ExecutionModule{d}, nil
}

func (m *ExecutionModule) Open(ctx context.Context, token, key string, in api.Open) (api.Ref, error) {
	return run(ctx, m.d.Commands, token, key, "execution.open", in.TaskID, in, func(tx context.Context, p identity.Principal) (api.Ref, error) {
		t, pos, e := load(tx, m.d.Tasks, m.d.Flow, p, in.TaskID)
		if e != nil {
			return api.Ref{}, e
		}
		if in.BasisVersion != t.BasisVersion {
			return api.Ref{}, fault.ErrConflict
		}
		if p.Kind != "human" && (p.Kind != "agent" || p.CredentialID == "") {
			return api.Ref{}, az.ErrForbidden
		}
		assigned, e := m.d.Executions.Assignee(tx, t.Tenant, t.ID)
		if e != nil {
			return api.Ref{}, e
		}
		if assigned != p.ID {
			return api.Ref{}, az.ErrForbidden
		}
		if _, e = m.d.Flow.CheckOpen(tx, t); e != nil {
			return api.Ref{}, e
		}
		// A new claim replaces any earlier one and fences it at once; only the
		// caller's own live claim of this round and basis has nothing to replace.
		old, e := m.d.Executions.Current(tx, t.Tenant, t.ID)
		if e == nil {
			if old.Writable && old.RoundID == pos.RoundID && old.BasisVersion == t.BasisVersion && ownExecution(p, old) {
				return api.Ref{}, fault.ErrConflict
			}
			old.Writable = false
			if e = m.d.Executions.SaveExecution(tx, old); e != nil {
				return api.Ref{}, e
			}
		} else if !errors.Is(e, fault.ErrNotFound) {
			return api.Ref{}, e
		}
		v, e := m.start(tx, t, pos, p.ID, agentCredential(p))
		if e != nil {
			return api.Ref{}, e
		}
		ref, e := finish(tx, m.d.Flow, m.d.Events, t)
		ref.ExecutionID = v.ID
		return ref, e
	})
}

func (m *ExecutionModule) Assign(ctx context.Context, token, key string, in api.Assignment) (api.Ref, error) {
	return run(ctx, m.d.Commands, token, key, "execution.assign", in.TaskID, in, func(tx context.Context, p identity.Principal) (api.Ref, error) {
		t, pos, e := load(tx, m.d.Tasks, m.d.Flow, p, in.TaskID)
		if e != nil {
			return api.Ref{}, e
		}
		if pos.Status != flow.PhaseActive {
			return api.Ref{}, fault.ErrConflict
		}
		if _, e = m.d.Flow.CheckOpen(tx, t); e != nil {
			return api.Ref{}, e
		}
		subject, e := m.d.Principals.Resolve(tx, t.Tenant, in.Subject)
		if e != nil {
			return api.Ref{}, e
		}
		if subject.Kind != "agent" && subject.Kind != "human" {
			return api.Ref{}, fault.ErrInput
		}
		member, e := m.d.Members.GetMember(tx, t.Tenant, subject.ID)
		if e != nil || !member.Active {
			return api.Ref{}, az.ErrForbidden
		}
		if e = fence(tx, m.d.Executions, t); e != nil {
			return api.Ref{}, e
		}
		// The owner already holds every task right; a participation row would add nothing.
		if in.Subject != t.Owner {
			if e = m.d.Participants.SetParticipant(tx, az.Participant{Tenant: t.Tenant, TaskID: t.ID, Subject: in.Subject}, true); e != nil {
				return api.Ref{}, e
			}
		}
		if e = m.d.Executions.AssignSubject(tx, t.Tenant, t.ID, in.Subject); e != nil {
			return api.Ref{}, e
		}
		// A person needs no separate claim: assignment starts their manual work.
		// An Agent still claims, binding the execution to one credential.
		var started execution.Execution
		if subject.Kind == "human" {
			if started, e = m.start(tx, t, pos, subject.ID, ""); e != nil {
				return api.Ref{}, e
			}
		}
		ref, e := finish(tx, m.d.Flow, m.d.Events, t)
		ref.ExecutionID = started.ID
		return ref, e
	})
}

// start opens a writable execution on the current round and basis. Agent work
// is bound to the credential that claimed it; human work passes no credential.
func (m *ExecutionModule) start(ctx context.Context, t task.Task, pos flow.Position, subject, credential string) (execution.Execution, error) {
	v := execution.Execution{Tenant: t.Tenant, TaskID: t.ID, ID: id(), RoundID: pos.RoundID, Subject: subject, CredentialID: credential, BasisVersion: t.BasisVersion, Writable: true, ProgressAt: time.Now()}
	return m.d.Executions.OpenExecution(ctx, v)
}

// Hold implements execution.Executor.
func (m *ExecutionModule) Hold(ctx context.Context, p identity.Principal, tenant, taskID string) (execution.Execution, error) {
	t, e := m.d.Tasks.GetTask(ctx, tenant, taskID)
	if e != nil {
		return execution.Execution{}, e
	}
	pos, e := m.d.Flow.Read(ctx, t)
	if e != nil {
		return execution.Execution{}, e
	}
	v, e := m.d.Executions.Current(ctx, tenant, taskID)
	if errors.Is(e, fault.ErrNotFound) {
		return execution.Execution{}, fault.ErrConflict
	}
	if e != nil {
		return execution.Execution{}, e
	}
	if pos.Status != flow.PhaseActive || !v.Writable || !ownExecution(p, v) || v.RoundID != pos.RoundID || v.BasisVersion != t.BasisVersion {
		return execution.Execution{}, fault.ErrConflict
	}
	return v, nil
}

// Follow implements execution.Executor. Only the task's current writable
// execution can follow; anything else was fenced and stays so.
func (m *ExecutionModule) Follow(ctx context.Context, v execution.Execution) error {
	current, e := m.d.Executions.Current(ctx, v.Tenant, v.TaskID)
	if e != nil {
		return e
	}
	if current.ID != v.ID || !current.Writable {
		return fault.ErrConflict
	}
	t, e := m.d.Tasks.GetTask(ctx, v.Tenant, v.TaskID)
	if e != nil {
		return e
	}
	current.BasisVersion, current.ProgressAt = t.BasisVersion, time.Now()
	return m.d.Executions.SaveExecution(ctx, current)
}

// RecordProgress implements execution.Progress. Callers invoke it inside their
// own command transaction; it never changes the flow or emits a task change.
func (m *ExecutionModule) RecordProgress(ctx context.Context, p identity.Principal, tenant, taskID string) error {
	v, e := m.d.Executions.Current(ctx, tenant, taskID)
	if errors.Is(e, fault.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	if v.Writable && ownExecution(p, v) {
		v.ProgressAt = time.Now()
		return m.d.Executions.SaveExecution(ctx, v)
	}
	// Only the current execution can be writable, so a credential bound to any
	// execution of this task is now fenced. A person is not an instance: their
	// comment is their own whatever happened to their manual work.
	credential := agentCredential(p)
	if credential == "" {
		return nil
	}
	bound, e := m.d.Executions.CredentialBound(ctx, tenant, taskID, credential)
	if e != nil {
		return e
	}
	if bound {
		return fault.ErrConflict
	}
	return nil
}
