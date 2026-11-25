package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/runtime/config"
)

// setupErr only builds the graph with the override, reporting its error.
func setupErr(t *testing.T, override map[string]any) (any, error) {
	t.Helper()
	dir := t.TempDir()
	override["sql"] = map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}}
	b, _ := json.Marshal(override)
	overlay := filepath.Join(dir, "config.yaml")
	if e := os.WriteFile(overlay, b, 0600); e != nil {
		t.Fatal(e)
	}
	_, graph, e := config.BuildFiles(context.Background(), "../../base.yaml", overlay)
	return graph, e
}

// splitting returns a parent task the Agent is executing.
func splitting(t *testing.T, f *fixture) string {
	t.Helper()
	parent := f.create(t)
	f.open(t, parent)
	return parent.Task.ID
}

// C23: an Agent pushes its own split forward. Its subtasks complete on a
// valid result unless it asks for review, and it may assign them, itself
// included, in the same command.
func TestAgentSplitAcceptanceAndAssignment(t *testing.T) {
	f := setup(t, nil)
	lead := f.human(t, "lead")
	parent := splitting(t, f)
	create := func(key string, in workspace.Create) (string, error) {
		in.ParentID = parent
		if in.Title == "" {
			in.Title = "part"
		}
		ref, e := f.tasks.Commands.Create(f.ctx, f.agent, key, in)
		return ref.TaskID, e
	}

	// Default: no review, assigned to itself, claimed and delivered.
	mine, e := create("mine", workspace.Create{Goal: "g", Criteria: "c", Assignee: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	s := f.read(t, mine)
	if s.Position.Acceptance != flow.AcceptanceNone || s.Assignee != "agent" {
		t.Fatal("Agent subtask: none by default, assigned to itself", s.Position.Acceptance, s.Assignee)
	}
	s, e = f.ex.Open(f.ctx, f.agent, "open-mine", api.Open{TaskID: mine, BasisVersion: 1})
	if e != nil {
		t.Fatal("self-assigned subtask could not be claimed", e)
	}
	if s, e = f.results.Submit(f.ctx, f.agent, "submit-mine", api.Submit{TaskID: mine, ExecutionID: s.Execution.ID, Body: "done"}); e != nil || s.Position.Status != flow.PhaseCompleted {
		t.Fatal("none subtask did not complete on its result", s.Position.Status, e)
	}

	// Asked-for review, handed to a person who starts at once.
	theirs, e := create("theirs", workspace.Create{Goal: "g", Criteria: "c", Acceptance: flow.AcceptanceManual, Assignee: "lead"})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.query.Read(f.ctx, lead, theirs)
	if e != nil || s.Position.Acceptance != flow.AcceptanceManual || s.Assignee != "lead" || s.Execution == nil || !s.Execution.Writable {
		t.Fatal("manual subtask assigned to a person", s.Position.Acceptance, s.Assignee, s.Execution, e)
	}

	// Failures leave no task behind.
	before := len(f.read(t, parent).Children)
	if _, e = create("incomplete", workspace.Create{Goal: "g", Assignee: "agent"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("assigned a subtask without criteria", e)
	}
	if _, e = create("bogus", workspace.Create{Goal: "g", Criteria: "c", Acceptance: "later"}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("unknown acceptance mode accepted", e)
	}
	if after := len(f.read(t, parent).Children); after != before {
		t.Fatal("a failed creation left a subtask", before, after)
	}
}

// The deployment limits whom an Agent may assign: self, or nobody.
func TestAgentAssignPolicy(t *testing.T) {
	for _, c := range []struct {
		policy      string
		self, other bool
	}{{"self", true, false}, {"none", false, false}} {
		f := setup(t, map[string]any{"workspace": map[string]any{"config": map[string]any{"agentAssign": c.policy}}})
		f.human(t, "lead")
		parent := splitting(t, f)
		for _, who := range []struct {
			subject string
			allowed bool
		}{{"agent", c.self}, {"lead", c.other}} {
			_, e := f.tasks.Commands.Create(f.ctx, f.agent, c.policy+who.subject, workspace.Create{Title: "part", Goal: "g", Criteria: "c", ParentID: parent, Assignee: who.subject})
			if who.allowed && e != nil || !who.allowed && !errors.Is(e, az.ErrForbidden) {
				t.Fatal(c.policy, who.subject, e)
			}
		}
		if _, e := f.tasks.Commands.Create(f.ctx, f.agent, c.policy+"unassigned", workspace.Create{Title: "part", ParentID: parent}); e != nil {
			t.Fatal("policy blocked an unassigned subtask", c.policy, e)
		}
	}
	if _, e := setupErr(t, map[string]any{"workspace": map[string]any{"config": map[string]any{"agentAssign": "anyone"}}}); e == nil {
		t.Fatal("unknown agentAssign built")
	}
}

// A person may also create, choose acceptance and assign in one command, with
// the same rights the assign command needs.
func TestPersonCreatesAndAssigns(t *testing.T) {
	f := setup(t, nil)
	lead := f.human(t, "lead")
	d, e := f.tasks.Create(f.ctx, lead, "mine", workspace.Create{Title: "t", Goal: "g", Criteria: "c", Acceptance: flow.AcceptanceNone, Assignee: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	if s := f.read(t, d.Task.ID); s.Position.Acceptance != flow.AcceptanceNone || s.Assignee != "agent" {
		t.Fatal("person's acceptance or assignment not applied", s.Position.Acceptance, s.Assignee)
	}
	if s := f.read(t, f.create(t).Task.ID); s.Position.Acceptance != flow.AcceptanceManual {
		t.Fatal("person's task lost the deployment default", s.Position.Acceptance)
	}
	// Owning the parent's plan is not owning the new subtask: naming another
	// owner leaves a non-admin creator without assignment rights.
	f.human(t, "third")
	if _, e = f.tasks.Commands.Create(f.ctx, lead, "for-third", workspace.Create{Title: "t", Goal: "g", Criteria: "c", ParentID: d.Task.ID, Owner: "third", Assignee: "agent"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("non-owner assigned a new task", e)
	}
}
