// Package workspace composes M04 named use cases through capability interfaces.
package workspace

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"strings"
	"time"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	api "github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/runtime/command"
)

type Deps struct {
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
}
type Service struct {
	d      Deps
	secret []byte
}

func init() {
	pluginkit.Register("workspace/tasks", func(_ struct{}, d Deps) (*Service, error) { return New(d) })
}
func New(d Deps) (*Service, error) {
	if d.Transactions == nil || d.Tasks == nil || d.Commits == nil || d.Flow == nil || d.Members == nil || d.Participants == nil || d.Identity == nil || d.Authorization == nil || d.Events == nil {
		return nil, errors.New("task capabilities required")
	}
	if d.Commands == nil {
		return nil, errors.New("command lifecycle required")
	}
	secret := make([]byte, 32)
	if _, e := rand.Read(secret); e != nil {
		return nil, e
	}
	return &Service{d: d, secret: secret}, nil
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
	c := commit.Commit{Tenant: p.Tenant, TaskID: t.ID, ID: id(), Author: p.ID, OwnerID: p.OwnerID, InstanceID: p.InstanceID, Incarnation: p.Incarnation, Kind: kind, Body: body, RefID: ref, BasisVersion: t.BasisVersion}
	if e := s.d.Commits.Append(ctx, c); e != nil {
		return c, e
	}
	return s.d.Commits.Get(ctx, p.Tenant, t.ID, c.ID)
}
func (s *Service) event(ctx context.Context, t task.Task) error {
	return s.d.Events.AppendEvent(ctx, record.Event{Tenant: t.Tenant, Resource: t.ID, Kind: "task.changed"})
}
func (s *Service) Create(ctx context.Context, token, key string, in api.Create) (api.Detail, error) {
	var out api.Detail
	p, e := s.actor(ctx, token)
	if e != nil {
		return out, e
	}
	b, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "task.create", Resource: p.Tenant}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if p.Kind != "human" || !valid(in.Title, in.Goal, in.Criteria) {
			return nil, fault.ErrInput
		}
		t := task.Task{Tenant: p.Tenant, ID: id(), Title: in.Title, Goal: in.Goal, Criteria: in.Criteria, Owner: p.ID, Version: 1, BasisVersion: 1, PolicyVersion: 1}
		if e := s.d.Tasks.CreateTask(tx, t); e != nil {
			return nil, e
		}
		position, e := s.d.Flow.Initialize(tx, t)
		if e != nil {
			return nil, e
		}
		if _, e = s.append(tx, p, t, "task.created", in.Title, ""); e != nil {
			return nil, e
		}
		if e = s.event(tx, t); e != nil {
			return nil, e
		}
		return json.Marshal(api.Detail{Task: t, Position: position})
	})
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}
func (s *Service) Edit(ctx context.Context, token, key string, in api.Edit) (api.Detail, error) {
	var out api.Detail
	b, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "task.edit", ReadOperation: "task.read", Resource: in.TaskID}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if !s.allowed(tx, p, "task.read", in.TaskID) {
			return nil, az.ErrForbidden
		}
		t, e := s.d.Tasks.GetTask(tx, p.Tenant, in.TaskID)
		if e != nil {
			return nil, e
		}
		if t.Version != in.ExpectedVersion {
			return nil, fault.ErrConflict
		}
		if !valid(in.Title, in.Goal, in.Criteria) {
			return nil, fault.ErrInput
		}
		if e = s.d.Flow.CheckEdit(tx, t); e != nil {
			return nil, e
		}
		t, e = s.d.Tasks.UpdateBasis(tx, task.BasisChange{Tenant: p.Tenant, TaskID: in.TaskID, Title: in.Title, Goal: in.Goal, Criteria: in.Criteria, ExpectedVersion: in.ExpectedVersion})
		if e != nil {
			return nil, e
		}
		pos, e := s.d.Flow.Refresh(tx, t)
		if e != nil {
			return nil, e
		}
		body, _ := json.Marshal(in)
		if _, e = s.append(tx, p, t, "task.basis", string(body), ""); e != nil {
			return nil, e
		}
		if e = s.event(tx, t); e != nil {
			return nil, e
		}
		return json.Marshal(api.Detail{Task: t, Position: pos})
	})
	if e == nil {
		e = json.Unmarshal(b, &out)
	}
	return out, e
}
func (s *Service) Comment(ctx context.Context, token, key string, in api.Comment) (commit.Commit, error) {
	var out commit.Commit
	b, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "commit.append", ReadOperation: "task.read", Resource: in.TaskID}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if !s.allowed(tx, p, "task.read", in.TaskID) {
			return nil, az.ErrForbidden
		}
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
		if e = s.event(tx, t); e != nil {
			return nil, e
		}
		return json.Marshal(c)
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
	pos, e := s.d.Flow.Read(ctx, t.Tenant, t.ID)
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

