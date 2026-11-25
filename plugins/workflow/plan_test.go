package workflow_test

import (
	"errors"
	"testing"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/plan"
	"github.com/lengzhao/taskloom/cap/record"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
)

func (f *fixture) plan(t *testing.T) plan.Commands {
	t.Helper()
	p, ok := build.GetByID[plan.Commands](f.graph, "plan-commands")
	if !ok {
		t.Fatal("plan commands not built")
	}
	return p
}

func (f *fixture) named(t *testing.T, title string) api.View {
	t.Helper()
	d, e := f.tasks.Create(f.ctx, f.owner, "create-"+title, workspace.Create{Title: title, Goal: "goal", Criteria: "criteria"})
	if e != nil {
		t.Fatal(e)
	}
	return d
}

func (f *fixture) basis(t *testing.T, id string) int64 {
	t.Helper()
	d, e := f.query.Read(f.ctx, f.owner, id, 0)
	if e != nil {
		t.Fatal(e)
	}
	return d.Task.BasisVersion
}

// attach places child under parent as the owner, pinning what the owner reads now.
func (f *fixture) attach(t *testing.T, key, child, parent string, required bool) {
	t.Helper()
	if e := f.plan(t).Place(f.ctx, f.owner, key, plan.Placement{ChildID: child, ParentID: parent, Required: required}); e != nil {
		t.Fatal(e)
	}
}

// finishTask assigns, works, submits and approves a task as agent and owner.
func (f *fixture) finishTask(t *testing.T, id string) {
	t.Helper()
	if _, e := f.ex.Assign(f.ctx, f.owner, "assign-"+id, api.Assignment{TaskID: id, Subject: "agent"}); e != nil {
		t.Fatal(e)
	}
	s, e := f.ex.Open(f.ctx, f.agent, "open-"+id, api.Open{TaskID: id, BasisVersion: f.basis(t, id)})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.results.Submit(f.ctx, f.agent, "submit-"+id, api.Submit{TaskID: id, ExecutionID: s.Execution.ID, Body: "done"})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.results.Review(f.ctx, f.owner, "review-"+id, api.Review{TaskID: id, Verdict: acceptance.Verdict{CommitID: s.Result.CommitID, Outcome: acceptance.OutcomeApproved}})
	if e != nil || s.Position.Status != "completed" {
		t.Fatal("task not completed", s.Position, e)
	}
}

