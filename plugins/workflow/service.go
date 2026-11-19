package workflow

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/runtime/command"
	"strings"
	"time"
)

// dependencies is an internal implementation detail. Registered constructors
// below declare each injected capability explicitly, never a dependency bag.
type dependencies struct {
	Participants     az.ParticipantWriter
	Principals       identity.PrincipalReader
	Members          az.MemberReader
	Commands         capcommand.Handler
	Tasks            task.Reader
	Flow             flow.Controller
	Executions       execution.Reader
	ExecutionWrites  execution.Store
	ResultExecutions execution.ResultAccess
	Results          delivery.Store
	Commits          commit.Log
	Reviews          acceptance.Records
	Snapshots        api.FactsReader
	Events           record.EventWriter
}
type Service struct {
	d dependencies
}

func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return fmt.Sprintf("%x", b)
}
func (s *Service) load(ctx context.Context, p identity.Principal, in api.Basis) (task.Task, flow.Position, error) {
	t, e := s.d.Tasks.GetTask(ctx, p.Tenant, in.TaskID)
	if e != nil {
		return t, flow.Position{}, e
	}
	pos, e := s.d.Flow.Read(ctx, t)
	if e != nil {
		return t, pos, e
	}
	if pos.RoundID != in.RoundID || t.BasisVersion != in.BasisVersion || pos.Revision != in.ExpectedRevision {
		return t, pos, fault.ErrConflict
	}
	return t, pos, nil
}
func (s *Service) snapshot(ctx context.Context, t task.Task) (api.Snapshot, error) {
	return s.d.Snapshots.ReadFacts(ctx, t)
}
func (s *Service) finish(ctx context.Context, t task.Task) ([]byte, error) {
	if _, e := s.d.Flow.Refresh(ctx, t); e != nil {
		return nil, e
	}
	if e := s.d.Events.AppendEvent(ctx, record.Event{Tenant: t.Tenant, Resource: t.ID, Kind: "task.changed"}); e != nil {
		return nil, e
	}
	v, e := s.snapshot(ctx, t)
	if e != nil {
		return nil, e
	}
	return json.Marshal(v)
}
func run[T any](ctx context.Context, s *Service, token, key, operation, taskID string, in T, apply func(context.Context, identity.Principal) ([]byte, error)) (api.Snapshot, error) {
	var out api.Snapshot
	b, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: operation, Resource: taskID, ReadOperation: "task.read"}, in, apply)
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}
func (s *Service) Open(ctx context.Context, token, key string, in api.Open) (api.Snapshot, error) {
	return run(ctx, s, token, key, "execution.open", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, pos, e := s.load(tx, p, in.Basis)
		if e != nil {
			return nil, e
		}
		if p.Kind != "human" && (p.Kind != "agent" || p.InstanceID == "" || p.Incarnation == "") {
			return nil, az.ErrForbidden
		}
		assigned, e := s.d.Executions.Assignee(tx, t.Tenant, t.ID)
		if e != nil {
			return nil, e
		}
		if assigned != p.ID {
			return nil, az.ErrForbidden
		}
		if _, e = s.d.Flow.CheckOpen(tx, t); e != nil {
			return nil, e
		}
		// A new claim replaces any earlier one and fences it at once; only the
		// caller's own live claim in this round has nothing to replace.
		old, e := s.d.Executions.Current(tx, t.Tenant, t.ID)
		if e == nil {
			if old.Writable && old.RoundID == pos.RoundID && ownExecution(p, old) {
				return nil, fault.ErrConflict
			}
			old.Writable = false
			if e = s.d.ExecutionWrites.SaveExecution(tx, old); e != nil {
				return nil, e
			}
		} else if !errors.Is(e, fault.ErrNotFound) {
			return nil, e
		}
		v := execution.Execution{Tenant: t.Tenant, TaskID: t.ID, ID: id(), RoundID: pos.RoundID, Subject: p.ID, InstanceID: p.InstanceID, Incarnation: p.Incarnation, BasisVersion: t.BasisVersion, Writable: true, ProgressAt: time.Now()}
		if p.Kind == "human" {
			v.Manual = true
			v.InstanceID, v.Incarnation = "", ""
		}
		_, e = s.d.ExecutionWrites.OpenExecution(tx, v)
		if e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}
func (s *Service) owned(ctx context.Context, p identity.Principal, taskID, executionID string) (task.Task, execution.Execution, error) {
	t, e := s.d.Tasks.GetTask(ctx, p.Tenant, taskID)
	if e != nil {
		return t, execution.Execution{}, e
	}
	v, e := s.d.Executions.GetExecution(ctx, p.Tenant, executionID)
	if e != nil {
		return t, v, e
	}
	if v.TaskID != taskID || !ownExecution(p, v) {
		return t, v, az.ErrForbidden
	}
	return t, v, nil
}
func (s *Service) active(ctx context.Context, t task.Task, v execution.Execution) error {
	pos, e := s.d.Flow.Read(ctx, t)
	if e != nil {
		return e
	}
	latest, e := s.d.Executions.Current(ctx, t.Tenant, t.ID)
	if e != nil {
		return e
	}
	if pos.Status != flow.PhaseActive || pos.RoundID != v.RoundID || t.BasisVersion != v.BasisVersion || latest.ID != v.ID || !v.Writable {
		return fault.ErrConflict
	}
	return nil
}

// RecordProgress implements execution.Progress. Callers invoke it inside their
// own command transaction; it never changes the flow or emits a task change.
func (s *Service) RecordProgress(ctx context.Context, p identity.Principal, tenant, taskID string) error {
	v, e := s.d.Executions.Current(ctx, tenant, taskID)
	if errors.Is(e, fault.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	if !v.Writable || !ownExecution(p, v) {
		return nil
	}
	v.ProgressAt = time.Now()
	return s.d.ExecutionWrites.SaveExecution(ctx, v)
}

func (s *Service) Submit(ctx context.Context, token, key string, in api.Submit) (api.Snapshot, error) {
	return run(ctx, s, token, key, "result.submit", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, _, e := s.load(tx, p, in.Basis)
		if e != nil {
			return nil, e
		}
		_, v, e := s.owned(tx, p, in.TaskID, in.ExecutionID)
		if e != nil {
			return nil, e
		}
		if e = s.active(tx, t, v); e != nil {
			return nil, e
		}
		if strings.TrimSpace(in.Body) == "" || len(in.Body) > 20000 {
			return nil, fault.ErrConflict
		}
		v.ProgressAt = time.Now() // a result is progress
		if e = s.d.ResultExecutions.SaveExecution(tx, v); e != nil {
			return nil, e
		}
		current, currentErr := s.d.Results.CurrentSubmission(tx, t.Tenant, t.ID, v.RoundID)
		if currentErr == nil && current.ExecutionID == v.ID && current.BasisVersion == t.BasisVersion {
			old, e := s.d.Commits.Get(tx, t.Tenant, t.ID, current.CommitID)
			if e != nil {
				return nil, e
			}
			if old.Body == in.Body {
				snapshot, e := s.snapshot(tx, t)
				if e != nil {
					return nil, e
				}
				return json.Marshal(snapshot)
			}
		} else if currentErr != nil && !errors.Is(currentErr, fault.ErrNotFound) {
			return nil, currentErr
		}
		c := commit.Commit{Tenant: t.Tenant, TaskID: t.ID, ID: id(), Kind: "result", Body: in.Body, Author: p.ID, OwnerID: p.OwnerID, InstanceID: p.InstanceID, Incarnation: p.Incarnation, BasisVersion: t.BasisVersion}
		if e = s.d.Commits.Append(tx, c); e != nil {
			return nil, e
		}
		if e = s.d.Results.RegisterSubmission(tx, delivery.Submission{Tenant: t.Tenant, TaskID: t.ID, ID: id(), RoundID: v.RoundID, ExecutionID: v.ID, CommitID: c.ID, Author: p.ID, BasisVersion: t.BasisVersion}); e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}
func (s *Service) Review(ctx context.Context, token, key string, in api.Review) (api.Snapshot, error) {
	return run(ctx, s, token, key, "result.review", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, pos, e := s.load(tx, p, in.Basis)
		if e != nil {
			return nil, e
		}
		if pos.Status != flow.PhaseActive || pos.Acceptance != flow.AcceptanceManual {
			return nil, fault.ErrConflict
		}
		if p.Kind != "human" || p.ID != t.Owner {
			return nil, az.ErrForbidden
		}
		if (in.Outcome != acceptance.OutcomeApproved && in.Outcome != acceptance.OutcomeRejected) || len(in.Reason) > 20000 {
			return nil, fault.ErrInput
		}
		// A rejection returns work to the executor, which needs to know what to change.
		if in.Outcome == acceptance.OutcomeRejected && strings.TrimSpace(in.Reason) == "" {
			return nil, fault.ErrInput
		}
		v, e := s.d.Results.CurrentSubmission(tx, t.Tenant, t.ID, pos.RoundID)
		if e != nil {
			return nil, e
		}
		if v.ID != in.SubmissionID || v.CommitID != in.CommitID || v.BasisVersion != t.BasisVersion {
			return nil, fault.ErrConflict
		}
		r := acceptance.Review{Tenant: t.Tenant, TaskID: t.ID, ID: id(), RoundID: pos.RoundID, SubmissionID: v.ID, CommitID: v.CommitID, Reviewer: p.ID, Outcome: in.Outcome, Reason: in.Reason, BasisVersion: t.BasisVersion}
		if e = s.d.Reviews.SaveReview(tx, r); e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}
func (s *Service) Withdraw(ctx context.Context, token, key string, in api.Withdraw) (api.Snapshot, error) {
	return run(ctx, s, token, key, "result.withdraw", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, pos, e := s.load(tx, p, in.Basis)
		if e != nil {
			return nil, e
		}
		if pos.Status != flow.PhaseActive {
			return nil, fault.ErrConflict
		}
		v, e := s.d.Results.CurrentSubmission(tx, t.Tenant, t.ID, pos.RoundID)
		if e != nil {
			return nil, e
		}
		if v.ID != in.SubmissionID {
			return nil, fault.ErrConflict
		}
		if p.ID != t.Owner && p.ID != v.Author {
			return nil, az.ErrForbidden
		}
		if e = s.d.Results.WithdrawSubmission(tx, t.Tenant, v.ID); e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}

// fence revokes the current execution's write access; nothing else about it
// changes, and nothing waits for the executor to acknowledge.
func (s *Service) fence(ctx context.Context, t task.Task) error {
	v, e := s.d.Executions.Current(ctx, t.Tenant, t.ID)
	if errors.Is(e, fault.ErrNotFound) {
		return nil
	}
	if e != nil || !v.Writable {
		return e
	}
	v.Writable = false
	return s.d.ExecutionWrites.SaveExecution(ctx, v)
}
func (s *Service) Cancel(ctx context.Context, token, key string, in api.Basis) (api.Snapshot, error) {
	return run(ctx, s, token, key, "task.cancel", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, _, e := s.load(tx, p, in)
		if e != nil {
			return nil, e
		}
		if e = s.fence(tx, t); e != nil {
			return nil, e
		}
		if _, e = s.d.Flow.Cancel(tx, t); e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}
func (s *Service) Return(ctx context.Context, token, key string, in api.Basis) (api.Snapshot, error) {
	return s.restart(ctx, token, key, in, false)
}
func (s *Service) Reopen(ctx context.Context, token, key string, in api.Basis) (api.Snapshot, error) {
	return s.restart(ctx, token, key, in, true)
}
func (s *Service) restart(ctx context.Context, token, key string, in api.Basis, reopen bool) (api.Snapshot, error) {
	op := "task.return"
	if reopen {
		op = "task.reopen"
	}
	return run(ctx, s, token, key, op, in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, _, e := s.load(tx, p, in)
		if e != nil {
			return nil, e
		}
		if e = s.fence(tx, t); e != nil {
			return nil, e
		}
		if reopen {
			_, e = s.d.Flow.Reopen(tx, t)
		} else {
			_, e = s.d.Flow.Return(tx, t)
		}
		if e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}
func (s *Service) Assign(ctx context.Context, token, key string, in api.Assignment) (api.Snapshot, error) {
	return run(ctx, s, token, key, "execution.assign", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, pos, e := s.load(tx, p, in.Basis)
		if e != nil {
			return nil, e
		}
		if pos.Status != flow.PhaseActive {
			return nil, fault.ErrConflict
		}
		if _, e = s.d.Flow.CheckOpen(tx, t); e != nil {
			return nil, e
		}
		subject, e := s.d.Principals.Resolve(tx, t.Tenant, in.Subject)
		if e != nil {
			return nil, e
		}
		if subject.Kind != "agent" && subject.Kind != "human" {
			return nil, fault.ErrInput
		}
		member, e := s.d.Members.GetMember(tx, t.Tenant, subject.ID)
		if e != nil || !member.Active {
			return nil, az.ErrForbidden
		}
		if e = s.fence(tx, t); e != nil {
			return nil, e
		}
		if e = s.d.Participants.SetParticipant(tx, az.Participant{Tenant: t.Tenant, TaskID: t.ID, Subject: in.Subject}, true); e != nil {
			return nil, e
		}
		if e = s.d.ExecutionWrites.AssignSubject(tx, t.Tenant, t.ID, in.Subject); e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}
