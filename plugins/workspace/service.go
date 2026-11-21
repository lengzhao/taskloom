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
	"github.com/lengzhao/taskloom/domain/attachment"
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
	// Executor is the caller's own execution of a task: an Agent's comment
	// counts as progress on it, and an Agent splitting the task it executes
	// keeps it across the basis change it made.
	Executor execution.Executor `json:"executor"`
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
	if d.Catalog == nil || d.Transactions == nil || d.Tasks == nil || d.Commits == nil || d.Flow == nil || d.Members == nil || d.Participants == nil || d.Identity == nil || d.Authorization == nil || d.Events == nil || d.Executor == nil || d.Placer == nil {
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
	return s.appendCommit(ctx, p, t, commit.Commit{Kind: kind, Body: body, RefID: ref})
}

// appendCommit fills in the commit's task, author and basis, then appends it.
func (s *Service) appendCommit(ctx context.Context, p identity.Principal, t task.Task, c commit.Commit) (commit.Commit, error) {
	c.Tenant, c.TaskID, c.ID, c.Author, c.OwnerID, c.BasisVersion = p.Tenant, t.ID, id(), p.ID, p.OwnerID, t.BasisVersion
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
	if (in.Required || in.Owner != "") && in.ParentID == "" {
		// Required describes a relation that would not exist; a top-level
		// task is owned by the person who creates it.
		return out, fault.ErrInput
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
			if actor.Kind == "agent" {
				// An Agent splits only the task it executes; the command
				// checks it holds the current execution.
				return s.d.Authorization.Check(tx, actor, "task.split", in.ParentID)
			}
			if e := s.d.Authorization.Check(tx, actor, "task.create", actor.Tenant); e != nil {
				return e
			}
			return s.d.Authorization.Check(tx, actor, "task.plan", in.ParentID)
		}
	}
	b, e := command.Run(ctx, s.d.Commands, req, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if !valid(in.Title, in.Goal, in.Criteria) {
			return nil, fault.ErrInput
		}
		owner, held, e := s.creation(tx, p, in)
		if e != nil {
			return nil, e
		}
		if e := s.projectOpen(tx, p.Tenant, in.ProjectID); e != nil {
			return nil, e
		}
		t := task.Task{ProjectID: in.ProjectID, Tenant: p.Tenant, ID: id(), Title: in.Title, Goal: in.Goal, Criteria: in.Criteria, Owner: owner, BasisVersion: 1}
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
			// A creator who does not own the subtask follows it as a participant.
			if owner != p.ID {
				if e := s.d.Participants.SetParticipant(tx, az.Participant{Tenant: p.Tenant, TaskID: t.ID, Subject: p.ID}, true); e != nil {
					return nil, e
				}
			}
			if held != nil {
				if e := s.d.Executor.Follow(tx, *held); e != nil {
					return nil, e
				}
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

// creation decides who owns a new task. A person owns a top-level task they
// create. A subtask may name any active human member as owner; otherwise its
// creator owns it, or, for an Agent, the parent's owner. An Agent creates
// subtasks only under the task it holds the current execution of, returned
// so that execution can follow the parent's basis.
func (s *Service) creation(ctx context.Context, p identity.Principal, in api.Create) (string, *execution.Execution, error) {
	var held *execution.Execution
	owner := p.ID
	switch p.Kind {
	case "human":
	case "agent":
		if in.ParentID == "" {
			return "", nil, az.ErrForbidden
		}
		v, e := s.d.Executor.Hold(ctx, p, p.Tenant, in.ParentID)
		if e != nil {
			return "", nil, e
		}
		parent, e := s.d.Tasks.GetTask(ctx, p.Tenant, in.ParentID)
		if e != nil {
			return "", nil, e
		}
		held, owner = &v, parent.Owner
	default:
		return "", nil, az.ErrForbidden
	}
	if in.Owner == "" {
		return owner, held, nil
	}
	people, e := s.d.Members.ListActiveMembers(ctx, p.Tenant, az.MemberQuery{Search: in.Owner, Kind: "human"})
	if e != nil {
		return "", nil, e
	}
	for _, m := range people {
		if m.Subject == in.Owner {
			return in.Owner, held, nil
		}
	}
	return "", nil, fault.ErrInput
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
		if attachment.Empty(in.Body, in.Files, in.Links) || len(in.Body) > 20000 || attachment.Valid(in.Files, in.Links) != nil {
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
		c, e := s.appendCommit(tx, p, t, commit.Commit{Kind: "comment", Body: in.Body, RefID: in.RefID, Files: in.Files, Links: in.Links})
		if e != nil {
			return nil, e
		}
		if e = s.d.Executor.RecordProgress(tx, p, t.Tenant, t.ID); e != nil {
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

// summary is a task as a list shows it: no timeline is read.
func (s *Service) summary(ctx context.Context, p identity.Principal, t task.Task) (api.Detail, error) {
	if e := s.d.Authorization.Check(ctx, p, "task.read", t.ID); e != nil {
		return api.Detail{}, e
	}
	pos, e := s.d.Flow.Read(ctx, t)
	if e != nil {
		return api.Detail{}, e
	}
	return api.Detail{Task: t, Position: pos, CanEdit: s.allowed(ctx, p, "task.edit", t.ID), CanComment: s.allowed(ctx, p, "commit.append", t.ID)}, nil
}
func (s *Service) detail(ctx context.Context, p identity.Principal, t task.Task, after int64) (api.Detail, error) {
	d, e := s.summary(ctx, p, t)
	if e != nil {
		return d, e
	}
	d.Commits, e = s.d.Commits.List(ctx, t.Tenant, t.ID, after, 100)
	return d, e
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
func (s *Service) List(ctx context.Context, token string, ids []string) ([]api.Detail, error) {
	out := []api.Detail{}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		ts, e := task.Pick(tx, s.d.Tasks, p.Tenant, ids)
		if e != nil {
			return e
		}
		for _, t := range ts {
			d, e := s.summary(tx, p, t)
			if errors.Is(e, az.ErrForbidden) {
				continue
			}
			if e != nil {
				return e
			}
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