// The parent cannot deliver until its required subtasks are completed; a
// completed subtask only unblocks submission and never completes the parent.
func TestRequiredSubtaskGatesParentSubmission(t *testing.T) {
	f := setup(t, nil)
	parent, child, optional := f.named(t, "parent"), f.named(t, "child"), f.named(t, "optional")
	f.attach(t, "attach-child", child.Task.ID, parent.Task.ID, true)
	f.attach(t, "attach-optional", optional.Task.ID, parent.Task.ID, false)

	s := f.read(t, parent.Task.ID)
	if len(s.Children) != 2 || s.Position.Waiting != "等待必需子任务完成" {
		t.Fatal("parent does not show its plan", s.Children, s.Position)
	}
	if v := f.read(t, child.Task.ID); v.Parent == nil || v.Parent.TaskID != parent.Task.ID || !v.Parent.Required {
		t.Fatal("child does not show its parent", v.Parent)
	}
	if _, e := f.ex.Assign(f.ctx, f.owner, "assign-parent", api.Assignment{TaskID: parent.Task.ID, Subject: "agent"}); e != nil {
		t.Fatal(e)
	}
	s, e := f.ex.Open(f.ctx, f.agent, "open-parent", api.Open{TaskID: parent.Task.ID, BasisVersion: f.basis(t, parent.Task.ID)})
	if e != nil {
		t.Fatal(e)
	}
	work := s.Execution.ID
	if v, _ := f.query.Read(f.ctx, f.agent, parent.Task.ID, 0); v.CanSubmit || v.Blocked[api.ActionSubmit].Code != api.ReasonChildrenPending {
		t.Fatal("submit not blocked by the subtask", v.Blocked)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "early", api.Submit{TaskID: parent.Task.ID, ExecutionID: work, Body: "too early"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("parent delivered before its required subtask", e)
	}
	if s = f.read(t, parent.Task.ID); s.Result != nil {
		t.Fatal("rejected submission left a result", s.Result)
	}

	events, _ := build.GetByID[record.EventLog](f.graph, "sql")
	before, _ := events.ReadEvents(f.ctx, "t", 0, 1000)
	f.finishTask(t, child.Task.ID)
	after, _ := events.ReadEvents(f.ctx, "t", 0, 1000)
	announced := false
	for _, ev := range after[len(before):] {
		announced = announced || ev.Resource == parent.Task.ID
	}
	if !announced {
		t.Fatal("parent not told its subtask changed")
	}
	if s = f.read(t, parent.Task.ID); s.Position.Status != "active" || s.Position.Waiting == "等待必需子任务完成" {
		t.Fatal("subtask completion must only unblock the parent", s.Position)
	}
	// The optional subtask is still open and does not block.
	s, e = f.results.Submit(f.ctx, f.agent, "summary", api.Submit{TaskID: parent.Task.ID, ExecutionID: work, Body: "summary"})
	if e != nil || s.Position.State != "accepting" {
		t.Fatal("parent could not deliver once subtasks completed", s.Position, e)
	}
}

// Changing which subtasks are required changes the parent's delivery basis;
// other relation changes do not.
func TestPlanChangesAdvanceParentBasis(t *testing.T) {
	f := setup(t, nil)
	parent, child, extra := f.named(t, "parent"), f.named(t, "child"), f.named(t, "extra")
	s := f.open(t, parent)
	s, e := f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: parent.Task.ID, ExecutionID: s.Execution.ID, Body: "result"})
	if e != nil || s.Position.State != "accepting" {
		t.Fatal(s.Position, e)
	}
	old := s.Execution.ID

	f.attach(t, "optional", extra.Task.ID, parent.Task.ID, false)
	if f.basis(t, parent.Task.ID) != 1 {
		t.Fatal("an optional subtask changed the delivery basis")
	}
	f.attach(t, "required", child.Task.ID, parent.Task.ID, true)
	if f.basis(t, parent.Task.ID) != 2 {
		t.Fatal("a required subtask did not advance the basis")
	}
	s = f.read(t, parent.Task.ID)
	if s.Position.State != "executing" || s.Position.Waiting != "等待必需子任务完成" {
		t.Fatal("result on the old plan still counts", s.Position)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "stale", api.Submit{TaskID: parent.Task.ID, ExecutionID: old, Body: "stale"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("execution on the old plan submitted", e)
	}
	timeline, _ := f.commits.List(f.ctx, "t", parent.Task.ID, 0, 100)
	if last := timeline[len(timeline)-1]; last.Kind != plan.KindChanged || last.BasisVersion != 2 {
		t.Fatal("plan change not recorded under the new basis", last)
	}

	p := f.plan(t)
	// Repeating the same placement changes nothing and advances nothing.
	if e = p.Place(f.ctx, f.owner, "same", plan.Placement{ChildID: child.Task.ID, ParentID: parent.Task.ID, Required: true, SeenParentID: parent.Task.ID, SeenRequired: true}); e != nil || f.basis(t, parent.Task.ID) != 2 {
		t.Fatal("no-op placement advanced the basis", e)
	}
	if e = p.Place(f.ctx, f.owner, "relax", plan.Placement{ChildID: child.Task.ID, ParentID: parent.Task.ID, Required: false, SeenParentID: parent.Task.ID, SeenRequired: true}); e != nil || f.basis(t, parent.Task.ID) != 3 {
		t.Fatal("dropping a requirement did not advance the basis", e)
	}
	// There is no detach: a placement must name a parent.
	if e = p.Place(f.ctx, f.owner, "detach", plan.Placement{ChildID: extra.Task.ID, SeenParentID: parent.Task.ID}); !errors.Is(e, fault.ErrInput) || f.basis(t, parent.Task.ID) != 3 {
		t.Fatal("detach accepted", e)
	}
	if v := f.read(t, parent.Task.ID); len(v.Children) != 2 || v.Children[0].Required || v.Children[1].Required {
		t.Fatal("plan after changes", v.Children)
	}
}

