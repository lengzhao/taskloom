// Package workspace composes M04 named use cases through capability interfaces.
package workspace

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lengzhao/taskloom/cap/catalog"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"strconv"
	"strings"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/plan"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	api "github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/runtime/command"
)

type Deps struct {
	Catalog       catalog.ProjectReader  `json:"catalog"`
	Transactions  unitofwork.Manager     `json:"transactions"`
	Tasks         task.Store             `json:"tasks"`
	Commits       commit.Log             `json:"commits"`
	Flow          flow.Controller        `json:"flow"`
	Identity      identity.Authenticator `json:"identity"`
	Authorization az.Authorizer          `json:"authorization"`
	Commands      capcommand.Handler     `json:"commands"`
	Members       az.MemberDirectory     `json:"members"`
	Participants  az.ParticipantWriter   `json:"participants"`
	Events        record.EventLog        `json:"events"`
	// Progress lets an Agent's comment count as progress on its own execution.
	Progress execution.Progress `json:"progress"`
	// Placer places a task created under a parent in the same transaction.
	// Thirteen fields, reviewed: the placement rule stays in the plan module;
	// this field only lets creation and placement be one atomic command.
	Placer plan.Placer `json:"placer"`
}
type Service struct {
	d Deps
}

func init() {
	pluginkit.Register("workspace/tasks", func(_ struct{}, d Deps) (*Service, error) { return New(d) })
}
func New(d Deps) (*Service, error) {
	if d.Catalog == nil || d.Transactions == nil || d.Tasks == nil || d.Commits == nil || d.Flow == nil || d.Members == nil || d.Participants == nil || d.Identity == nil || d.Authorization == nil || d.Events == nil || d.Progress == nil || d.Placer == nil {
		return nil, errors.New("task capabilities required")
	}
	if d.Commands == nil {
		return nil, errors.New("command lifecycle required")
	}
	return &Service{d: d}, nil
}
func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return fmt.Sprintf("%x", b)
}

