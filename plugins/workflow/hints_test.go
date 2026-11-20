package workflow_test

import (
	"errors"
	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
	"testing"
)

func TestSnapshotExplainsBlockedActionsAndNextActor(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	asAgent := func() api.Snapshot {
		t.Helper()
		s, e := f.query.Read(f.ctx, f.agent, d.Task.ID)
		if e != nil {
			t.Fatal(e)
		}
		return s
	}
	s := f.read(t, d.Task.ID)
	if s.NextActor != api.ActorOwner || s.Blocked != nil {
		t.Fatal("unassigned task should wait on owner without agent hints", s.NextActor, s.Blocked)
	}
	s, e := f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{TaskID: d.Task.ID, Subject: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	if s = f.read(t, d.Task.ID); s.NextActor != api.ActorAssignee {
		t.Fatal("assigned task should wait on agent", s.NextActor)
	}
	if a := asAgent(); !a.CanOpen || a.Blocked != nil {
		t.Fatal("assigned agent can open", a.Blocked)
	}
	if _, e = f.ex.Open(f.ctx, f.agent, "open", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion}); e != nil {
		t.Fatal(e)
	}
	a := asAgent()
	if !a.CanSubmit || a.Blocked[api.ActionOpen] != api.ReasonExecutionInProgress || a.NextActor != api.ActorAssignee {
		t.Fatal("running execution", a.CanSubmit, a.Blocked, a.NextActor)
	}
	v := *a.Execution
	if _, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: v.ID, Body: "done"}); e != nil {
		t.Fatal(e)
	}
	if s = f.read(t, d.Task.ID); !s.CanReview || s.NextActor != api.ActorReviewer || s.Blocked != nil {
		t.Fatal("owner reviews pending result", s.CanReview, s.NextActor, s.Blocked)
	}
	if a = asAgent(); a.CanReview || a.Blocked[api.ActionReview] != api.ReasonNotReviewer {
		t.Fatal("agent cannot review", a.Blocked)
	}
	if s, e = f.controls.Cancel(f.ctx, f.owner, "cancel", api.Cancel{TaskID: d.Task.ID}); e != nil {
		t.Fatal(e)
	}
	// The run is fenced and nothing waits on anyone.
	if a = asAgent(); a.NextActor != "" || a.CanSubmit || a.Blocked[api.ActionSubmit] != api.ReasonTaskNotActive {
		t.Fatal("cancelled task", a.NextActor, a.Blocked)
	}
}

func TestInboxListsOnlyTasksWaitingOnCaller(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	other, e := f.tasks.Create(f.ctx, f.owner, "create-other", workspace.Create{Title: "other", Goal: "goal", Criteria: "criteria"})
	if e != nil {
		t.Fatal(e)
	}
	inbox := func(token string) map[string]api.Actor {
		t.Helper()
		items, e := f.query.Inbox(f.ctx, token)
		if e != nil {
			t.Fatal(e)
		}
		out := map[string]api.Actor{}
		for _, v := range items {
			out[v.TaskID] = v.NextActor
		}
		return out
	}
	if got := inbox(f.owner); got[d.Task.ID] != api.ActorOwner || got[other.Task.ID] != api.ActorOwner {
		t.Fatal("owner should see unassigned tasks", got)
	}
	if got := inbox(f.agent); len(got) != 0 {
		t.Fatal("agent without assignment sees nothing", got)
	}
	s := f.open(t, d)
	if got := inbox(f.owner); len(got) != 1 || got[other.Task.ID] != api.ActorOwner {
		t.Fatal("running task leaves owner inbox", got)
	}
	if got := inbox(f.agent); len(got) != 1 || got[d.Task.ID] != api.ActorAssignee {
		t.Fatal("agent sees only its running task", got)
	}
	v := s.Execution
	if s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: v.ID, Body: "done"}); e != nil {
		t.Fatal(e)
	}
	if got := inbox(f.owner); got[d.Task.ID] != api.ActorReviewer {
		t.Fatal("reviewer sees pending review", got)
	}
	if got := inbox(f.agent); len(got) != 0 {
		t.Fatal("agent waits while review is pending", got)
	}
	s = f.read(t, d.Task.ID)
	if _, e = f.controls.Cancel(f.ctx, f.owner, "cancel", api.Cancel{TaskID: d.Task.ID}); e != nil {
		t.Fatal(e)
	}
	if got := inbox(f.agent); len(got) != 0 {
		t.Fatal("cancelled task should leave the agent inbox", got)
	}
	if got := inbox(f.owner); got[d.Task.ID] != "" {
		t.Fatal("cancelled task is not the owner's to handle", got)
	}
}

func TestRejectionRequiresReason(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	v := s.Execution
	s, e := f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: v.ID, Body: "done"})
	if e != nil {
		t.Fatal(e)
	}
	verdict := func(key, reason string) error {
		s = f.read(t, d.Task.ID)
		in := api.Review{TaskID: d.Task.ID}
		in.CommitID, in.Outcome, in.Reason = s.Result.CommitID, "rejected", reason
		_, e := f.results.Review(f.ctx, f.owner, key, in)
		return e
	}
	for _, blank := range []string{"", "  \n"} {
		if e = verdict("reject-blank-"+blank, blank); !errors.Is(e, fault.ErrInput) {
			t.Fatal("blank rejection reason accepted", e)
		}
	}
	if s = f.read(t, d.Task.ID); s.Review == nil || s.Review.Outcome != "pending" {
		t.Fatal("refused rejection changed the review", s.Review)
	}
	if e = verdict("reject", "补充错误处理"); e != nil {
		t.Fatal(e)
	}
}

// After approval nothing waits on the agent: the task is complete.
func TestApprovedTaskLeavesTheAgentInbox(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	v := s.Execution
	s, e := f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: v.ID, Body: "done"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: s.Result.CommitID, Outcome: "approved"}}); e != nil {
		t.Fatal(e)
	}
	if s = f.read(t, d.Task.ID); s.Position.Status != "completed" || s.NextActor != "" {
		t.Fatal("approved task", s.Position.Status, s.NextActor)
	}
	if items, e := f.query.Inbox(f.ctx, f.agent); e != nil || len(items) != 0 {
		t.Fatal("agent inbox should be empty", items, e)
	}
}
