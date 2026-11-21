package workflow_test

import (
	"errors"
	"testing"
	"time"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
)

// secondAgent creates another Agent of the owner and returns its credential.
func (f *fixture) secondAgent(t *testing.T, id string) string {
	t.Helper()
	if e := f.security.Create(f.ctx, f.owner, "create-"+id, identity.Principal{Tenant: "t", ID: id, Kind: "agent", OwnerID: "owner"}); e != nil {
		t.Fatal(e)
	}
	return f.credential(t, id, "issue-"+id)
}

func (f *fixture) credential(t *testing.T, subject, key string) string {
	t.Helper()
	v, e := f.security.Issue(f.ctx, f.owner, key, capsecurity.IssueInput{Tenant: "t", Subject: subject, ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	return v.Secret
}

func (f *fixture) comment(token, key, taskID string) error {
	_, e := f.tasks.Comment(f.ctx, token, key, workspace.Comment{TaskID: taskID, Body: key})
	return e
}

func (f *fixture) commitCount(t *testing.T, taskID string) int {
	t.Helper()
	cs, e := f.commits.List(f.ctx, "t", taskID, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	return len(cs)
}

// C08: once an execution is fenced, its Agent instance may no longer comment
// or ask, just as it may no longer submit.
func TestCancelledExecutionCannotComment(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	f.open(t, d)
	if _, e := f.controls.Cancel(f.ctx, f.owner, "cancel", api.Cancel{TaskID: d.Task.ID}); e != nil {
		t.Fatal(e)
	}
	before := f.commitCount(t, d.Task.ID)
	if e := f.comment(f.agent, "late-comment", d.Task.ID); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("fenced execution commented", e)
	}
	if now := f.commitCount(t, d.Task.ID); now != before {
		t.Fatal("rejected comment left a commit", before, now)
	}
	// The owner still talks on a cancelled task.
	if e := f.comment(f.owner, "owner-after-cancel", d.Task.ID); e != nil {
		t.Fatal(e)
	}
}

func TestReassignedExecutionCannotCommentOrAsk(t *testing.T) {
	f := setup(t, nil)
	qs := f.questions()
	d := f.create(t)
	s := f.open(t, d)
	other := f.secondAgent(t, "agent2")
	if _, e := f.ex.Assign(f.ctx, f.owner, "reassign", api.Assignment{TaskID: d.Task.ID, Subject: "agent2"}); e != nil {
		t.Fatal(e)
	}
	// Fenced before the new assignee claims...
	if e := f.comment(f.agent, "before-claim", d.Task.ID); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("fenced execution commented before the new claim", e)
	}
	if _, e := f.ex.Open(f.ctx, other, "claim2", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion}); e != nil {
		t.Fatal(e)
	}
	// ...and after, when the current execution is someone else's.
	if e := f.comment(f.agent, "after-claim", d.Task.ID); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("fenced execution commented after the new claim", e)
	}
	if _, e := qs.Ask(f.ctx, f.agent, "late-ask", api.Ask{TaskID: d.Task.ID, RoundID: s.Position.RoundID, Body: "还要做吗？"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("fenced execution asked", e)
	}
	if e := f.comment(other, "new-executor", d.Task.ID); e != nil {
		t.Fatal("current executor could not comment", e)
	}
}

func TestReplacedInstanceCannotCommentButNewOneCan(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	f.open(t, d)
	restarted := f.credential(t, "agent", "restarted")
	if _, e := f.ex.Open(f.ctx, restarted, "reclaim", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion}); e != nil {
		t.Fatal(e)
	}
	if e := f.comment(f.agent, "old-instance", d.Task.ID); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("replaced instance commented", e)
	}
	if e := f.comment(restarted, "new-instance", d.Task.ID); e != nil {
		t.Fatal(e)
	}
}

// Fencing is about the instance, not the past: a re-claim on a new basis with
// the same credential writes again, and nobody who never executed is affected.
func TestFenceSparesCurrentInstancesPeopleAndBystanders(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	f.open(t, d)
	edited, e := f.tasks.Edit(f.ctx, f.owner, "edit", workspace.Edit{TaskID: d.Task.ID, Title: "t2", Goal: "g2", Criteria: "c2", BasisVersion: d.Task.BasisVersion})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Open(f.ctx, f.agent, "reopen-basis", api.Open{TaskID: d.Task.ID, BasisVersion: edited.Task.BasisVersion}); e != nil {
		t.Fatal(e)
	}
	if e = f.comment(f.agent, "same-instance-new-basis", d.Task.ID); e != nil {
		t.Fatal("re-claimed instance could not comment", e)
	}

	// The owner worked on it by hand, then handed it to the Agent: the owner's
	// manual execution is fenced, but the owner still comments.
	d2 := f.create(t)
	if _, e = f.ex.Assign(f.ctx, f.owner, "self", api.Assignment{TaskID: d2.Task.ID, Subject: "owner"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Assign(f.ctx, f.owner, "to-agent", api.Assignment{TaskID: d2.Task.ID, Subject: "agent"}); e != nil {
		t.Fatal(e)
	}
	if e = f.comment(f.owner, "owner-after-handoff", d2.Task.ID); e != nil {
		t.Fatal("owner lost the right to comment", e)
	}

	// An Agent that never executed the task comments as a participant.
	bystander := f.secondAgent(t, "agent3")
	if e = f.tasks.SetParticipant(f.ctx, f.owner, "bystander", workspace.Participation{TaskID: d2.Task.ID, Subject: "agent3", Present: true}); e != nil {
		t.Fatal(e)
	}
	if e = f.comment(bystander, "bystander-comment", d2.Task.ID); e != nil {
		t.Fatal("bystander Agent could not comment", e)
	}
}