func valid(title, goal, criteria string) bool {
	return strings.TrimSpace(title) != "" && len(title) <= 200 && len(goal) <= 20000 && len(criteria) <= 20000
}
func (s *Service) allowed(ctx context.Context, p identity.Principal, action, id string) bool {
	return s.d.Authorization.Check(ctx, p, action, id) == nil
}
func (s *Service) append(ctx context.Context, p identity.Principal, t task.Task, kind, body, ref string) (commit.Commit, error) {
	c := commit.Commit{Tenant: p.Tenant, TaskID: t.ID, ID: id(), Author: p.ID, OwnerID: p.OwnerID, Kind: kind, Body: body, RefID: ref, BasisVersion: t.BasisVersion}
	if e := s.d.Commits.Append(ctx, c); e != nil {
		return c, e
	}
	return s.d.Commits.Get(ctx, p.Tenant, t.ID, c.ID)
}
func (s *Service) event(ctx context.Context, t task.Task) error {
	return s.d.Events.AppendEvent(ctx, record.Event{Tenant: t.Tenant, Resource: t.ID, Kind: record.EventTaskChanged})
}
func (s *Service) Create(ctx context.Context, token, key string, in api.Create) (api.Ref, error) {
	var out api.Ref
	if in.Required && in.ParentID == "" {
		return out, fault.ErrInput // Required describes a relation that would not exist
	}
	p, e := s.actor(ctx, token)
	if e != nil {
		return out, e
	}
	req := capcommand.Request{Token: token, Key: key, Operation: "task.create", Resource: p.Tenant}
	if in.ParentID != "" {
		// Creating under a parent changes the parent's plan, so it also needs
		// planning rights there; checked again before any replay.
		req.Authorize = func(tx context.Context, actor identity.Principal) error {
			if e := s.d.Authorization.Check(tx, actor, "task.create", actor.Tenant); e != nil {
				return e
			}
			return s.d.Authorization.Check(tx, actor, "task.plan", in.ParentID)
		}
	}
	b, e := command.Run(ctx, s.d.Commands, req, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if p.Kind != "human" || !valid(in.Title, in.Goal, in.Criteria) {
			return nil, fault.ErrInput
		}
		if e := s.projectOpen(tx, p.Tenant, in.ProjectID); e != nil {
			return nil, e
		}
		t := task.Task{ProjectID: in.ProjectID, Tenant: p.Tenant, ID: id(), Title: in.Title, Goal: in.Goal, Criteria: in.Criteria, Owner: p.ID, BasisVersion: 1}
		if e := s.d.Tasks.CreateTask(tx, t); e != nil {
			return nil, e
		}
		if _, e := s.d.Flow.Initialize(tx, t); e != nil {
			return nil, e
		}
		if _, e := s.append(tx, p, t, "task.created", in.Title, ""); e != nil {
			return nil, e
		}
		if in.ParentID != "" {
			// The placement announces the new task and its parent itself.
			if e := s.d.Placer.Apply(tx, p, plan.Placement{ChildID: t.ID, ParentID: in.ParentID, Required: in.Required}); e != nil {
				return nil, e
			}
		} else if e := s.event(tx, t); e != nil {
			return nil, e
		}
		return json.Marshal(api.Ref{TaskID: t.ID})
	})
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}
func (s *Service) Edit(ctx context.Context, token, key string, in api.Edit) (api.Ref, error) {
	var out api.Ref
	b, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "task.edit", Resource: in.TaskID}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, e := s.d.Tasks.GetTask(tx, p.Tenant, in.TaskID)
		if e != nil {
			return nil, e
		}
		if t.BasisVersion != in.BasisVersion {
			return nil, fault.ErrConflict
		}
		if !valid(in.Title, in.Goal, in.Criteria) {
			return nil, fault.ErrInput
		}
		// The basis may change at any time on an active task: a claim or result
		// made on the old basis can no longer submit or complete.
		if pos, e := s.d.Flow.Read(tx, t); e != nil {
			return nil, e
		} else if pos.Status != flow.PhaseActive {
			return nil, fault.ErrConflict
		}
		// Saving the same text changes nothing, so no version advances and no
		// claim or result is invalidated.
		if in.Title == t.Title && in.Goal == t.Goal && in.Criteria == t.Criteria {
			return json.Marshal(api.Ref{TaskID: t.ID})
		}
		t, e = s.d.Tasks.UpdateBasis(tx, task.BasisChange{Tenant: p.Tenant, TaskID: in.TaskID, Title: in.Title, Goal: in.Goal, Criteria: in.Criteria})
		if e != nil {
			return nil, e
		}
		if _, e = s.d.Flow.Refresh(tx, t); e != nil {
			return nil, e
		}
		body, _ := json.Marshal(in)
		if _, e = s.append(tx, p, t, "task.basis", string(body), ""); e != nil {
			return nil, e
		}
		if e = s.event(tx, t); e != nil {
			return nil, e
		}
		return json.Marshal(api.Ref{TaskID: t.ID})
	})
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}
func (s *Service) Comment(ctx context.Context, token, key string, in api.Comment) (api.Ref, error) {
	var out api.Ref
	b, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "commit.append", Resource: in.TaskID}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if strings.TrimSpace(in.Body) == "" || len(in.Body) > 20000 {
			return nil, fault.ErrInput
		}
		t, e := s.d.Tasks.GetTask(tx, p.Tenant, in.TaskID)
		if e != nil {
			return nil, e
		}
		if in.RefID != "" {
			old, e := s.d.Commits.Get(tx, p.Tenant, t.ID, in.RefID)
			if e != nil {
				return nil, e
			}
			if old.Author != p.ID || old.Kind != "comment" {
				return nil, az.ErrForbidden
			}
		}
		c, e := s.append(tx, p, t, "comment", in.Body, in.RefID)
		if e != nil {
			return nil, e
		}
		if e = s.d.Progress.RecordProgress(tx, p, t.Tenant, t.ID); e != nil {
			return nil, e
		}
		if e = s.event(tx, t); e != nil {
			return nil, e
		}
		return json.Marshal(api.Ref{TaskID: t.ID, CommitID: c.ID})
	})
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}
func (s *Service) detail(ctx context.Context, p identity.Principal, t task.Task, after int64) (api.Detail, error) {
	var d api.Detail
	if e := s.d.Authorization.Check(ctx, p, "task.read", t.ID); e != nil {
		return d, e
	}
	pos, e := s.d.Flow.Read(ctx, t)
	if e != nil {
		return d, e
	}
	cs, e := s.d.Commits.List(ctx, t.Tenant, t.ID, after, 100)
	return api.Detail{Task: t, Position: pos, Commits: cs, CanEdit: s.allowed(ctx, p, "task.edit", t.ID), CanComment: s.allowed(ctx, p, "commit.append", t.ID)}, e
}
func (s *Service) Get(ctx context.Context, token, id string, after int64) (api.Detail, error) {
	var out api.Detail
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		if !s.allowed(tx, p, "task.read", id) {
			return az.ErrForbidden
		}
		t, e := s.d.Tasks.GetTask(tx, p.Tenant, id)
		if e != nil {
			return e
		}
		out, e = s.detail(tx, p, t, after)
		return e
	})
	return out, e
}
func (s *Service) List(ctx context.Context, token string) ([]api.Detail, error) {
	out := []api.Detail{}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		ts, e := s.d.Tasks.ListTasks(tx, p.Tenant)
		if e != nil {
			return e
		}
		for _, t := range ts {
			d, e := s.detail(tx, p, t, 0)
			if errors.Is(e, az.ErrForbidden) {
				continue
			}
			if e != nil {
				return e
			}
			d.Commits = nil
			out = append(out, d)
		}
		return nil
	})
	return out, e
}

