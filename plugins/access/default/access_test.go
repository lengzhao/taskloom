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