type cursor struct {
	Tenant, Subject string
	Sequence        int64
	Expires         int64
}

func (s *Service) encode(c cursor) string {
	b, _ := json.Marshal(c)
	h := hmac.New(sha256.New, s.secret)
	h.Write(b)
	return base64.RawURLEncoding.EncodeToString(b) + "." + base64.RawURLEncoding.EncodeToString(h.Sum(nil))
}
func (s *Service) decode(raw string) (cursor, error) {
	var c cursor
	a, b, ok := strings.Cut(raw, ".")
	if !ok {
		return c, fault.ErrInput
	}
	data, e := base64.RawURLEncoding.DecodeString(a)
	if e != nil {
		return c, fault.ErrInput
	}
	sig, e := base64.RawURLEncoding.DecodeString(b)
	if e != nil {
		return c, fault.ErrInput
	}
	h := hmac.New(sha256.New, s.secret)
	h.Write(data)
	if !hmac.Equal(sig, h.Sum(nil)) {
		return c, fault.ErrInput
	}
	e = json.Unmarshal(data, &c)
	if e != nil || c.Expires < time.Now().Unix() {
		return c, fault.ErrInput
	}
	return c, nil
}
func (s *Service) Changes(ctx context.Context, token, raw string) (api.Page, error) {
	out := api.Page{Changes: []api.Change{}}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.actor(tx, token)
		if e != nil {
			return e
		}
		c := cursor{Tenant: p.Tenant, Subject: p.ID, Expires: time.Now().Add(time.Hour).Unix()}
		start := raw == ""
		if !start {
			c, e = s.decode(raw)
			if e != nil {
				return e
			}
			if c.Tenant != p.Tenant || c.Subject != p.ID {
				return az.ErrForbidden
			}
		}
		for {
			es, e := s.d.Events.ReadEvents(tx, p.Tenant, c.Sequence, 100)
			if e != nil {
				return e
			}
			for _, event := range es {
				c.Sequence = event.Sequence
				if !start && event.Kind == "task.changed" && s.allowed(tx, p, "task.read", event.Resource) {
					out.Changes = append(out.Changes, api.Change{TaskID: event.Resource, Kind: event.Kind})
				}
				if !start && (event.Kind == "member.set" || event.Kind == "participant.set") {
					out.Changes = append(out.Changes, api.Change{Kind: "access.changed"})
				}
			}
			if !start || len(es) < 100 {
				break
			}
		}
		out.Cursor = s.encode(c)
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
	_, e := command.Run(ctx, s.d.Commands, capcommand.Request{Token: token, Key: key, Operation: "participant.set", Resource: in.TaskID, ReadOperation: "task.read"}, in, func(tx context.Context, p identity.Principal) ([]byte, error) {
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
		return nil, s.event(tx, t)
	})
	return e
}

// Candidates exposes directory metadata only to people allowed to manage this task.
func (s *Service) Candidates(ctx context.Context, token, taskID string) ([]az.MemberOption, error) {
	out := []az.MemberOption{}
	e := s.d.Transactions.Do(ctx, func(tx context.Context) error {
		p, e := s.d.Identity.Authenticate(tx, token)
		if e != nil {
			return e
		}
		if e = s.d.Authorization.Check(tx, p, "execution.assign", taskID); e != nil {
			return e
		}
		out, e = s.d.Members.ListActiveMembers(tx, p.Tenant)
		return e
	})
	return out, e
}
