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
	"github.com/lengzhao/taskloom/cap/plan"
	"github.com/lengzhao/taskloom/cap/question"
	"github.com/lengzhao/taskloom/cap/task"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	api "github.com/lengzhao/taskloom/cap/workflow"
)

type QueryDeps struct {
	Commits       commit.Reader          `json:"commits"`
	Transactions  unitofwork.Manager     `json:"transactions"`
	Tasks         task.Lister            `json:"tasks"`
	Flow          flow.Controller        `json:"flow"`
	Executions    execution.Reader       `json:"executions"`
	Results       delivery.Reader        `json:"results"`
	Reviews       acceptance.Reader      `json:"reviews"`
	Identity      identity.Authenticator `json:"identity"`
	Authorization az.Authorizer          `json:"authorization"`
	// Questions keeps the snapshot and "waiting on" in step with open requests
	// for input; the query stays a read-only aggregation of eleven readers.
	Questions question.Reader `json:"questions"`
	// Plan shows the direct parent and subtasks, filtered by what the viewer may read.
	Plan plan.Reader `json:"plan"`
	// Participants tells askers whom a question may name; reviewed as the
	// twelfth reader of this read-only aggregation, no write capability.
	Participants az.ParticipantLister `json:"participants"`
}
type Query struct{ d QueryDeps }

func init() {
	pluginkit.Register("query/workflow", func(_ struct{}, d QueryDeps) (*Query, error) { return NewQuery(d) })
}

// NewQuery builds the read-only task view aggregation.
func NewQuery(d QueryDeps) (*Query, error) {
	{
		if d.Commits == nil || d.Transactions == nil || d.Tasks == nil || d.Flow == nil || d.Executions == nil || d.Results == nil || d.Reviews == nil || d.Identity == nil || d.Authorization == nil || d.Questions == nil || d.Plan == nil || d.Participants == nil {
			return nil, fault.ErrInput
		}
		return &Query{d}, nil
	}
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
	v, e := s.d.Results.CurrentResult(ctx, t.Tenant, t.ID, pos.RoundID)
	if e == nil {
		out.Result = &v
		c, e := s.d.Commits.Get(ctx, t.Tenant, t.ID, v.CommitID)
		if e != nil {
			return out, e
		}
		out.ResultCommit = &c
		r, e := s.d.Reviews.LatestReview(ctx, t.Tenant, t.ID, v.CommitID)
		if e == nil {
			out.Review = &r
		} else if !errors.Is(e, fault.ErrNotFound) {
			return out, e
		} else if pos.State == flow.StepAccepting {
			// A result awaiting acceptance with no verdict yet is pending; nothing is stored for it.
			out.Review = &acceptance.Review{CommitID: v.CommitID, Reviewer: t.Owner, Outcome: acceptance.OutcomePending, BasisVersion: c.BasisVersion}
		}
	} else if !errors.Is(e, fault.ErrNotFound) {
		return out, e
	}
	if pos.Status == flow.PhaseActive {
		qs, e := s.d.Questions.OpenQuestions(ctx, t.Tenant, t.ID, pos.RoundID)
		if e != nil {
			return out, e
		}
		for _, q := range qs {
			c, e := s.d.Commits.Get(ctx, t.Tenant, t.ID, q.CommitID)
			if e != nil {
				return out, e
			}
			out.Questions = append(out.Questions, api.OpenQuestion{Question: q, Asker: c.Author, Body: c.Body, AskedAt: c.CreatedAt})
		}
	}
	// Nobody can deliver before someone handles the task: say what is
	// actually awaited. The flow derives its text from results alone.
	if pos.Status == flow.PhaseActive && pos.State == flow.StepExecuting && out.Assignee == "" && out.Result == nil && !pos.WaitingOnSubtasks {
		out.Position.Waiting = WaitingAssignee
	}
	out.NextActor = nextActor(out)
	return out, nil
}

// WaitingAssignee is the wait text of an active task nobody handles yet.
const WaitingAssignee = "等待负责人分配处理人"

func (s *Query) Read(ctx context.Context, token, taskID string, before int64) (api.View, error) {
	var view api.View
	if before < 0 {
		return view, fault.Input(fault.CodeInvalid, "before")
	}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		out := &view.Snapshot
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
		view.Task = t
		*out, e = s.ReadFacts(tx, t)
		if e == nil {
			out.CanAssign = s.d.Authorization.Check(tx, p, "execution.assign", taskID) == nil
			out.CanManage = s.d.Authorization.Check(tx, p, "task.cancel", taskID) == nil
			blocked := map[string]api.Reason{}
			why := func(action, code string, relevant bool) bool {
				if code != "" && relevant {
					blocked[action] = api.Reason{Code: code, Message: fault.Message(code)}
				}
				return code == ""
			}
			worker := p.Kind == "agent" || out.Assignee == p.ID
			out.CanReview = why(api.ActionReview, reviewBlock(p, t, *out), out.Review != nil)
			out.CanOpen = why(api.ActionOpen, s.openBlock(tx, p, t, *out), worker)
			if out.Execution != nil {
				code := submitBlock(p, t, out.Position, *out.Execution)
				if code == "" {
					if e := s.d.Flow.CheckSubmit(tx, t); errors.Is(e, fault.ErrConflict) {
						code = api.ReasonChildrenPending
					} else if e != nil {
						return e
					}
				}
				out.CanSubmit = why(api.ActionSubmit, code, worker)
			}
			if e = s.relations(tx, p, t, out); e != nil {
				return e
			}
			view.CanEdit = s.d.Authorization.Check(tx, p, "task.edit", taskID) == nil
			view.CanComment = s.d.Authorization.Check(tx, p, "commit.append", taskID) == nil
			// One extra commit tells whether older ones precede the page.
			if view.Commits, e = s.d.Commits.Before(tx, t.Tenant, t.ID, before, api.TimelinePage+1); e != nil {
				return e
			}
			if len(view.Commits) > api.TimelinePage {
				view.Commits, view.Earlier = view.Commits[1:], true
			}
			out.Viewer = p.ID
			if out.Participants, e = s.d.Participants.ListParticipants(tx, t.Tenant, t.ID); e != nil {
				return e
			}
			if len(blocked) > 0 {
				out.Blocked = blocked
			}
		}
		return e
	})
	return view, e
}

