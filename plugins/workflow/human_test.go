package workflow_test

import (
	"context"
	"errors"
	"fmt"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"testing"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
)

func (f *fixture) human(t *testing.T, id string) string {
	t.Helper()
	if e := f.security.CreateUser(f.ctx, f.owner, "create-"+id, capsecurity.UserInput{Tenant: "t", Username: id, Password: "UserPassword123!"}); e != nil {
		t.Fatal(e)
	}
	v, e := f.security.Login(f.ctx, capsecurity.LoginInput{Tenant: "t", Username: id, Password: "UserPassword123!"})
	if e != nil {
		t.Fatal(e)
	}
	return v.Secret
}

func TestHumanProcessesAndSubmitsWithoutRuntimeProof(t *testing.T) {
	f := setup(t, nil)
	worker := f.human(t, "worker")
	d := f.create(t)
	s := f.read(t, d.Task.ID)
	var e error
	s, e = f.ex.Assign(f.ctx, f.owner, "assign-human", api.Assignment{TaskID: d.Task.ID, Subject: "worker"})
	if e != nil {
		t.Fatal(e)
	}
	// Assigning a person starts their manual work; there is no separate claim.
	s, e = f.snap(worker, d.Task.ID)
	if e != nil || s.CanOpen || !s.CanSubmit || !s.CanAssign || s.CanManage {
		t.Fatalf("human flags %+v %v", s, e)
	}
	items, e := inbox(f.ctx, f.query, worker, nil)
	if e != nil || len(items) != 1 || items[0].NextActor != api.ActorAssignee {
		t.Fatal(items, e)
	}
	if _, e = f.taskQueries.People(f.ctx, worker, az.MemberQuery{}); e != nil {
		t.Fatal("human delegate cannot list people", e)
	}
	if !s.Execution.Writable || s.Execution.CredentialID != "" || s.Execution.Subject != "worker" {
		t.Fatal("human requires no runtime", s.Execution)
	}
	if _, e = f.ex.Open(f.ctx, worker, "duplicate-start", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("duplicate human execution", e)
	}
	v := *s.Execution
	s, e = f.results.Submit(f.ctx, worker, "human-result", api.Submit{TaskID: d.Task.ID, ExecutionID: v.ID, Body: "人工完成的结果"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Review == nil || s.Review.Reviewer != "owner" {
		t.Fatal("human submission", s)
	}
	s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: s.Result.CommitID, Outcome: "approved"}})
	if e != nil || s.Position.Status != "completed" {
		t.Fatal("human completion must not wait on runtime proof", s, e)
	}
}

