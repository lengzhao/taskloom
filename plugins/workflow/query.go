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
	"github.com/lengzhao/taskloom/cap/question"
	"github.com/lengzhao/taskloom/cap/task"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	api "github.com/lengzhao/taskloom/cap/workflow"
)

type QueryDeps struct {
	Commits       commit.Lookup          `json:"commits"`
	Transactions  unitofwork.Manager     `json:"transactions"`
	Tasks         task.Lister            `json:"tasks"`
	Flow          flow.Controller        `json:"flow"`
	Executions    execution.Reader       `json:"executions"`
	Results       delivery.Reader        `json:"results"`
	Reviews       acceptance.Reader      `json:"reviews"`
	Identity      identity.Authenticator `json:"identity"`
	Authorization az.Authorizer          `json:"authorization"`
	// Questions keeps the snapshot and "waiting on" in step with open requests
	// for input; the query stays a read-only aggregation of ten readers.
	Questions question.Reader `json:"questions"`
}
type Query struct{ d QueryDeps }

func init() {
	pluginkit.Register("query/workflow", func(_ struct{}, d QueryDeps) (*Query, error) {
		if d.Commits == nil || d.Transactions == nil || d.Tasks == nil || d.Flow == nil || d.Executions == nil || d.Results == nil || d.Reviews == nil || d.Identity == nil || d.Authorization == nil || d.Questions == nil {
			return nil, fault.ErrInput
		}
		return &Query{d}, nil
	})
}
func (s *Query) ReadFacts(ctx context.Context, t task.Task) (api.Snapshot, error) {
	pos, e := s.d.Flow.Read(ctx, t)
	out := api.Snapshot{Position: pos}
	assigned, assignedErr := s.d.Executions.Assignee(ctx, t.Tenant, t.ID)
	if assignedErr == nil {
		out.Assignee = assigned
	} else if !errors.Is(assignedErr, fault.ErrNotFound) {
		return out, assignedErr
	}
	if e != nil {
		return out, e
	}
	ex, e := s.d.Executions.Current(ctx, t.Tenant, t.ID)
	if e == nil {
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
		r, e := s.d.Reviews.GetReview(ctx, t.Tenant, t.ID, pos.RoundID, v.ID)
		if e == nil {
			out.Review = &r
		} else if !errors.Is(e, fault.ErrNotFound) {
			return out, e
		} else if pos.State == flow.StepAccepting {
			// A result awaiting acceptance with no verdict yet is pending; nothing is stored for it.
			out.Review = &acceptance.Review{Tenant: t.Tenant, TaskID: t.ID, RoundID: pos.RoundID, SubmissionID: v.ID, CommitID: v.CommitID, Reviewer: t.Owner, Outcome: acceptance.OutcomePending, BasisVersion: v.BasisVersion}
		}
	} else if !errors.Is(e, fault.ErrNotFound) {
		return out, e
	}
	if pos.Status == flow.PhaseActive {
		qs, e := s.d.Questions.OpenQuestions(ctx, t.Tenant, t.ID, pos.RoundID)
		if e != nil {
			return out, e
		}
		if len(qs) > 0 {
			out.Questions = qs
		}
	}
	out.NextActor = nextActor(out)
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
			out.ViewerKind = p.Kind
			out.CanAssign = s.d.Authorization.Check(tx, p, "execution.assign", taskID) == nil
			out.CanManage = s.d.Authorization.Check(tx, p, "task.cancel", taskID) == nil
			blocked := map[string]string{}
			why := func(action, code string, relevant bool) bool {
				if code != "" && relevant {
					blocked[action] = code
				}
				return code == ""
			}
			worker := p.Kind == "agent" || out.Assignee == p.ID
			out.CanReview = why(api.ActionReview, reviewBlock(p, t, out), out.Review != nil)
			out.CanOpen = why(api.ActionOpen, s.openBlock(tx, p, t, out), worker)
			if out.Execution != nil {
				out.CanSubmit = why(api.ActionSubmit, submitBlock(p, t, out.Position, *out.Execution), worker)
			}
			if len(blocked) > 0 {
				out.Blocked = blocked
			}
		}
		return e
	})
	return out, e
}

