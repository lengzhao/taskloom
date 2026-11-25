package access_test

import (
	"context"
	"errors"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/skills"
	"github.com/lengzhao/taskloom/cap/task"
	access "github.com/lengzhao/taskloom/plugins/access/default"
	"testing"
)

// Neither provider exposes a write method: access decisions cannot mutate facts.
type relations struct{}

func (relations) GetMember(context.Context, string, string) (az.Member, error) {
	return az.Member{Role: "member", Active: true}, nil
}
func (relations) HasParticipant(context.Context, az.Participant) (bool, error) { return false, nil }

type taskLookup struct{}

func (taskLookup) GetTask(_ context.Context, tenant, id string) (task.Task, error) {
	return task.Task{Tenant: tenant, ID: id, Owner: "owner"}, nil
}
func TestReadOnlyProvidersAreSufficient(t *testing.T) {
	s, e := access.New(access.Deps{Store: relations{}, Tasks: taskLookup{}, Projects: projects{}, Skills: skillCaps{}})
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	p := identity.Principal{Tenant: "t", ID: "owner", Kind: "human"}
	if e = s.Check(ctx, p, "task.edit", "task"); e != nil {
		t.Fatal(e)
	}
	p.ID = "other"
	if e = s.Check(ctx, p, "task.read", "task"); !errors.Is(e, az.ErrForbidden) {
		t.Fatal(e)
	}
}

func (relations) IsAssigned(context.Context, string, string, string) (bool, error) { return false, nil }
func (relations) AssignedBy(context.Context, string, string) (string, error)       { return "", nil }

type fixedRelations struct {
	role                  string
	participant, assigned bool
	assigner              string
}

func (r fixedRelations) GetMember(context.Context, string, string) (az.Member, error) {
	return az.Member{Role: r.role, Active: true}, nil
}
func (r fixedRelations) HasParticipant(context.Context, az.Participant) (bool, error) {
	return r.participant, nil
}
func (r fixedRelations) IsAssigned(context.Context, string, string, string) (bool, error) {
	return r.assigned, nil
}
func (r fixedRelations) AssignedBy(context.Context, string, string) (string, error) {
	return r.assigner, nil
}

// Task writes run without a separate task.read check, so every principal
// allowed a task write must also be allowed to read that task.
func TestTaskWritesImplyRead(t *testing.T) {
	ctx := context.Background()
	for _, role := range []string{"admin", "member"} {
		for _, participant := range []bool{false, true} {
			for _, assigned := range []bool{false, true} {
				for _, assigner := range []string{"", "owner", "other"} {
					s, e := access.New(access.Deps{Store: fixedRelations{role, participant, assigned, assigner}, Tasks: taskLookup{}, Projects: projects{}, Skills: skillCaps{}})
					if e != nil {
						t.Fatal(e)
					}
					for _, kind := range []string{"human", "agent"} {
						for _, id := range []string{"owner", "other"} {
							p := identity.Principal{Tenant: "t", ID: id, Kind: kind, CredentialID: "c"}
							read := s.Check(ctx, p, "task.read", "task")
							for _, op := range []string{"task.edit", "commit.append", "participant.set", "execution.assign", "execution.open", "result.submit", "result.review", "task.cancel", "task.restart", "task.project", "task.owner"} {
								if s.Check(ctx, p, op, "task") == nil && read != nil {
									t.Fatalf("%s allowed without task.read: role=%s participant=%v assigned=%v assigner=%s kind=%s id=%s", op, role, participant, assigned, assigner, kind, id)
								}
							}
						}
					}
				}
			}
		}
	}
}

type projects struct{}

func (projects) GetProject(_ context.Context, tenant, id string) (catalog.Project, error) {
	if id != "p" {
		return catalog.Project{}, fault.ErrNotFound
	}
	return catalog.Project{Tenant: tenant, ID: id, Owner: "lead", Members: []string{"watcher"}}, nil
}

type filedTask struct{}

func (filedTask) GetTask(_ context.Context, tenant, id string) (task.Task, error) {
	return task.Task{Tenant: tenant, ID: id, Owner: "owner", ProjectID: "p"}, nil
}

// A project's owner and members take part in its tasks like participants:
// they read, comment and ask, but neither manage nor file elsewhere; only the
// project owner may also hand the task to another handler.
func TestProjectMembersActAsParticipants(t *testing.T) {
	ctx := context.Background()
	s, e := access.New(access.Deps{Store: relations{}, Tasks: filedTask{}, Projects: projects{}, Skills: skillCaps{}})
	if e != nil {
		t.Fatal(e)
	}
	for _, p := range []identity.Principal{{Tenant: "t", ID: "lead", Kind: "human"}, {Tenant: "t", ID: "watcher", Kind: "agent", CredentialID: "c"}} {
		for _, op := range []string{"task.read", "commit.append", "file.upload"} {
			if e := s.Check(ctx, p, op, "task"); e != nil {
				t.Fatal(p.ID, op, e)
			}
		}
		for _, op := range []string{"task.edit", "participant.set", "execution.assign", "task.cancel", "task.restart", "task.plan", "task.project", "task.owner", "result.review"} {
			// The project's owner (a person) may change who handles a filed task.
			if op == "execution.assign" && p.ID == "lead" {
				if e := s.Check(ctx, p, op, "task"); e != nil {
					t.Fatal("project owner cannot reassign", e)
				}
				continue
			}
			if e := s.Check(ctx, p, op, "task"); !errors.Is(e, az.ErrForbidden) {
				t.Fatal(p.ID, op, "allowed", e)
			}
		}
	}
	if e := s.Check(ctx, identity.Principal{Tenant: "t", ID: "outsider", Kind: "human"}, "task.read", "task"); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("non-member reads a filed task", e)
	}
	// A participant may comment but no longer re-file the task.
	s, _ = access.New(access.Deps{Store: fixedRelations{"member", true, false, ""}, Tasks: taskLookup{}, Projects: projects{}, Skills: skillCaps{}})
	if e := s.Check(ctx, identity.Principal{Tenant: "t", ID: "other", Kind: "human"}, "task.project", "task"); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("participant files the task", e)
	}
}

