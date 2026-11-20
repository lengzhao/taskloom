package access_test

import (
	"context"
	"errors"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/identity"
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
	s, e := access.New(access.Deps{Store: relations{}, Tasks: taskLookup{}})
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

type fixedRelations struct {
	role                  string
	participant, assigned bool
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

// Task writes run without a separate task.read check, so every principal
// allowed a task write must also be allowed to read that task.
func TestTaskWritesImplyRead(t *testing.T) {
	ctx := context.Background()
	for _, role := range []string{"admin", "member"} {
		for _, participant := range []bool{false, true} {
			for _, assigned := range []bool{false, true} {
				s, e := access.New(access.Deps{Store: fixedRelations{role, participant, assigned}, Tasks: taskLookup{}})
				if e != nil {
					t.Fatal(e)
				}
				for _, kind := range []string{"human", "agent"} {
					for _, id := range []string{"owner", "other"} {
						p := identity.Principal{Tenant: "t", ID: id, Kind: kind, CredentialID: "c"}
						read := s.Check(ctx, p, "task.read", "task")
						for _, op := range []string{"task.edit", "commit.append", "participant.set", "execution.assign", "execution.open", "result.submit", "result.review", "task.cancel", "task.restart", "task.project"} {
							if s.Check(ctx, p, op, "task") == nil && read != nil {
								t.Fatalf("%s allowed without task.read: role=%s participant=%v assigned=%v kind=%s id=%s", op, role, participant, assigned, kind, id)
							}
						}
					}
				}
			}
		}
	}
}