func TestHumanCanDelegateButOldWorkCannotSubmit(t *testing.T) {
	f := setup(t, nil)
	worker := f.human(t, "worker")
	other := f.human(t, "other")
	d := f.create(t)
	s := f.read(t, d.Task.ID)
	s, e := f.ex.Assign(f.ctx, f.owner, "assign-human", api.Assignment{TaskID: d.Task.ID, Subject: "worker"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.tasks.SetParticipant(f.ctx, f.owner, "other-participant", workspace.Participation{TaskID: d.Task.ID, Subject: "other", Present: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Assign(f.ctx, other, "unauthorized-delegate", api.Assignment{TaskID: d.Task.ID, Subject: "agent"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("participant can delegate", e)
	}
	old := *s.Execution
	if old.CredentialID != "" || old.Subject != "worker" || !old.Writable {
		t.Fatal("assignment did not start manual work", old)
	}
	s, e = f.ex.Assign(f.ctx, worker, "delegate", api.Assignment{TaskID: d.Task.ID, Subject: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Assignee != "agent" || s.Execution.Writable {
		t.Fatal("delegation not fenced", s)
	}
	if _, e = f.results.Submit(f.ctx, worker, "stale-human-result", api.Submit{TaskID: d.Task.ID, ExecutionID: old.ID, Body: "too late"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old human submission accepted", e)
	}
	if _, e = f.ex.Assign(f.ctx, worker, "steal-back", api.Assignment{TaskID: d.Task.ID, Subject: "worker"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("old handler can steal assignment", e)
	}
	s, e = f.ex.Open(f.ctx, f.agent, "agent-start", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion})
	if e != nil {
		t.Fatal(e)
	}
	if s.Execution.CredentialID == "" || s.Execution.ID == old.ID {
		t.Fatal("agent claim", s)
	}
	// Reassigning a running agent fences it at once; nothing waits for it to stop.
	agentRun := *s.Execution
	if s, e = f.ex.Assign(f.ctx, f.owner, "reassign", api.Assignment{TaskID: d.Task.ID, Subject: "worker"}); e != nil || s.Execution.ID == agentRun.ID || s.Execution.CredentialID != "" {
		t.Fatal("reassign over a running agent", e)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "stale-agent-result", api.Submit{TaskID: d.Task.ID, ExecutionID: agentRun.ID, Body: "too late"}); e == nil {
		t.Fatal("fenced agent submitted")
	}
	d2, e := f.query.Read(f.ctx, f.owner, d.Task.ID, 0)
	if e != nil || d2.Task.Owner != "owner" {
		t.Fatal("delegation changed owner", e)
	}
}

func TestHumanDelegationRevokedWithParticipation(t *testing.T) {
	f := setup(t, nil)
	worker := f.human(t, "worker")
	d := f.create(t)
	_, e := f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{TaskID: d.Task.ID, Subject: "worker"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.tasks.SetParticipant(f.ctx, f.owner, "remove", workspace.Participation{TaskID: d.Task.ID, Subject: "worker", Present: false}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Assign(f.ctx, worker, "delegate", api.Assignment{TaskID: d.Task.ID, Subject: "agent"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("removed handler can delegate", e)
	}
	// A disabled target is not eligible even if the caller is the administrator.
	members, _ := build.GetByID[az.MemberStore](f.graph, "sql")
	if e = f.tx.Do(f.ctx, func(ctx context.Context) error {
		return members.SetMember(ctx, az.Member{Tenant: "t", Subject: "worker", Role: "member", Active: false})
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Assign(f.ctx, f.owner, "inactive", api.Assignment{TaskID: d.Task.ID, Subject: "worker"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("inactive human assigned", e)
	}
}

// The task list shows, for each readable task, who handles it, its parent and
// when its timeline last changed; a task the caller cannot read is left out.
// Assigning the owner records no participation: the owner already holds every right.
func TestOverviewAndOwnerSelfAssignment(t *testing.T) {
	f := setup(t, nil)
	outsider := f.human(t, "outsider")
	parent := f.create(t)
	child, e := f.tasks.Create(f.ctx, f.owner, "child", workspace.Create{Title: "child", Goal: "g", Criteria: "c", ParentID: parent.Task.ID, Required: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Assign(f.ctx, f.owner, "self", api.Assignment{TaskID: child.Task.ID, Subject: "owner"}); e != nil {
		t.Fatal(e)
	}
	if s := f.read(t, child.Task.ID); s.Assignee != "owner" || len(s.Participants) != 0 {
		t.Fatal("owner self-assignment", s.Assignee, s.Participants)
	}
	list := func(token string, ids []string) ([]api.Summary, error) {
		return f.query.List(f.ctx, token, api.ListFilter{IDs: ids})
	}
	items, e := list(f.owner, nil)
	if e != nil {
		t.Fatal(e)
	}
	byID := map[string]api.Summary{}
	for _, x := range items {
		byID[x.Task.ID] = x
	}
	if c := byID[child.Task.ID]; c.Assignee != "owner" || c.ParentID != parent.Task.ID || c.UpdatedAt.IsZero() || c.Position.Status != "active" {
		t.Fatal("child row", c)
	}
	if p := byID[parent.Task.ID]; p.Assignee != "" || p.ParentID != "" || p.UpdatedAt.IsZero() {
		t.Fatal("parent row", p)
	}
	if mine, e := list(outsider, nil); e != nil || len(mine) != 0 {
		t.Fatal("list leaked unreadable tasks", mine, e)
	}

	// Naming tasks limits every list to them; a missing or unreadable task is
	// left out exactly as in the full list.
	ids := []string{child.Task.ID, "missing", child.Task.ID}
	if items, e := list(f.owner, ids); e != nil || len(items) != 1 || items[0] != byID[child.Task.ID] {
		t.Fatal("list by id", items, e)
	}
	if items, e := list(outsider, ids); e != nil || len(items) != 0 {
		t.Fatal("list by id leaked", items, e)
	}
	full, e := inbox(f.ctx, f.query, f.owner, nil)
	if e != nil {
		t.Fatal(e)
	}
	for _, x := range full {
		if items, e := inbox(f.ctx, f.query, f.owner, []string{x.TaskID, "missing"}); e != nil || len(items) != 1 || items[0] != x {
			t.Fatal("inbox by id", x, items, e)
		}
	}
	if len(full) == 0 {
		t.Fatal("inbox empty")
	}
	many := make([]string, 101)
	for i := range many {
		many[i] = fmt.Sprint("t", i)
	}
	for _, e := range []error{
		func() error { _, e := list(f.owner, many); return e }(),
		func() error { _, e := inbox(f.ctx, f.query, f.owner, many); return e }(),
		func() error { _, e := list(f.owner, []string{""}); return e }(),
	} {
		if !errors.Is(e, fault.ErrInput) {
			t.Fatal("bad id list accepted", e)
		}
	}
}
