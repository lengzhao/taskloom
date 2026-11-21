package workflow_test

import (
	"errors"
	"testing"

	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
)

// An executing Agent splits its task: the subtask goes to any human owner,
// the Agent follows it, and its own execution survives the basis change it
// made, so it works in parallel and submits once the subtask is completed.
func TestAgentSplitsTaskItExecutes(t *testing.T) {
	f := setup(t, nil)
	lead := f.human(t, "lead")
	parent := f.create(t)
	work := f.open(t, parent).Execution.ID
	in := workspace.Create{Title: "part", Goal: "goal", Criteria: "criteria", ParentID: parent.Task.ID, Owner: "lead", Required: true}
	ref, e := f.tasks.Commands.Create(f.ctx, f.agent, "split", in)
	if e != nil {
		t.Fatal(e)
	}
	if again, e := f.tasks.Commands.Create(f.ctx, f.agent, "split", in); e != nil || again.TaskID != ref.TaskID {
		t.Fatal("replay created another subtask", again, e)
	}
	child, e := f.taskQueries.Get(f.ctx, lead, ref.TaskID, 0)
	if e != nil || child.Task.Owner != "lead" {
		t.Fatal("subtask not owned by the named person", child.Task.Owner, e)
	}
	if _, e = f.taskQueries.Get(f.ctx, f.agent, ref.TaskID, 0); e != nil {
		t.Fatal("splitting Agent cannot follow its subtask", e)
	}
	if f.basis(t, parent.Task.ID) != 2 {
		t.Fatal("required subtask did not advance the parent basis", f.basis(t, parent.Task.ID))
	}
	v, e := f.exStore.Current(f.ctx, "t", parent.Task.ID)
	if e != nil || v.ID != work || !v.Writable || v.BasisVersion != 2 {
		t.Fatal("author's execution did not follow its own change", v, e)
	}
	if e = f.comment(f.agent, "still-mine", parent.Task.ID); e != nil {
		t.Fatal("author fenced by its own change", e)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "early", api.Submit{TaskID: parent.Task.ID, ExecutionID: work, Body: "early"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("parent delivered before its required subtask", e)
	}

	// The subtask's owner decides who handles it.
	if _, e = f.ex.Assign(f.ctx, lead, "assign-part", api.Assignment{TaskID: ref.TaskID, Subject: "agent"}); e != nil {
		t.Fatal(e)
	}
	s, e := f.ex.Open(f.ctx, f.agent, "open-part", api.Open{TaskID: ref.TaskID, BasisVersion: 1})
	if e != nil {
		t.Fatal(e)
	}
	if s, e = f.results.Submit(f.ctx, f.agent, "submit-part", api.Submit{TaskID: ref.TaskID, ExecutionID: s.Execution.ID, Body: "part done"}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.results.Review(f.ctx, lead, "review-part", api.Review{TaskID: ref.TaskID, Verdict: acceptance.Verdict{CommitID: s.Result.CommitID, Outcome: acceptance.OutcomeApproved}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: parent.Task.ID, ExecutionID: work, Body: "all done"}); e != nil {
		t.Fatal("parent execution could not deliver after the subtask completed", e)
	}
}

// Only the Agent holding the task's current execution may split it, never at
// the top level, and only to a human owner. A plan change by anyone else
// still fences the execution.
func TestAgentSplitGuards(t *testing.T) {
	f := setup(t, nil)
	f.human(t, "lead")
	parent := f.create(t)
	split := func(token, key string, in workspace.Create) error {
		if in.Title == "" {
			in.Title, in.Goal, in.Criteria = "part", "goal", "criteria"
		}
		_, e := f.tasks.Commands.Create(f.ctx, token, key, in)
		return e
	}
	if e := split(f.agent, "unrelated", workspace.Create{ParentID: parent.Task.ID}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("Agent split a task it does not take part in", e)
	}
	if e := split(f.agent, "top", workspace.Create{}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("Agent created a top-level task", e)
	}
	if _, e := f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{TaskID: parent.Task.ID, Subject: "agent"}); e != nil {
		t.Fatal(e)
	}
	if e := split(f.agent, "unclaimed", workspace.Create{ParentID: parent.Task.ID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("Agent split a task before claiming it", e)
	}
	work, e := f.ex.Open(f.ctx, f.agent, "open", api.Open{TaskID: parent.Task.ID, BasisVersion: 1})
	if e != nil {
		t.Fatal(e)
	}
	for _, owner := range []string{"agent", "nobody"} {
		if e := split(f.agent, "owner-"+owner, workspace.Create{ParentID: parent.Task.ID, Owner: owner}); !errors.Is(e, fault.ErrInput) {
			t.Fatal("subtask owner must be an active person", owner, e)
		}
	}
	if e := split(f.owner, "top-owner", workspace.Create{Owner: "lead"}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("top-level task named another owner", e)
	}
	ref, e := f.tasks.Commands.Create(f.ctx, f.agent, "optional", workspace.Create{Title: "side", Goal: "g", Criteria: "c", ParentID: parent.Task.ID})
	if e != nil {
		t.Fatal(e)
	}
	child, e := f.taskQueries.Get(f.ctx, f.owner, ref.TaskID, 0)
	if e != nil || child.Task.Owner != "owner" || f.basis(t, parent.Task.ID) != 1 {
		t.Fatal("optional subtask: owner defaults to the parent's, basis unchanged", child.Task.Owner, f.basis(t, parent.Task.ID), e)
	}

	// The owner's own plan change fences the Agent, which re-claims on the new basis.
	if e = split(f.owner, "owner-split", workspace.Create{ParentID: parent.Task.ID, Owner: "lead", Required: true}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "stale", api.Submit{TaskID: parent.Task.ID, ExecutionID: work.Execution.ID, Body: "x"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("another person's plan change did not fence the execution", e)
	}
	if e = split(f.agent, "fenced", workspace.Create{ParentID: parent.Task.ID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("fenced execution split the task", e)
	}

	// Another instance of the same Agent that is not the current execution cannot split either.
	if _, e = f.ex.Open(f.ctx, f.agent, "reopen", api.Open{TaskID: parent.Task.ID, BasisVersion: 2}); e != nil {
		t.Fatal(e)
	}
	other := f.credential(t, "agent", "second")
	if e = split(other, "other-instance", workspace.Create{ParentID: parent.Task.ID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("an instance without the execution split the task", e)
	}
}