// Inbox derives "waiting on me" from the same facts as Read; it keeps no
// separate inbox records, so it cannot drift from the task's real state.
func (s *Query) Inbox(ctx context.Context, token string) ([]api.Pending, error) {
	out := []api.Pending{}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.d.Identity.Authenticate(tx, token)
		if e != nil {
			return e
		}
		ts, e := s.d.Tasks.ListTasks(tx, p.Tenant)
		if e != nil {
			return e
		}
		for _, t := range ts {
			if s.d.Authorization.Check(tx, p, "task.read", t.ID) != nil {
				continue
			}
			v, e := s.ReadFacts(tx, t)
			if e != nil {
				return e
			}
			if waitsOn(p, t, v) {
				out = append(out, api.Pending{TaskID: t.ID, Title: t.Title, NextActor: v.NextActor})
			}
		}
		return nil
	})
	return out, e
}

func waitsOn(p identity.Principal, t task.Task, v api.Snapshot) bool {
	switch v.NextActor {
	case api.ActorOwner:
		return p.Kind == "human" && p.ID == t.Owner
	case api.ActorReviewer:
		return p.Kind == "human" && v.Review != nil && p.ID == v.Review.Reviewer
	case api.ActorAssignee:
		return v.Assignee == p.ID
	case api.ActorAddressee:
		for _, q := range v.Questions {
			if q.Addressee == p.ID {
				return true
			}
		}
	}
	return false
}

// Each *Block returns the first reason an action is unavailable, or "" when the
// matching Can* flag is true, so flags and explanations cannot drift apart.
func reviewBlock(p identity.Principal, t task.Task, out api.Snapshot) string {
	switch {
	case out.Review == nil:
		return api.ReasonStepNotOpen
	case out.Position.Status != flow.PhaseActive:
		return api.ReasonTaskNotActive
	case p.Kind != "human" || p.ID != t.Owner:
		return api.ReasonNotReviewer
	}
	return ""
}

// openBlock reuses the controller's OpenExecution check instead of guessing from
// state names, so the UI flag and the Open command cannot drift apart.
func (s *Query) openBlock(ctx context.Context, p identity.Principal, t task.Task, out api.Snapshot) string {
	switch {
	case out.Position.Status != flow.PhaseActive:
		return api.ReasonTaskNotActive
	case p.Kind != "human" && (p.Kind != "agent" || p.InstanceID == "" || p.Incarnation == ""):
		return api.ReasonCredential
	case out.Assignee != p.ID:
		return api.ReasonNotAssigned
	case out.Execution != nil && out.Execution.Writable && out.Execution.RoundID == out.Position.RoundID && ownExecution(p, *out.Execution):
		// Already claimed by this caller: continue it rather than claim again.
		return api.ReasonExecutionInProgress
	}
	if _, e := s.d.Flow.CheckOpen(ctx, t); e != nil {
		return api.ReasonStepNotOpen
	}
	return ""
}

func ownExecution(p identity.Principal, v execution.Execution) bool {
	if v.Manual {
		return p.Kind == "human" && v.Subject == p.ID
	}
	return p.Kind == "agent" && v.Subject == p.ID && v.InstanceID == p.InstanceID && v.Incarnation == p.Incarnation
}

func submitBlock(p identity.Principal, t task.Task, pos flow.Position, v execution.Execution) string {
	switch {
	case pos.Status != flow.PhaseActive:
		return api.ReasonTaskNotActive
	case !ownExecution(p, v):
		return api.ReasonNotYourExecution
	case !v.Writable:
		return api.ReasonExecutionFenced
	case v.RoundID != pos.RoundID:
		return api.ReasonRoundChanged
	case v.BasisVersion != t.BasisVersion:
		return api.ReasonBasisChanged
	}
	return ""
}

// nextActor is viewer independent and derived only from facts already read.
func nextActor(out api.Snapshot) api.Actor {
	pos := out.Position
	switch {
	case pos.Status != flow.PhaseActive:
		return ""
	case len(out.Questions) > 0:
		// Whoever was asked must answer before the task can complete.
		return api.ActorAddressee
	case pos.State == flow.StepReady:
		return api.ActorOwner
	case pos.State == flow.StepAccepting && out.Review != nil && out.Review.Outcome == acceptance.OutcomePending:
		return api.ActorReviewer
	case pos.Waiting == "":
		return ""
	case out.Assignee == "":
		return api.ActorOwner
	}
	return api.ActorAssignee
}
