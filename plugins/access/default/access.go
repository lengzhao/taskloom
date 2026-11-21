package access

import (
	"context"
	"errors"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/task"
)

type Deps struct {
	Store az.Relations `json:"store"`
	Tasks task.Reader  `json:"tasks"`
}
type Service struct{ d Deps }

func init() {
	pluginkit.Register("access/default", func(_ struct{}, d Deps) (*Service, error) { return New(d) })
}
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Tasks == nil {
		return nil, errors.New("membership and tasks required")
	}
	return &Service{d}, nil
}
func (s *Service) Check(ctx context.Context, p identity.Principal, op, resource string) error {
	if p.Tenant == "" || p.ID == "" {
		return az.ErrForbidden
	}
	m, e := s.d.Store.GetMember(ctx, p.Tenant, p.ID)
	if errors.Is(e, fault.ErrNotFound) {
		return az.ErrForbidden
	}
	if e != nil {
		return e
	}
	if !m.Active || (m.Role != "admin" && m.Role != "member") {
		return az.ErrForbidden
	}
	switch op {
	case "audit.read", "space.edit", "identity.create", "identity.issue", "identity.revoke", "identity.password", "member.set":
		if resource == p.Tenant && m.Role == "admin" && p.Kind == "human" {
			return nil
		}
		return az.ErrForbidden
	case "task.create":
		if resource == p.Tenant && p.Kind == "human" {
			return nil
		}
		return az.ErrForbidden
	case "task.project", "task.read", "task.edit", "commit.append", "file.upload", "participant.set", "execution.open", "execution.assign", "result.submit", "result.review", "task.cancel", "task.restart", "task.plan":
		t, e := s.d.Tasks.GetTask(ctx, p.Tenant, resource)
		if errors.Is(e, fault.ErrNotFound) {
			return az.ErrForbidden
		}
		if e != nil {
			return e
		}
		if op == "result.review" {
			if p.Kind == "human" && p.ID == t.Owner {
				return nil
			}
			return az.ErrForbidden
		}
		if p.Kind == "human" && (m.Role == "admin" || t.Owner == p.ID) {
			return nil
		}
		if op == "execution.assign" && p.Kind == "human" {
			assigned, err := s.d.Store.IsAssigned(ctx, p.Tenant, t.ID, p.ID)
			if err != nil {
				return err
			}
			if assigned {
				participating, err := s.d.Store.HasParticipant(ctx, az.Participant{Tenant: p.Tenant, TaskID: t.ID, Subject: p.ID})
				if err != nil {
					return err
				}
				if participating {
					return nil
				}
			}
		}
		if op == "task.edit" || op == "participant.set" || op == "execution.assign" || op == "task.cancel" || op == "task.restart" || op == "task.plan" {
			return az.ErrForbidden
		}
		// Execution commands recheck explicit assignment and instance/generation in
		// their own domain. Read access still requires task participation.
		// Agents require explicit task participation; owner attribution grants nothing.
		ok, e := s.d.Store.HasParticipant(ctx, az.Participant{Tenant: p.Tenant, TaskID: t.ID, Subject: p.ID})
		if e != nil {
			return e
		}
		if ok {
			return nil
		}
		return az.ErrForbidden
	default:
		return az.ErrForbidden
	}
}