// relations fills the direct parent and subtasks the viewer may read.
func (s *Query) relations(ctx context.Context, p identity.Principal, t task.Task, out *api.Snapshot) error {
	view := func(id string, required bool) (*api.PlanTask, error) {
		if s.d.Authorization.Check(ctx, p, "task.read", id) != nil {
			return nil, nil
		}
		other, e := s.d.Tasks.GetTask(ctx, t.Tenant, id)
		if e != nil {
			return nil, e
		}
		pos, e := s.d.Flow.Read(ctx, other)
		if e != nil {
			return nil, e
		}
		return &api.PlanTask{TaskID: other.ID, Title: other.Title, Required: required, Status: pos.Status, State: pos.State}, nil
	}
	l, e := s.d.Plan.Parent(ctx, t.Tenant, t.ID)
	if e == nil {
		if out.Parent, e = view(l.ParentID, l.Required); e != nil {
			return e
		}
	} else if !errors.Is(e, fault.ErrNotFound) {
		return e
	}
	children, e := s.d.Plan.Children(ctx, t.Tenant, t.ID)
	if e != nil {
		return e
	}
	for _, c := range children {
		v, e := view(c.ChildID, c.Required)
		if e != nil {
			return e
		}
		if v == nil {
			out.HiddenRequired = out.HiddenRequired || c.Required
			continue
		}
		out.Children = append(out.Children, *v)
	}
	return nil
}

// List derives every list row from the same facts as Read; it keeps no
// inbox or overview records, so it cannot drift from the tasks' real state.
func (s *Query) List(ctx context.Context, token string, f api.ListFilter) ([]api.Summary, error) {
	out := []api.Summary{}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.d.Identity.Authenticate(tx, token)
		if e != nil {
			return e
		}
		// A deactivated member reads nothing, not an empty list.
		if e = s.d.Authorization.Check(tx, p, "tasks.list", p.Tenant); e != nil {
			return e
		}
		ts, e := task.Pick(tx, s.d.Tasks, p.Tenant, f.IDs)
		if e != nil {
			return e
		}
		blocked := []api.Summary{}
		for _, t := range ts {
			if s.d.Authorization.Check(tx, p, "task.read", t.ID) != nil {
				continue
			}
			v, e := s.ReadFacts(tx, t)
			if e != nil {
				return e
			}
			row := api.Summary{Task: t, Position: v.Position, Assignee: v.Assignee, NextActor: v.NextActor, ForMe: waitsOn(p, t, v)}
			l, e := s.d.Plan.Parent(tx, t.Tenant, t.ID)
			if e == nil {
				row.ParentID = l.ParentID
			} else if !errors.Is(e, fault.ErrNotFound) {
				return e
			}
			c, e := s.d.Commits.Last(tx, t.Tenant, t.ID)
			if e == nil {
				row.UpdatedAt = c.CreatedAt
			} else if !errors.Is(e, fault.ErrNotFound) {
				return e
			}
			switch {
			case !f.Mine || row.ForMe:
				out = append(out, row)
			case v.Position.WaitingOnSubtasks && (v.Assignee == p.ID || (p.Kind == "human" && p.ID == t.Owner)):
				blocked = append(blocked, row)
			}
		}
		out = append(out, blocked...)
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
	case p.Kind != "human" && (p.Kind != "agent" || p.CredentialID == ""):
		return api.ReasonCredential
	case out.Assignee != p.ID:
		return api.ReasonNotAssigned
	case out.Execution != nil && out.Execution.Writable && out.Execution.RoundID == out.Position.RoundID && out.Execution.BasisVersion == t.BasisVersion && ownExecution(p, *out.Execution):
		// Already claimed by this caller on the current basis: continue it rather than claim again.
		return api.ReasonExecutionInProgress
	}
	if _, e := s.d.Flow.CheckOpen(ctx, t); e != nil {
		return api.ReasonStepNotOpen
	}
	return ""
}

// agentCredential is the credential an execution by p is bound to: an Agent's
// own credential, or none for a person.
func agentCredential(p identity.Principal) string {
	if p.Kind == "agent" {
		return p.CredentialID
	}
	return ""
}

func ownExecution(p identity.Principal, v execution.Execution) bool {
	return v.Subject == p.ID && v.CredentialID == agentCredential(p)
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
	case pos.WaitingOnSubtasks:
		// The next move is on the required subtasks, which reach their own
		// actors; nobody on this task is waited on until they complete.
		return ""
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
