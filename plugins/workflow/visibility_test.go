package workflow_test

import (
	"errors"
	"testing"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
)

func (f *fixture) catalog(t *testing.T) catalog.Service {
	t.Helper()
	c, ok := build.GetByID[catalog.Service](f.graph, "catalog")
	if !ok {
		t.Fatal("catalog not built")
	}
	return c
}

func (f *fixture) ownership(t *testing.T) workspace.Ownership {
	t.Helper()
	o, ok := build.GetByID[workspace.Ownership](f.graph, "ownership-commands")
	if !ok {
		t.Fatal("ownership not built")
	}
	return o
}

func (f *fixture) deactivate(t *testing.T, subject string) error {
	t.Helper()
	return f.security.SetMember(f.ctx, f.owner, "deactivate-"+subject, az.Member{Tenant: "t", Subject: subject, Role: "member", Active: false})
}

func (f *fixture) visible(token, taskID string) bool {
	_, e := f.query.Read(f.ctx, token, taskID, 0)
	return e == nil
}

// A project's owner and members read and take part in every task filed under
// it; filing needs belonging to the project, and only the task owner files.
func TestProjectMembersSeeEveryTaskOfTheProject(t *testing.T) {
	f := setup(t, nil)
	lead, member, outsider := f.human(t, "lead"), f.human(t, "member"), f.human(t, "outsider")
	cat := f.catalog(t)
	project, e := cat.SaveProject(f.ctx, lead, "project", catalog.ProjectEdit{Name: "p", Owner: "lead", Members: []string{"member", "agent", "member"}})
	if e != nil {
		t.Fatal(e)
	}
	if p, _ := cat.Project(f.ctx, lead, project); len(p.Members) != 2 {
		t.Fatal("members deduplicated", p.Members)
	}
	if _, e = cat.SaveProject(f.ctx, lead, "bad-member", catalog.ProjectEdit{ID: project, Name: "p", Owner: "lead", Members: []string{"ghost"}, ExpectedVersion: 1}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("unknown member accepted", e)
	}
	d, e := f.tasks.Create(f.ctx, lead, "filed", workspace.Create{Title: "filed", Goal: "g", Criteria: "c", ProjectID: project})
	if e != nil {
		t.Fatal(e)
	}
	if !f.visible(member, d.Task.ID) || !f.visible(f.agent, d.Task.ID) {
		t.Fatal("project member cannot read the filed task")
	}
	if f.visible(outsider, d.Task.ID) {
		t.Fatal("outsider reads the filed task")
	}
	if e = f.comment(member, "member-comment", d.Task.ID); e != nil {
		t.Fatal("project member cannot comment", e)
	}
	if _, e = f.ex.Assign(f.ctx, member, "member-assign", api.Assignment{TaskID: d.Task.ID, Subject: "member"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("project member managed the task", e)
	}
	// Filing shares a task, so only members of the target project may file into it.
	if _, e = f.tasks.Create(f.ctx, outsider, "outsider-file", workspace.Create{Title: "x", ProjectID: project}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("outsider filed into the project", e)
	}
	own, e := f.tasks.Create(f.ctx, outsider, "outsider-own", workspace.Create{Title: "own"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.tasks.Commands.SetProject(f.ctx, outsider, "outsider-move", workspace.ProjectMove{TaskID: own.Task.ID, ProjectID: project}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("outsider moved a task into the project", e)
	}
	mine, e := f.tasks.Create(f.ctx, member, "member-own", workspace.Create{Title: "mine"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.tasks.Commands.SetProject(f.ctx, member, "member-move", workspace.ProjectMove{TaskID: mine.Task.ID, ProjectID: project}); e != nil {
		t.Fatal("member could not file own task", e)
	}
	if !f.visible(lead, mine.Task.ID) {
		t.Fatal("project owner cannot read a member's filed task")
	}
	// A participant takes part but no longer re-files someone else's task.
	if e = f.tasks.SetParticipant(f.ctx, lead, "add-outsider", workspace.Participation{TaskID: d.Task.ID, Subject: "outsider", Present: true}); e != nil {
		t.Fatal(e)
	}
	if e = f.tasks.Commands.SetProject(f.ctx, outsider, "participant-move", workspace.ProjectMove{TaskID: d.Task.ID}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("participant re-filed the task", e)
	}
	// A member who leaves the tenant drops out of the list on the next save
	// instead of blocking it; naming an unknown subject is still refused.
	if e = f.deactivate(t, "member"); e != nil {
		t.Fatal(e)
	}
	if _, e = cat.SaveProject(f.ctx, lead, "keep-list", catalog.ProjectEdit{ID: project, Name: "p", Owner: "lead", Members: []string{"member", "agent"}, Archived: true, ExpectedVersion: 1}); e != nil {
		t.Fatal("save with a departed member refused", e)
	}
	if p, _ := cat.Project(f.ctx, lead, project); len(p.Members) != 1 || p.Members[0] != "agent" {
		t.Fatal("departed member kept", p.Members)
	}
	// An archived project still shares its tasks with its people.
	if !f.visible(f.agent, d.Task.ID) {
		t.Fatal("archiving ended member access")
	}
	// Leaving the project ends access at once.
	if _, e = cat.SaveProject(f.ctx, lead, "drop-member", catalog.ProjectEdit{ID: project, Name: "p", Owner: "lead", ExpectedVersion: 2}); e != nil {
		t.Fatal(e)
	}
	if f.visible(member, d.Task.ID) || f.visible(f.agent, d.Task.ID) {
		t.Fatal("former member still reads the task")
	}
}

// A handover moves responsibility and review, keeps the old owner reading,
// and pins the owner the caller saw.
func TestHandoverMovesResponsibility(t *testing.T) {
	f := setup(t, nil)
	lead, next, outsider := f.human(t, "lead"), f.human(t, "next"), f.human(t, "outsider")
	o := f.ownership(t)
	d, e := f.tasks.Create(f.ctx, lead, "task", workspace.Create{Title: "t", Goal: "g", Criteria: "c"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.Assign(f.ctx, lead, "assign", api.Assignment{TaskID: d.Task.ID, Subject: "agent"}); e != nil {
		t.Fatal(e)
	}
	s, e := f.ex.Open(f.ctx, f.agent, "open", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion})
	if e != nil {
		t.Fatal(e)
	}
	if s, e = f.results.Submit(f.ctx, f.agent, "result", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "r"}); e != nil {
		t.Fatal(e)
	}
	var last workspace.Ref
	hand := func(token, key, to, seen string) error {
		var e error
		last, e = o.Handover(f.ctx, token, key, workspace.Handover{TaskID: d.Task.ID, Owner: to, SeenOwner: seen, Reason: "调岗"})
		return e
	}
	if e = hand(outsider, "outsider", "outsider", "lead"); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("non-owner handed over", e)
	}
	if e = hand(lead, "to-agent", "agent", "lead"); !errors.Is(e, fault.ErrInput) {
		t.Fatal("handed over to an Agent", e)
	}
	if e = hand(lead, "stale", "next", "someone"); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("stale owner accepted", e)
	}
	if e = hand(lead, "handover", "next", " lead "); e != nil {
		t.Fatal(e)
	}
	first := last
	// A retry of the applied handover returns its result although the caller
	// no longer owns the task; a new request by them writes nothing.
	if e = hand(lead, "handover", "next", "lead"); e != nil || last != first {
		t.Fatal("retry of an applied handover refused", e, last, first)
	}
	if e = hand(lead, "again", "next", "lead"); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("former owner handed over again", e)
	}
	if e = hand(outsider, "handover", "next", "outsider"); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("non-reader passed the retry rule", e)
	}
	got, e := f.query.Read(f.ctx, next, d.Task.ID, 0)
	if e != nil || !got.CanReview || got.NextActor != api.ActorReviewer {
		t.Fatal("new owner cannot review", got.CanReview, got.NextActor, e)
	}
	old, e := f.query.Read(f.ctx, lead, d.Task.ID, 0)
	if e != nil || old.CanReview || old.CanManage {
		t.Fatal("old owner kept responsibility or lost reading", old.CanReview, old.CanManage, e)
	}
	if _, e = f.results.Review(f.ctx, lead, "old-review", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: got.Result.CommitID, Outcome: acceptance.OutcomeApproved}}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("old owner reviewed", e)
	}
	if got.Assignee != "agent" || got.Execution == nil || !got.Execution.Writable {
		t.Fatal("handover touched the execution", got.Assignee, got.Execution)
	}
	if _, e = f.results.Review(f.ctx, next, "review", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: got.Result.CommitID, Outcome: acceptance.OutcomeApproved}}); e != nil {
		t.Fatal(e)
	}
	// Completed tasks can still change hands, e.g. when someone leaves.
	if e = hand(f.owner, "admin-handover", "lead", "next"); e != nil {
		t.Fatal("administrator could not hand over a completed task", e)
	}
	if c := f.commitCount(t, d.Task.ID); c == 0 {
		t.Fatal("no timeline")
	}
}
