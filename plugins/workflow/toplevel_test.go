package workflow_test

import (
	"errors"
	"testing"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
)

// C25: an Agent creates a top-level task through the same command as a
// subtask, without ParentID. Its owner answers for it unless Owner names
// another person; the Agent follows it as a participant.
func TestAgentCreatesTopLevelTask(t *testing.T) {
	f := setup(t, nil)
	lead := f.human(t, "lead")
	create := func(key string, in workspace.Create) (string, error) {
		if in.Title == "" {
			in.Title = "top"
		}
		ref, e := f.tasks.Commands.Create(f.ctx, f.agent, key, in)
		return ref.TaskID, e
	}

	id, e := create("mine", workspace.Create{Goal: "g", Assignee: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	s := f.read(t, id)
	v, _ := f.query.Read(f.ctx, f.owner, id, 0)
	if v.Task.Owner != "owner" || s.Position.Acceptance != flow.AcceptanceNone || s.Assignee != "agent" {
		t.Fatal("owned by the Agent's owner, no review by default, assigned", v.Task.Owner, s.Position.Acceptance, s.Assignee)
	}
	if !f.visible(f.agent, id) {
		t.Fatal("the creating Agent cannot follow its task")
	}
	// It works the task like any other: claim and deliver.
	s, e = f.ex.Open(f.ctx, f.agent, "open-mine", api.Open{TaskID: id, BasisVersion: 1})
	if e != nil {
		t.Fatal(e)
	}
	if s, e = f.results.Submit(f.ctx, f.agent, "submit-mine", api.Submit{TaskID: id, ExecutionID: s.Execution.ID, Body: "done"}); e != nil || s.Position.Status != flow.PhaseCompleted {
		t.Fatal("top-level none task did not complete", s.Position.Status, e)
	}

	// Owner names another person; review may be asked for.
	id, e = create("for-lead", workspace.Create{Owner: "lead", Acceptance: flow.AcceptanceManual})
	if e != nil {
		t.Fatal(e)
	}
	if v, e := f.query.Read(f.ctx, lead, id, 0); e != nil || v.Task.Owner != "lead" || v.Position.Acceptance != flow.AcceptanceManual {
		t.Fatal("named owner", v.Task.Owner, e)
	}

	// Owners are people; Required needs a parent.
	for _, owner := range []string{"agent", "nobody"} {
		if _, e = create("owner-"+owner, workspace.Create{Owner: owner}); !errors.Is(e, fault.ErrInput) {
			t.Fatal("owner must be an active person", owner, e)
		}
	}
	if _, e = create("required", workspace.Create{Required: true}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("Required without a parent", e)
	}

	// Filing under a project still needs the Agent to belong to it.
	c := f.catalog(t)
	project, e := c.SaveProject(f.ctx, lead, "project", catalog.ProjectEdit{Name: "p", Owner: "lead"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = create("outsider", workspace.Create{ProjectID: project}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("Agent filed into a project it does not belong to", e)
	}
	if _, e = c.SaveProject(f.ctx, lead, "project-2", catalog.ProjectEdit{ID: project, Name: "p", Owner: "lead", Members: []string{"agent"}, ExpectedVersion: 1}); e != nil {
		t.Fatal(e)
	}
	if _, e = create("member", workspace.Create{ProjectID: project}); e != nil {
		t.Fatal("project member Agent could not file", e)
	}

	// The deployment's assignment policy applies at the top level too.
	g := setup(t, map[string]any{"workspace": map[string]any{"config": map[string]any{"agentAssign": "none"}}})
	if _, e = g.tasks.Commands.Create(g.ctx, g.agent, "policy", workspace.Create{Title: "t", Goal: "g", Assignee: "agent"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("assignment policy ignored at the top level", e)
	}

	// With its owner gone, the Agent must name one.
	if e = f.security.Create(f.ctx, f.owner, "create-helper", identity.Principal{Tenant: "t", ID: "helper", Kind: "agent", OwnerID: "lead"}); e != nil {
		t.Fatal(e)
	}
	helper := f.credential(t, "helper", "issue-helper")
	f.human(t, "third")
	if e = f.deactivate(t, "lead"); e != nil {
		t.Fatal(e)
	}
	if _, e = f.tasks.Commands.Create(f.ctx, helper, "orphan", workspace.Create{Title: "t"}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("task created for an inactive owner", e)
	}
	if _, e = f.tasks.Commands.Create(f.ctx, helper, "orphan-named", workspace.Create{Title: "t", Owner: "third"}); e != nil {
		t.Fatal(e)
	}
}

// A person may also name another owner at the top level; the creator then
// follows the task and assigns it only as an administrator.
func TestPersonNamesTopLevelOwner(t *testing.T) {
	f := setup(t, nil)
	lead := f.human(t, "lead")
	f.human(t, "third")
	ref, e := f.tasks.Commands.Create(f.ctx, lead, "for-third", workspace.Create{Title: "t", Goal: "g", Owner: "third"})
	if e != nil {
		t.Fatal(e)
	}
	v, e := f.query.Read(f.ctx, lead, ref.TaskID, 0)
	if e != nil || v.Task.Owner != "third" {
		t.Fatal("named owner, creator still reads", v.Task.Owner, e)
	}
	if _, e = f.tasks.Commands.Create(f.ctx, lead, "assign-third", workspace.Create{Title: "t", Goal: "g", Owner: "third", Assignee: "agent"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("non-owner assigned a new task", e)
	}
	if _, e = f.tasks.Commands.Create(f.ctx, f.owner, "admin-assign", workspace.Create{Title: "t", Goal: "g", Owner: "third", Assignee: "agent"}); e != nil {
		t.Fatal("administrator could not assign", e)
	}
}