// Changes reads the change feed after a cursor. The cursor is the plain event
// sequence: every event is re-authorized for the caller when read, so a cursor
// grants nothing and needs no signature or expiry. An empty cursor returns the
// current position without replaying history.
func (s *Service) Changes(ctx context.Context, token, raw string) (api.Page, error) {
	out := api.Page{Changes: []api.Change{}}
	start := raw == ""
	var after int64
	if !start {
		n, e := strconv.ParseInt(raw, 10, 64)
		if e != nil || n < 0 {
			return out, fault.ErrInput
		}
		after = n
	}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		for {
			es, e := s.d.Events.ReadEvents(tx, p.Tenant, after, 100)
			if e != nil {
				return e
			}
			for _, event := range es {
				after = event.Sequence
				if !start && event.Kind == record.EventTaskChanged && s.allowed(tx, p, "task.read", event.Resource) {
					out.Changes = append(out.Changes, api.Change{TaskID: event.Resource, Kind: event.Kind})
				}
				if !start && event.Kind == record.EventAccessChanged {
					out.Changes = append(out.Changes, api.Change{Kind: event.Kind})
				}
			}
			if !start || len(es) < 100 {
				break
			}
		}
		out.Cursor = strconv.FormatInt(after, 10)
		return nil
	})
	return out, e
}
func (s *Service) actor(ctx context.Context, token string) (identity.Principal, error) {
	p, e := s.d.Identity.Authenticate(ctx, token)
	if e != nil {
		return p, e
	}
	m, e := s.d.Members.GetMember(ctx, p.Tenant, p.ID)
	if e != nil || !m.Active || (m.Role != "admin" && m.Role != "member") {
		return p, az.ErrForbidden
	}
	return p, nil
}
func (s *Service) SetParticipant(ctx context.Context, token, key string, in api.Participation) error {
	_, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "participant.set", Resource: in.TaskID}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		t, e := s.d.Tasks.GetTask(tx, p.Tenant, in.TaskID)
		if e != nil {
			return nil, e
		}
		if in.Present {
			m, e := s.d.Members.GetMember(tx, p.Tenant, in.Subject)
			if e != nil || !m.Active {
				return nil, az.ErrForbidden
			}
		}
		if e = s.d.Participants.SetParticipant(tx, az.Participant{Tenant: p.Tenant, TaskID: t.ID, Subject: in.Subject}, in.Present); e != nil {
			return nil, e
		}
		// Participation changes who may read the task, so every viewer refreshes access.
		return nil, s.d.Events.AppendEvent(tx, record.Event{Tenant: t.Tenant, Resource: t.ID, Kind: record.EventAccessChanged})
	})
	return e
}

// People pages the tenant's active members (subject, kind, owner) for any
// active member, Agents included: it is the one selector for addressees,
// assignees, participants and project owners. It exposes no credentials.
func (s *Service) People(ctx context.Context, token string, q az.MemberQuery) (api.PeoplePage, error) {
	out := api.PeoplePage{Items: []az.MemberOption{}}
	if q.Kind != "" && q.Kind != "human" && q.Kind != "agent" {
		return out, fault.ErrInput
	}
	switch {
	case q.Limit <= 0:
		q.Limit = api.PeopleDefault
	case q.Limit > api.PeopleMax:
		q.Limit = api.PeopleMax
	}
	size := q.Limit
	q.Limit++ // one extra row tells whether another page follows
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		items, e := s.d.Members.ListActiveMembers(tx, p.Tenant, q)
		if e != nil {
			return e
		}
		if len(items) > size {
			items = items[:size]
			out.Next = items[size-1].Subject
		}
		out.Items = items
		return nil
	})
	return out, e
}

func (s *Service) Viewer(ctx context.Context, token string) (api.Viewer, error) {
	var out api.Viewer
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.d.Identity.Authenticate(tx, token)
		if e != nil {
			return e
		}
		member, e := s.d.Members.GetMember(tx, p.Tenant, p.ID)
		if e != nil || !member.Active {
			return az.ErrForbidden
		}
		out = api.Viewer{Tenant: p.Tenant, Subject: p.ID, Kind: p.Kind, CanAdmin: s.d.Authorization.Check(tx, p, "member.set", p.Tenant) == nil}
		return nil
	})
	return out, e
}
func (s *Service) Members(ctx context.Context, token string) ([]az.DirectoryMember, error) {
	out := []az.DirectoryMember{}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.d.Identity.Authenticate(tx, token)
		if e != nil {
			return e
		}
		if e = s.d.Authorization.Check(tx, p, "member.set", p.Tenant); e != nil {
			return e
		}
		out, e = s.d.Members.ListMembers(tx, p.Tenant)
		return e
	})
	return out, e
}

func (s *Service) projectOpen(ctx context.Context, tenant, id string) error {
	if id == "" {
		return nil
	}
	p, e := s.d.Catalog.GetProject(ctx, tenant, id)
	if e != nil {
		return e
	}
	if p.Archived {
		return fault.ErrConflict
	}
	return nil
}

func (s *Service) SetProject(ctx context.Context, token, key string, in api.ProjectMove) error {
	_, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "task.project", Resource: in.TaskID}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if e := s.projectOpen(tx, p.Tenant, in.ProjectID); e != nil {
			return nil, e
		}
		t, e := s.d.Tasks.SetProject(tx, p.Tenant, in.TaskID, in.ProjectID)
		if e != nil {
			return nil, e
		}
		if _, e = s.append(tx, p, t, "task.project", in.ProjectID, ""); e != nil {
			return nil, e
		}
		return nil, s.event(tx, t)
	})
	return e
}
