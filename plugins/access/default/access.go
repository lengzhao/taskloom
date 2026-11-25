package access

import (
	"context"
	"errors"

	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/skills"
	"github.com/lengzhao/taskloom/cap/task"
)

type Deps struct {
	Store az.Relations `json:"store"`
	Tasks task.Reader  `json:"tasks"`
	// Projects grants a project's owner and members what a participant has
	// on every task filed under it.
	Projects catalog.ProjectReader `json:"projects"`
	// Skills gives a capability's owner and collaborators their relations.
	Skills skills.CapabilityReader `json:"skills"`
}
type Service struct{ d Deps }

func init() {
	pluginkit.Register("access/default", func(_ struct{}, d Deps) (*Service, error) { return New(d) })
}
func New(d Deps) (*Service, error) {
	if d.Store == nil || d.Tasks == nil || d.Projects == nil || d.Skills == nil {
		return nil, errors.New("membership, tasks, projects and skills required")
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
	case "tasks.list":
		// Any active member may list; each task is still filtered by task.read.
		if resource == p.Tenant {
			return nil
		}
		return az.ErrForbidden
	case "project.file":
		// Filing a task under a project shares it with the project's people,
		// so only those who belong to the project, or administrators, file there.
		if m.Role == "admin" && p.Kind == "human" {
			return nil
		}
		project, e := s.d.Projects.GetProject(ctx, p.Tenant, resource)
		if errors.Is(e, fault.ErrNotFound) {
			return az.ErrForbidden
		}
		if e != nil {
			return e
		}
		if project.Includes(p.ID) {
			return nil
		}
		return az.ErrForbidden
	case "task.create":
		if resource == p.Tenant && p.Kind == "human" {
			return nil
		}
		return az.ErrForbidden
	case "skill.read", "skill.create":
		// Any active member, person or Agent, reads the tenant catalog and may
		// start a capability; visibility of each one is checked by its reader.
		if resource == p.Tenant {
			return nil
		}
		return az.ErrForbidden
	case "skill.review", "skill.admin":
		if resource == p.Tenant && m.Role == "admin" && p.Kind == "human" {
			return nil
		}
		return az.ErrForbidden
	case "skill.release", "skill.manage":
		// Resource is a capability ID. Collaborators publish releases; only the
		// owner and administrators change metadata, visibility or archive state.
		if m.Role == "admin" && p.Kind == "human" {
			return nil
		}
		c, e := s.d.Skills.GetCapability(ctx, p.Tenant, resource)
		if errors.Is(e, fault.ErrNotFound) {
			return az.ErrForbidden
		}
		if e != nil {
			return e
		}
		if c.Owner == p.ID || (op == "skill.release" && c.Contributes(p.ID)) {
			return nil
		}
		return az.ErrForbidden
	case "task.project", "task.owner", "task.read", "task.edit", "commit.append", "file.upload", "participant.set", "execution.open", "execution.assign", "result.submit", "result.review", "task.cancel", "task.restart", "task.plan", "task.split":
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
		if op == "task.split" && p.Kind != "agent" {
			return az.ErrForbidden // people split through task.create and task.plan
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
		// Filing a task decides who else may read it, and a handover decides who
		// answers for it: both stay with the owner and administrators.
		if op == "task.edit" || op == "participant.set" || op == "execution.assign" || op == "task.cancel" || op == "task.restart" || op == "task.plan" || op == "task.project" || op == "task.owner" {
			return az.ErrForbidden
		}
		// Execution commands recheck explicit assignment and instance/generation in
		// their own domain. Read access still requires task participation or
		// membership of the task's project; for Agents both are explicit, and
		// owner attribution grants nothing.
		ok, e := s.d.Store.HasParticipant(ctx, az.Participant{Tenant: p.Tenant, TaskID: t.ID, Subject: p.ID})
		if e != nil {
			return e
		}
		if ok {
			return nil
		}
		if t.ProjectID != "" {
			project, e := s.d.Projects.GetProject(ctx, p.Tenant, t.ProjectID)
			if e != nil && !errors.Is(e, fault.ErrNotFound) {
				return e
			}
			if e == nil && project.Includes(p.ID) {
				return nil
			}
		}
		return az.ErrForbidden
	default:
		return az.ErrForbidden
	}
}
