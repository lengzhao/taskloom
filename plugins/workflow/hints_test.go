package workflow_test

import (
	"context"
	api "github.com/lengzhao/taskloom/cap/workflow"
	"testing"
	"time"
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
	s, e := f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{Basis: basis(d, s), Subject: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	if s = f.read(t, d.Task.ID); s.NextActor != api.ActorAgent {
		t.Fatal("assigned task should wait on agent", s.NextActor)
	}
	if a := asAgent(); !a.CanOpen || a.Blocked != nil {
		t.Fatal("assigned agent can open", a.Blocked)
	}
	if _, e = f.ex.Open(f.ctx, f.agent, "open", api.Open{Basis: basis(d, s)}); e != nil {
		t.Fatal(e)
	}
	a := asAgent()
	if !a.CanSubmit || a.Blocked[api.ActionOpen] != api.ReasonExecutionInProgress || a.NextActor != api.ActorAgent {
		t.Fatal("running execution", a.CanSubmit, a.Blocked, a.NextActor)
	}
	v := *a.Execution
	if e = f.tx.Do(f.ctx, func(tx context.Context) error {
		v.LeaseUntil = time.Now().Add(-time.Second)
		return f.exStore.SaveExecution(tx, v)
	}); e != nil {
		t.Fatal(e)
	}
	if a = asAgent(); a.CanSubmit || !a.CanRenew || a.Blocked[api.ActionSubmit] != api.ReasonLeaseExpired {
		t.Fatal("expired lease should explain submit and still allow renew", a.Blocked)
	}
	if _, e = f.ex.Renew(f.ctx, f.agent, "renew", api.ExecutionRef{TaskID: d.Task.ID, ExecutionID: v.ID, Generation: v.Generation}); e != nil {
		t.Fatal(e)
	}
	a = asAgent()
	if _, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{Basis: basis(d, a), ExecutionID: v.ID, Generation: v.Generation, Body: "done"}); e != nil {
		t.Fatal(e)
	}
	if s = f.read(t, d.Task.ID); !s.CanReview || s.NextActor != api.ActorReviewer || s.Blocked != nil {
		t.Fatal("owner reviews pending result", s.CanReview, s.NextActor, s.Blocked)
	}
	if a = asAgent(); a.CanReview || a.Blocked[api.ActionReview] != api.ReasonNotReviewer {
		t.Fatal("agent cannot review", a.Blocked)
	}
	if s, e = f.controls.Cancel(f.ctx, f.owner, "cancel", basis(d, s)); e != nil {
		t.Fatal(e)
	}
	if a = asAgent(); a.NextActor != api.ActorAgent || a.CanSubmit || a.Blocked[api.ActionSubmit] != api.ReasonTaskNotActive {
		t.Fatal("cancelled task waits for stop proof", a.NextActor, a.Blocked)
	}
}
