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

type Config struct {
	LeaseSeconds int `json:"leaseSeconds"`
}

// dependencies is an internal implementation detail. Registered constructors
// below declare each injected capability explicitly, never a dependency bag.
type dependencies struct {
	Participants    az.ParticipantWriter
	Principals      identity.PrincipalReader
	Members         az.MemberReader
	Commands        capcommand.Handler
	Tasks           task.Reader
	Flow            flow.Controller
	Executions      execution.Reader
	ExecutionWrites execution.Store
	Results         delivery.Store
	Commits         commit.Log
	Accepting       acceptance.Actions
	Snapshots       api.FactsReader
	Events          record.EventWriter
}
type Service struct {
	c Config
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
	pos, e := s.d.Flow.Read(ctx, t.Tenant, t.ID)
	if e != nil {
		return t, pos, e
	}
	if pos.RoundID != in.RoundID || t.BasisVersion != in.BasisVersion || pos.Revision != in.ExpectedVersion {
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
		if p.Kind != "agent" || p.CredentialPurpose != "business" || p.InstanceID == "" || p.Incarnation == "" {
			return nil, az.ErrForbidden
		}
		assigned, e := s.d.Executions.AssignedAgent(tx, t.Tenant, t.ID)
		if e != nil {
			return nil, e
		}
		if assigned != p.ID {
			return nil, az.ErrForbidden
		}
		if _, e = s.d.Flow.CheckOpen(tx, t); e != nil {
			return nil, e
		}
		safe, e := s.d.Executions.SafeToExit(tx, t.Tenant, t.ID)
		if e != nil {
			return nil, e
		}
		if !safe {
			return nil, fault.ErrConflict
		}
		old, e := s.d.Executions.Current(tx, t.Tenant, t.ID)
		if e == nil {
			old.Writable = false
			if e = s.d.ExecutionWrites.SaveExecution(tx, old); e != nil {
				return nil, e
			}
		} else if !errors.Is(e, fault.ErrNotFound) {
			return nil, e
		}
		_, e = s.d.ExecutionWrites.OpenExecution(tx, execution.Execution{Tenant: t.Tenant, TaskID: t.ID, ID: id(), RoundID: pos.RoundID, Subject: p.ID, InstanceID: p.InstanceID, Incarnation: p.Incarnation, BasisVersion: t.BasisVersion, Status: "active", Writable: true, EndEvidenceID: id(), LeaseUntil: time.Now().Add(time.Duration(s.c.LeaseSeconds) * time.Second)})
		if e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}
func (s *Service) owned(ctx context.Context, p identity.Principal, in api.ExecutionRef) (task.Task, execution.Execution, error) {
	t, e := s.d.Tasks.GetTask(ctx, p.Tenant, in.TaskID)
	if e != nil {
		return t, execution.Execution{}, e
	}
	v, e := s.d.Executions.GetExecution(ctx, p.Tenant, in.ExecutionID)
	if e != nil {
		return t, v, e
	}
	if v.TaskID != in.TaskID || v.Generation != in.Generation || p.ID != v.Subject || p.InstanceID != v.InstanceID || p.Incarnation != v.Incarnation {
		return t, v, az.ErrForbidden
	}
	return t, v, nil
}
func (s *Service) active(ctx context.Context, t task.Task, v execution.Execution) error {
	pos, e := s.d.Flow.Read(ctx, t.Tenant, t.ID)
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
func (s *Service) Renew(ctx context.Context, token, key string, in api.ExecutionRef) (api.Snapshot, error) {
	return run(ctx, s, token, key, "execution.renew", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, v, e := s.owned(tx, p, in)
		if e != nil {
			return nil, e
		}
		if e = s.active(tx, t, v); e != nil {
			return nil, e
		}
		if v.Status != "active" {
			return nil, fault.ErrConflict
		}
		v.LeaseUntil = time.Now().Add(time.Duration(s.c.LeaseSeconds) * time.Second)
		if e = s.d.ExecutionWrites.SaveExecution(tx, v); e != nil {
			return nil, e
		}
		// A lease renewal is a heartbeat, not a business change: it must not
		// re-evaluate the flow, bump its revision or emit a task change.
		snap, e := s.snapshot(tx, t)
		if e != nil {
			return nil, e
		}
		return json.Marshal(snap)
	})
}
func (s *Service) End(ctx context.Context, token, key string, in api.ExecutionRef) (api.Snapshot, error) {
	return run(ctx, s, token, key, "execution.end", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, v, e := s.owned(tx, p, in)
		if e != nil {
			return nil, e
		}
		if e = s.active(tx, t, v); e != nil {
			return nil, e
		}
		v.Status = "ended"
		if e = s.d.ExecutionWrites.SaveExecution(tx, v); e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}
func (s *Service) Submit(ctx context.Context, token, key string, in api.Submit) (api.Snapshot, error) {
	return run(ctx, s, token, key, "result.submit", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, _, e := s.load(tx, p, in.Basis)
		if e != nil {
			return nil, e
		}
		_, v, e := s.owned(tx, p, api.ExecutionRef{TaskID: in.TaskID, ExecutionID: in.ExecutionID, Generation: in.Generation})
		if e != nil {
			return nil, e
		}
		if e = s.active(tx, t, v); e != nil {
			return nil, e
		}
		if (v.Status != "ended" && !v.Safe && time.Now().After(v.LeaseUntil)) || strings.TrimSpace(in.Body) == "" || len(in.Body) > 20000 {
			return nil, fault.ErrConflict
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
		if e = s.d.Results.RegisterSubmission(tx, delivery.Submission{Tenant: t.Tenant, TaskID: t.ID, ID: id(), RoundID: v.RoundID, ExecutionID: v.ID, Generation: v.Generation, CommitID: c.ID, Author: p.ID, BasisVersion: t.BasisVersion}); e != nil {
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
		if pos.Status != flow.PhaseActive || s.d.Accepting == nil {
			return nil, fault.ErrConflict
		}
		step, e := s.d.Flow.Step(tx, t, s.d.Accepting.Describe().Key)
		if e != nil {
			return nil, e
		}
		if e = s.d.Accepting.RecordVerdict(tx, step, p, in.Verdict); e != nil {
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
func (s *Service) invalidate(ctx context.Context, t task.Task, requestStop bool) error {
	v, e := s.d.Executions.Current(ctx, t.Tenant, t.ID)
	if errors.Is(e, fault.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	v.Writable = false
	if requestStop && !v.Safe {
		v.Status = "stop-requested"
		if v.StopRequestID == "" {
			v.StopRequestID = id()
		}
		payload, _ := json.Marshal(struct {
			ExecutionID, Challenge string
			Generation             int64
		}{v.ID, v.StopRequestID, v.Generation})
		if e = s.d.Events.AppendEvent(ctx, record.Event{Tenant: t.Tenant, Resource: t.ID, Kind: "execution.stop-requested", Payload: payload}); e != nil {
			return e
		}
	}
	return s.d.ExecutionWrites.SaveExecution(ctx, v)
}
func (s *Service) Cancel(ctx context.Context, token, key string, in api.Basis) (api.Snapshot, error) {
	return run(ctx, s, token, key, "task.cancel", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, _, e := s.load(tx, p, in)
		if e != nil {
			return nil, e
		}
		if e = s.invalidate(tx, t, true); e != nil {
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
		safe, e := s.d.Executions.SafeToExit(tx, t.Tenant, t.ID)
		if e != nil {
			return nil, e
		}
		if !safe {
			return nil, fault.ErrConflict
		}
		if e = s.invalidate(tx, t, false); e != nil {
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
func (s *Service) ProveStopped(ctx context.Context, token, key string, in api.StopEvidence) (api.Snapshot, error) {
	// No task.read requirement: a still-valid runtime credential may close its old
	// execution after participation is revoked, without receiving task contents.
	var out api.Snapshot
	b, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "execution.prove", Resource: in.TaskID}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, v, e := s.owned(tx, p, in.ExecutionRef)
		if e != nil {
			return nil, e
		}
		if p.CredentialPurpose != "runtime" {
			return nil, az.ErrForbidden
		}
		challenge := v.EndEvidenceID
		if v.StopRequestID != "" {
			challenge = v.StopRequestID
		}
		if in.Challenge != challenge {
			return nil, fault.ErrConflict
		}
		if v.Safe {
			return json.Marshal(api.Snapshot{})
		}
		if e = s.d.ExecutionWrites.RecordStopEvidence(tx, execution.StopEvidence{Tenant: t.Tenant, TaskID: t.ID, ID: id(), ExecutionID: v.ID, Generation: v.Generation, Challenge: in.Challenge, Source: "runtime", Subject: p.ID, InstanceID: p.InstanceID, Incarnation: p.Incarnation, MainStopped: in.MainStopped, ChildrenStopped: in.ChildrenStopped, ExternalSettled: in.ExternalSettled, ReceivedAt: time.Now().UTC()}); e != nil {
			return nil, e
		}
		v.Safe = in.MainStopped && in.ChildrenStopped && in.ExternalSettled
		if v.Safe {
			v.Status = "stopped"
		} else {
			v.Status = "unknown"
		}
		if e = s.d.ExecutionWrites.SaveExecution(tx, v); e != nil {
			return nil, e
		}
		pos, e := s.d.Flow.Read(tx, t.Tenant, t.ID)
		if e != nil {
			return nil, e
		}
		if pos.RoundID == v.RoundID {
			if _, e = s.finish(tx, t); e != nil {
				return nil, e
			}
		}
		return json.Marshal(api.Snapshot{})
	})
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
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
		if subject.Kind != "agent" {
			return nil, fault.ErrInput
		}
		member, e := s.d.Members.GetMember(tx, t.Tenant, subject.ID)
		if e != nil || !member.Active {
			return nil, az.ErrForbidden
		}
		safe, e := s.d.Executions.SafeToExit(tx, t.Tenant, t.ID)
		if e != nil {
			return nil, e
		}
		if !safe {
			return nil, fault.ErrConflict
		}
		if e = s.invalidate(tx, t, false); e != nil {
			return nil, e
		}
		if e = s.d.Participants.SetParticipant(tx, az.Participant{Tenant: t.Tenant, TaskID: t.ID, Subject: in.Subject}, true); e != nil {
			return nil, e
		}
		if e = s.d.ExecutionWrites.AssignAgent(tx, t.Tenant, t.ID, in.Subject); e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}

func (s *Service) Isolate(ctx context.Context, token, key string, in api.Isolation) (api.Snapshot, error) {
	return run(ctx, s, token, key, "execution.isolate", in.TaskID, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, _, e := s.load(tx, p, in.Basis)
		if e != nil {
			return nil, e
		}
		v, e := s.d.Executions.GetExecution(tx, p.Tenant, in.ExecutionID)
		if e != nil {
			return nil, e
		}
		if v.TaskID != t.ID || v.Generation != in.Generation || strings.TrimSpace(in.Reason) == "" || strings.TrimSpace(in.Scope) == "" || len(in.Scope) > 20000 || len(in.Reason) > 20000 {
			return nil, fault.ErrInput
		}
		if e = s.d.ExecutionWrites.RecordStopEvidence(tx, execution.StopEvidence{Tenant: t.Tenant, TaskID: t.ID, ID: id(), ExecutionID: v.ID, Generation: v.Generation, Subject: p.ID, Source: "operator-isolation", Reason: in.Reason, Scope: in.Scope, ReceivedAt: time.Now().UTC()}); e != nil {
			return nil, e
		}
		v.Safe = true
		v.Writable = false
		v.Status = "isolated"
		if e = s.d.ExecutionWrites.SaveExecution(tx, v); e != nil {
			return nil, e
		}
		return s.finish(tx, t)
	})
}