// Filing into a project is for its people and administrators.
func TestProjectFiling(t *testing.T) {
	ctx := context.Background()
	s, e := access.New(access.Deps{Store: relations{}, Tasks: filedTask{}, Projects: projects{}, Skills: skillCaps{}})
	if e != nil {
		t.Fatal(e)
	}
	for id, want := range map[string]bool{"lead": true, "watcher": true, "outsider": false} {
		if got := s.Check(ctx, identity.Principal{Tenant: "t", ID: id, Kind: "human"}, "project.file", "p") == nil; got != want {
			t.Fatal(id, got)
		}
	}
	if e := s.Check(ctx, identity.Principal{Tenant: "t", ID: "lead", Kind: "human"}, "project.file", "missing"); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("unknown project", e)
	}
	admin, _ := access.New(access.Deps{Store: fixedRelations{"admin", false, false, ""}, Tasks: filedTask{}, Projects: projects{}, Skills: skillCaps{}})
	if e := admin.Check(ctx, identity.Principal{Tenant: "t", ID: "boss", Kind: "human"}, "project.file", "p"); e != nil {
		t.Fatal("administrator", e)
	}
}

// skillCaps knows one capability "c" owned by "owner" with collaborator "helper".
type skillCaps struct{}

func (skillCaps) GetCapability(_ context.Context, tenant, id string) (skills.Capability, error) {
	if id != "c" {
		return skills.Capability{}, fault.ErrNotFound
	}
	return skills.Capability{Tenant: tenant, ID: id, Owner: "owner", Collaborators: []string{"helper"}}, nil
}

func TestSkillOperations(t *testing.T) {
	member, _ := access.New(access.Deps{Store: fixedRelations{"member", false, false, ""}, Tasks: taskLookup{}, Projects: projects{}, Skills: skillCaps{}})
	admin, _ := access.New(access.Deps{Store: fixedRelations{"admin", false, false, ""}, Tasks: taskLookup{}, Projects: projects{}, Skills: skillCaps{}})
	cases := []struct {
		s       *access.Service
		p       identity.Principal
		op, res string
		allowed bool
	}{
		{member, identity.Principal{Tenant: "t", ID: "agent", Kind: "agent"}, "skill.read", "t", true},
		{member, identity.Principal{Tenant: "t", ID: "agent", Kind: "agent"}, "skill.create", "t", true},
		{member, identity.Principal{Tenant: "t", ID: "agent", Kind: "agent"}, "skill.read", "other", false},
		{member, identity.Principal{Tenant: "t", ID: "owner", Kind: "human"}, "skill.manage", "c", true},
		{member, identity.Principal{Tenant: "t", ID: "helper", Kind: "agent"}, "skill.release", "c", true},
		{member, identity.Principal{Tenant: "t", ID: "helper", Kind: "agent"}, "skill.manage", "c", false},
		{member, identity.Principal{Tenant: "t", ID: "stranger", Kind: "human"}, "skill.release", "c", false},
		{member, identity.Principal{Tenant: "t", ID: "owner", Kind: "human"}, "skill.release", "missing", false},
		{member, identity.Principal{Tenant: "t", ID: "owner", Kind: "human"}, "skill.review", "t", false},
		{admin, identity.Principal{Tenant: "t", ID: "boss", Kind: "human"}, "skill.review", "t", true},
		{admin, identity.Principal{Tenant: "t", ID: "boss", Kind: "human"}, "skill.manage", "c", true},
		{admin, identity.Principal{Tenant: "t", ID: "bot", Kind: "agent"}, "skill.admin", "t", false},
	}
	for _, c := range cases {
		err := c.s.Check(context.Background(), c.p, c.op, c.res)
		if (err == nil) != c.allowed {
			t.Errorf("%s %s on %s: %v", c.p.ID, c.op, c.res, err)
		}
	}
}

// Any active member, person or Agent, may create a task in its own tenant.
func TestTaskCreate(t *testing.T) {
	ctx := context.Background()
	s, e := access.New(access.Deps{Store: relations{}, Tasks: filedTask{}, Projects: projects{}, Skills: skillCaps{}})
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"human", "agent"} {
		if e := s.Check(ctx, identity.Principal{Tenant: "t", ID: "lead", Kind: kind}, "task.create", "t"); e != nil {
			t.Fatal(kind, e)
		}
		if e := s.Check(ctx, identity.Principal{Tenant: "t", ID: "lead", Kind: kind}, "task.create", "other"); !errors.Is(e, az.ErrForbidden) {
			t.Fatal("created in another tenant", kind, e)
		}
	}
	if e := s.Check(ctx, identity.Principal{Tenant: "t", ID: "lead", Kind: "service"}, "task.create", "t"); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("unknown principal kind", e)
	}
}
