package workflow

import (
	"context"
	"errors"
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/task"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	api "github.com/lengzhao/taskloom/cap/workflow"
	"time"
)

type QueryDeps struct {
	Commits       commit.Lookup          `json:"commits"`
	Transactions  unitofwork.Manager     `json:"transactions"`
	Tasks         task.Reader            `json:"tasks"`
	Flow          flow.Controller        `json:"flow"`
	Executions    execution.Reader       `json:"executions"`
	Results       delivery.Reader        `json:"results"`
	Reviews       acceptance.Reader      `json:"reviews"`
	Identity      identity.Authenticator `json:"identity"`
	Authorization az.Authorizer          `json:"authorization"`
}
type Query struct{ d QueryDeps }

func init() {
	pluginkit.Register("query/workflow", func(_ struct{}, d QueryDeps) (*Query, error) {
		if d.Commits == nil || d.Transactions == nil || d.Tasks == nil || d.Flow == nil || d.Executions == nil || d.Results == nil || d.Reviews == nil || d.Identity == nil || d.Authorization == nil {
			return nil, fault.ErrInput
		}
		return &Query{d}, nil
	})
}
func (s *Query) ReadFacts(ctx context.Context, t task.Task) (api.Snapshot, error) {
	pos, e := s.d.Flow.Read(ctx, t.Tenant, t.ID)
	out := api.Snapshot{Position: pos}
	safe, safeErr := s.d.Executions.SafeToExit(ctx, t.Tenant, t.ID)
	if safeErr != nil {
		return out, safeErr
	}
	out.SafeToExit = safe
	assigned, assignedErr := s.d.Executions.AssignedAgent(ctx, t.Tenant, t.ID)
	if assignedErr == nil {
		out.AssignedAgent = assigned
	} else if !errors.Is(assignedErr, fault.ErrNotFound) {
		return out, assignedErr
	}
	if e != nil {
		return out, e
	}
	ex, e := s.d.Executions.Current(ctx, t.Tenant, t.ID)
	if e == nil {
		if ex.Status == "active" && time.Now().After(ex.LeaseUntil) {
			ex.Status = "unknown"
		}
		out.Execution = &ex
	} else if !errors.Is(e, fault.ErrNotFound) {
		return out, e
	}
	v, e := s.d.Results.CurrentSubmission(ctx, t.Tenant, t.ID, pos.RoundID)
	if e == nil {
		out.Submission = &v
		c, e := s.d.Commits.Get(ctx, t.Tenant, t.ID, v.CommitID)
		if e != nil {
			return out, e
		}
		out.ResultCommit = &c
		for _, entry := range pos.Entries {
			r, e := s.d.Reviews.GetReview(ctx, t.Tenant, t.ID, pos.RoundID, entry.ID, v.ID)
			if e == nil {
				out.Review = &r
			} else if !errors.Is(e, fault.ErrNotFound) {
				return out, e
			}
		}
	} else if !errors.Is(e, fault.ErrNotFound) {
		return out, e
	}
	return out, nil
}
func (s *Query) Read(ctx context.Context, token, taskID string) (api.Snapshot, error) {
	var out api.Snapshot
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.d.Identity.Authenticate(tx, token)
		if e != nil {
			return e
		}
		if e = s.d.Authorization.Check(tx, p, "task.read", taskID); e != nil {
			return e
		}
		t, e := s.d.Tasks.GetTask(tx, p.Tenant, taskID)
		if e != nil {
			return e
		}
		out, e = s.ReadFacts(tx, t)
		if e == nil {
			out.CanIsolate = s.d.Authorization.Check(tx, p, "execution.isolate", taskID) == nil
			out.CanManage = s.d.Authorization.Check(tx, p, "task.cancel", taskID) == nil
			out.CanReview = p.Kind == "human" && p.ID == t.Owner && out.Review != nil && out.Position.Status == flow.PhaseActive
			out.CanOpen = p.Kind == "agent" && p.CredentialPurpose == "business" && out.AssignedAgent == p.ID && out.SafeToExit && s.canOpen(tx, t)
			if out.Execution != nil {
				v := out.Execution
				raw, _ := s.d.Executions.Current(tx, t.Tenant, t.ID)
				out.CanRenew = raw.Status == "active" && raw.Writable && raw.Subject == p.ID && raw.InstanceID == p.InstanceID && raw.Incarnation == p.Incarnation && p.CredentialPurpose == "business" && out.Position.Status == flow.PhaseActive && raw.RoundID == out.Position.RoundID
				out.CanSubmit = out.Position.Status == flow.PhaseActive && v.Writable && v.Subject == p.ID && v.InstanceID == p.InstanceID && v.Incarnation == p.Incarnation && v.RoundID == out.Position.RoundID && v.BasisVersion == t.BasisVersion && p.CredentialPurpose == "business" && (v.Safe || v.Status == "ended" || time.Now().Before(v.LeaseUntil))
			}
		}
		return e
	})
	return out, e
}

// canOpen reuses the controller's OpenExecution check instead of guessing from
// state names, so the UI flag and the Open command cannot drift apart.
func (s *Query) canOpen(ctx context.Context, t task.Task) bool {
	_, e := s.d.Flow.CheckOpen(ctx, t)
	return e == nil
}