// Moving a required subtask changes both parents at once.
func TestMovingRequiredSubtaskChangesBothParents(t *testing.T) {
	f := setup(t, nil)
	a, b, child := f.named(t, "a"), f.named(t, "b"), f.named(t, "child")
	f.attach(t, "under-a", child.Task.ID, a.Task.ID, true)
	e := f.plan(t).Place(f.ctx, f.owner, "move", plan.Placement{ChildID: child.Task.ID, ParentID: b.Task.ID, Required: true, SeenParentID: a.Task.ID, SeenRequired: true})
	if e != nil {
		t.Fatal(e)
	}
	if f.basis(t, a.Task.ID) != 3 || f.basis(t, b.Task.ID) != 2 {
		t.Fatal("move did not advance both parents", f.basis(t, a.Task.ID), f.basis(t, b.Task.ID))
	}
	if v := f.read(t, a.Task.ID); len(v.Children) != 0 || v.Position.Waiting == "等待必需子任务完成" {
		t.Fatal("old parent still waits on the moved subtask", v.Children, v.Position)
	}
	if v := f.read(t, child.Task.ID); v.Parent == nil || v.Parent.TaskID != b.Task.ID {
		t.Fatal("child not under the new parent", v.Parent)
	}
}

// Placements are checked against what the caller saw, the tree shape and
// the parent's phase, not the project; agents cannot plan.
func TestPlacementGuards(t *testing.T) {
	f := setup(t, nil)
	a, b, c := f.named(t, "a"), f.named(t, "b"), f.named(t, "c")
	p := f.plan(t)
	place := func(key string, in plan.Placement) error { return p.Place(f.ctx, f.owner, key, in) }
	if e := place("self", plan.Placement{ChildID: a.Task.ID, ParentID: a.Task.ID}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("self parent", e)
	}
	f.attach(t, "b-under-a", b.Task.ID, a.Task.ID, false)
	f.attach(t, "c-under-b", c.Task.ID, b.Task.ID, false)
	if e := place("cycle", plan.Placement{ChildID: a.Task.ID, ParentID: c.Task.ID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("cycle accepted", e)
	}
	// A child must be newer than its parent, even where no cycle would form.
	newer := f.named(t, "newer")
	if e := place("older-under-newer", plan.Placement{ChildID: a.Task.ID, ParentID: newer.Task.ID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("older task placed under a newer parent", e)
	}
	if e := place("newer-under-older", plan.Placement{ChildID: newer.Task.ID, ParentID: b.Task.ID}); e != nil {
		t.Fatal(e)
	}
	if e := place("unseen", plan.Placement{ChildID: c.Task.ID, ParentID: a.Task.ID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("placement on an unseen relation", e)
	}
	// Only the child's relation is pinned: a parent whose basis moved meanwhile does not conflict.
	if _, e := f.tasks.Edit(f.ctx, f.owner, "retitle-b", workspace.Edit{TaskID: b.Task.ID, Title: "b2", Goal: "goal", Criteria: "criteria", BasisVersion: f.basis(t, b.Task.ID)}); e != nil {
		t.Fatal(e)
	}
	if e := place("after-edit", plan.Placement{ChildID: c.Task.ID, ParentID: b.Task.ID, Required: true, SeenParentID: b.Task.ID}); e != nil {
		t.Fatal("unrelated parent change blocked the placement", e)
	}
	if e := p.Place(f.ctx, f.agent, "agent", plan.Placement{ChildID: c.Task.ID, ParentID: b.Task.ID, SeenParentID: b.Task.ID, SeenRequired: true}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("agent changed the plan", e)
	}

	// Projects only organize: a tree may span projects and a node may move alone.
	cat, ok := build.GetByID[catalog.Service](f.graph, "catalog")
	if !ok {
		t.Fatal("catalog not built")
	}
	project, e := cat.SaveProject(f.ctx, f.owner, "project", catalog.ProjectEdit{Name: "p", Owner: "owner"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.tasks.Commands.SetProject(f.ctx, f.owner, "move-b", workspace.ProjectMove{TaskID: b.Task.ID, ProjectID: project}); e != nil {
		t.Fatal("tree node could not move project", e)
	}
	lone := f.named(t, "lone")
	if e = f.tasks.Commands.SetProject(f.ctx, f.owner, "move-lone", workspace.ProjectMove{TaskID: lone.Task.ID, ProjectID: project}); e != nil {
		t.Fatal(e)
	}
	if e = place("cross", plan.Placement{ChildID: lone.Task.ID, ParentID: a.Task.ID}); e != nil {
		t.Fatal("cross-project link refused", e)
	}

	// A terminal parent's plan is frozen.
	if _, e = f.controls.Cancel(f.ctx, f.owner, "cancel-a", api.Cancel{TaskID: a.Task.ID}); e != nil {
		t.Fatal(e)
	}
	if e = place("frozen", plan.Placement{ChildID: b.Task.ID, ParentID: a.Task.ID, Required: true, SeenParentID: a.Task.ID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("cancelled parent's plan changed", e)
	}
}

// A relation grants no access: a reader of the parent only learns that a
// required subtask it cannot read exists.
func TestHiddenSubtaskRevealsNothing(t *testing.T) {
	f := setup(t, nil)
	parent, child := f.named(t, "parent"), f.named(t, "child")
	f.attach(t, "attach", child.Task.ID, parent.Task.ID, true)
	if e := f.tasks.Commands.SetParticipant(f.ctx, f.owner, "join", workspace.Participation{TaskID: parent.Task.ID, Subject: "agent", Present: true}); e != nil {
		t.Fatal(e)
	}
	v, e := f.query.Read(f.ctx, f.agent, parent.Task.ID, 0)
	if e != nil {
		t.Fatal(e)
	}
	if len(v.Children) != 0 || !v.HiddenRequired {
		t.Fatal("hidden subtask leaked or unannounced", v.Children, v.HiddenRequired)
	}
	if _, e = f.query.Read(f.ctx, f.agent, child.Task.ID, 0); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("relation granted access to the subtask", e)
	}
}

// A cancelled required subtask does not satisfy the parent, and replaying a
// placement returns its receipt without advancing the basis again.
func TestCancelledSubtaskBlocksAndReplayIsStable(t *testing.T) {
	f := setup(t, nil)
	parent, child := f.named(t, "parent"), f.named(t, "child")
	in := plan.Placement{ChildID: child.Task.ID, ParentID: parent.Task.ID, Required: true}
	p := f.plan(t)
	for i := 0; i < 2; i++ {
		if e := p.Place(f.ctx, f.owner, "same-key", in); e != nil {
			t.Fatal(e)
		}
	}
	if f.basis(t, parent.Task.ID) != 2 {
		t.Fatal("replay advanced the basis again", f.basis(t, parent.Task.ID))
	}
	if _, e := f.controls.Cancel(f.ctx, f.owner, "cancel-child", api.Cancel{TaskID: child.Task.ID}); e != nil {
		t.Fatal(e)
	}
	if _, e := f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{TaskID: parent.Task.ID, Subject: "agent"}); e != nil {
		t.Fatal(e)
	}
	s, e := f.ex.Open(f.ctx, f.agent, "open", api.Open{TaskID: parent.Task.ID, BasisVersion: 2})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: parent.Task.ID, ExecutionID: s.Execution.ID, Body: "summary"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("cancelled subtask satisfied the parent", e)
	}
}

// Waiting on required subtasks blocks delivery, not ownership: the parent
// leaves "waiting on me" but stays listed as the caller's blocked task, an
// open question still reaches its addressee, and the last subtask completing
// puts the parent back in its assignee's inbox.
func TestBlockedParentLeavesInboxUntilSubtasksComplete(t *testing.T) {
	f := setup(t, nil)
	parent, child := f.named(t, "parent"), f.named(t, "child")
	f.attach(t, "attach", child.Task.ID, parent.Task.ID, true)
	if _, e := f.ex.Assign(f.ctx, f.owner, "assign-parent", api.Assignment{TaskID: parent.Task.ID, Subject: "agent"}); e != nil {
		t.Fatal(e)
	}
	s := f.read(t, parent.Task.ID)
	if !s.Position.WaitingOnSubtasks || s.NextActor != "" || s.Position.State != "executing" {
		t.Fatal("waiting on subtasks is not a blocked wait", s.Position, s.NextActor)
	}
	inbox := func(token string) map[string]pending {
		items, e := inbox(f.ctx, f.query, token, nil)
		if e != nil {
			t.Fatal(e)
		}
		out := map[string]pending{}
		for _, x := range items {
			out[x.TaskID] = x
		}
		return out
	}
	if x, ok := inbox(f.agent)[parent.Task.ID]; !ok || !x.WaitingOnSubtasks || x.NextActor != "" {
		t.Fatal("assignee does not see its blocked task as blocked", x, ok)
	}
	if x := inbox(f.owner)[parent.Task.ID]; !x.WaitingOnSubtasks {
		t.Fatal("owner does not see its blocked task as blocked", x)
	}
	// The owner is still asked to act on the subtask itself.
	if x := inbox(f.owner)[child.Task.ID]; x.WaitingOnSubtasks || x.NextActor != api.ActorOwner {
		t.Fatal("subtask action not routed to the owner", x)
	}
	// An open question is actionable even while blocked.
	if e := f.tasks.Commands.SetParticipant(f.ctx, f.owner, "join", workspace.Participation{TaskID: parent.Task.ID, Subject: "agent", Present: true}); e != nil {
		t.Fatal(e)
	}
	s, e := f.ex.Open(f.ctx, f.agent, "open-parent", api.Open{TaskID: parent.Task.ID, BasisVersion: f.basis(t, parent.Task.ID)})
	if e != nil {
		t.Fatal(e)
	}
	q := f.questions()
	s, e = q.Ask(f.ctx, f.agent, "ask", api.Ask{TaskID: parent.Task.ID, RoundID: s.Position.RoundID, Body: "which format?"})
	if e != nil || s.NextActor != api.ActorAddressee {
		t.Fatal("question hidden by the block", s.NextActor, e)
	}
	if _, e = q.Answer(f.ctx, f.owner, "answer", api.Answer{TaskID: parent.Task.ID, QuestionID: s.Questions[0].ID, Body: "markdown"}); e != nil {
		t.Fatal(e)
	}
	f.finishTask(t, child.Task.ID)
	s = f.read(t, parent.Task.ID)
	if s.Position.WaitingOnSubtasks || s.NextActor != api.ActorAssignee {
		t.Fatal("parent not unblocked", s.Position, s.NextActor)
	}
	if x, ok := inbox(f.agent)[parent.Task.ID]; !ok || x.WaitingOnSubtasks || x.NextActor != api.ActorAssignee {
		t.Fatal("assignee not asked to deliver the parent", x, ok)
	}
}

// Creating under a parent is one command: the task, its link and the parent's
// new basis commit together, or nothing does.
func TestCreateUnderParentIsAtomic(t *testing.T) {
	f := setup(t, nil)
	parent := f.named(t, "parent")
	in := workspace.Create{Title: "child", Goal: "goal", Criteria: "criteria", ParentID: parent.Task.ID, Required: true}
	ref, e := f.tasks.Commands.Create(f.ctx, f.owner, "create-child", in)
	if e != nil {
		t.Fatal(e)
	}
	again, e := f.tasks.Commands.Create(f.ctx, f.owner, "create-child", in)
	if e != nil || again.TaskID != ref.TaskID {
		t.Fatal("replay created another subtask", again, e)
	}
	s := f.read(t, parent.Task.ID)
	if len(s.Children) != 1 || s.Children[0].TaskID != ref.TaskID || !s.Children[0].Required || !s.Position.WaitingOnSubtasks {
		t.Fatal("subtask not placed with its creation", s.Children, s.Position)
	}
	if f.basis(t, parent.Task.ID) != 2 {
		t.Fatal("required subtask did not advance the parent basis once", f.basis(t, parent.Task.ID))
	}

	// A frozen parent rejects the placement, and the task is not created either.
	if _, e = f.controls.Cancel(f.ctx, f.owner, "cancel", api.Cancel{TaskID: parent.Task.ID}); e != nil {
		t.Fatal(e)
	}
	before, _, _ := f.query.List(f.ctx, f.owner, api.ListFilter{})
	in.Title = "late"
	if _, e = f.tasks.Commands.Create(f.ctx, f.owner, "create-late", in); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("subtask created under a cancelled parent", e)
	}
	if after, _, _ := f.query.List(f.ctx, f.owner, api.ListFilter{}); len(after) != len(before) {
		t.Fatal("failed placement left a task behind", len(before), len(after))
	}
	if _, e = f.tasks.Commands.Create(f.ctx, f.agent, "agent-create", workspace.Create{Title: "x", ParentID: parent.Task.ID}); e == nil {
		t.Fatal("agent created a subtask")
	}
	if _, e = f.tasks.Commands.Create(f.ctx, f.owner, "orphan-required", workspace.Create{Title: "x", Goal: "g", Criteria: "c", Required: true}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("Required without a parent was accepted", e)
	}
}
