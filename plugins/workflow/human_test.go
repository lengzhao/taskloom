package workflow_test

import (
	"context"
	"errors"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"testing"
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
)

func (f *fixture) human(t *testing.T, id string) string {
	t.Helper()
	if e := f.security.Create(f.ctx, f.owner, "create-"+id, identity.Principal{Tenant: "t", ID: id, Kind: "human"}); e != nil {
		t.Fatal(e)
	}
	v, e := f.security.Issue(f.ctx, f.owner, "issue-"+id, capsecurity.IssueInput{Tenant: "t", Subject: id, ExpiresAt: time.Now().Add(time.Hour)})
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
	s, e = f.ex.Assign(f.ctx, f.owner, "assign-human", api.Assignment{Basis: basis(d, s), Subject: "worker"})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.query.Read(f.ctx, worker, d.Task.ID)
	if e != nil || !s.CanOpen || !s.CanAssign || s.CanManage || s.ViewerKind != "human" {
		t.Fatalf("human flags %+v %v", s, e)
	}
	items, e := f.query.Inbox(f.ctx, worker)
	if e != nil || len(items) != 1 || items[0].NextActor != api.ActorAssignee {
		t.Fatal(items, e)
	}
	if _, e = f.taskQueries.Candidates(f.ctx, worker, d.Task.ID); e != nil {
		t.Fatal("human delegate cannot list candidates", e)
	}
	s, e = f.ex.Open(f.ctx, worker, "start", api.Open{Basis: basis(d, s)})
	if e != nil {
		t.Fatal(e)
	}
	if !s.Execution.Manual || !s.Execution.Writable || s.Execution.InstanceID != "" {
		t.Fatal("human requires no runtime", s.Execution)
	}
	s, e = f.query.Read(f.ctx, worker, d.Task.ID)
	if e != nil || s.CanOpen || !s.CanSubmit {
		t.Fatal("manual actions", s, e)
	}
	if _, e = f.ex.Open(f.ctx, worker, "duplicate-start", api.Open{Basis: basis(d, s)}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("duplicate human execution", e)
	}
	v := *s.Execution
	s, e = f.results.Submit(f.ctx, worker, "human-result", api.Submit{Basis: basis(d, s), ExecutionID: v.ID, Body: "人工完成的结果"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Review == nil || s.Review.Reviewer != "owner" {
		t.Fatal("human submission", s)
	}
	s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{Basis: basis(d, s), Verdict: acceptance.Verdict{SubmissionID: s.Submission.ID, CommitID: s.Submission.CommitID, Outcome: "approved"}})
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
	s, e := f.ex.Assign(f.ctx, f.owner, "assign-human", api.Assignment{Basis: basis(d, s), Subject: "worker"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.tasks.SetParticipant(f.ctx, f.owner, "other-participant", workspace.Participation{TaskID: d.Task.ID, Subject: "other", Present: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Assign(f.ctx, other, "unauthorized-delegate", api.Assignment{Basis: basis(d, s), Subject: "agent"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("participant can delegate", e)
	}
	s, e = f.ex.Open(f.ctx, worker, "human-start", api.Open{Basis: basis(d, s)})
	if e != nil {
		t.Fatal(e)
	}
	old := *s.Execution
	s, e = f.ex.Assign(f.ctx, worker, "delegate", api.Assignment{Basis: basis(d, s), Subject: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Assignee != "agent" || s.Execution.Writable {
		t.Fatal("delegation not fenced", s)
	}
	if _, e = f.results.Submit(f.ctx, worker, "stale-human-result", api.Submit{Basis: basis(d, s), ExecutionID: old.ID, Body: "too late"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old human submission accepted", e)
	}
	if _, e = f.ex.Assign(f.ctx, worker, "steal-back", api.Assignment{Basis: basis(d, s), Subject: "worker"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("old handler can steal assignment", e)
	}
	s, e = f.ex.Open(f.ctx, f.agent, "agent-start", api.Open{Basis: basis(d, s)})
	if e != nil {
		t.Fatal(e)
	}
	if s.Execution.Manual || s.Execution.ID == old.ID {
		t.Fatal("agent claim", s)
	}
	// Reassigning a running agent fences it at once; nothing waits for it to stop.
	agentRun := *s.Execution
	if s, e = f.ex.Assign(f.ctx, f.owner, "reassign", api.Assignment{Basis: basis(d, s), Subject: "worker"}); e != nil || s.Execution.Writable {
		t.Fatal("reassign over a running agent", e)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "stale-agent-result", api.Submit{Basis: basis(d, s), ExecutionID: agentRun.ID, Body: "too late"}); e == nil {
		t.Fatal("fenced agent submitted")
	}
	d2, e := f.taskQueries.Get(f.ctx, f.owner, d.Task.ID, 0)
	if e != nil || d2.Task.Owner != "owner" {
		t.Fatal("delegation changed owner", e)
	}
}

func TestHumanDelegationRevokedWithParticipation(t *testing.T) {
	f := setup(t, nil)
	worker := f.human(t, "worker")
	d := f.create(t)
	s := f.read(t, d.Task.ID)
	s, e := f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{Basis: basis(d, s), Subject: "worker"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.tasks.SetParticipant(f.ctx, f.owner, "remove", workspace.Participation{TaskID: d.Task.ID, Subject: "worker", Present: false}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Assign(f.ctx, worker, "delegate", api.Assignment{Basis: basis(d, s), Subject: "agent"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("removed handler can delegate", e)
	}
	// A disabled target is not eligible even if the caller is the administrator.
	members, _ := build.GetByID[az.MemberWriter](f.graph, "sql")
	if e = f.tx.Do(f.ctx, func(ctx context.Context) error {
		return members.SetMember(ctx, az.Member{Tenant: "t", Subject: "worker", Role: "member", Active: false})
	}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Assign(f.ctx, f.owner, "inactive", api.Assignment{Basis: basis(d, s), Subject: "worker"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("inactive human assigned", e)
	}
}
