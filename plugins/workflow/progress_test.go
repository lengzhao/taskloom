package workflow_test

import (
	"testing"
	"time"

	"github.com/lengzhao/pluginkit/build"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
)

// Progress is a timestamp the owner reads; nothing expires on it.
func TestExecutorProgressIsRecordedWithoutChangingTheFlow(t *testing.T) {
	f := setup(t, nil)
	qs, _ := build.GetByID[api.QuestionCommands](f.graph, "question-commands")
	d := f.create(t)
	s := f.open(t, d)
	opened := s.Execution.ProgressAt
	if opened.IsZero() {
		t.Fatal("open is progress")
	}
	at := func() time.Time { t.Helper(); return f.read(t, d.Task.ID).Execution.ProgressAt }
	position := f.read(t, d.Task.ID).Position

	time.Sleep(5 * time.Millisecond)
	if _, e := f.tasks.Comment(f.ctx, f.owner, "owner-note", workspace.Comment{TaskID: d.Task.ID, Body: "加油"}); e != nil {
		t.Fatal(e)
	}
	if !at().Equal(opened) {
		t.Fatal("owner comment counted as executor progress")
	}
	if _, e := f.tasks.Comment(f.ctx, f.agent, "progress", workspace.Comment{TaskID: d.Task.ID, Body: "完成一半"}); e != nil {
		t.Fatal(e)
	}
	commented := at()
	if !commented.After(opened) {
		t.Fatal("agent comment not recorded as progress")
	}
	if r := f.read(t, d.Task.ID); r.Position != position {
		t.Fatal("progress changed the flow")
	}

	time.Sleep(5 * time.Millisecond)
	if _, e := qs.Ask(f.ctx, f.agent, "ask", api.Ask{TaskID: d.Task.ID, RoundID: s.Position.RoundID, Body: "用哪种格式？"}); e != nil {
		t.Fatal(e)
	}
	asked := at()
	if !asked.After(commented) {
		t.Fatal("question not recorded as progress")
	}

	time.Sleep(5 * time.Millisecond)
	r := f.read(t, d.Task.ID)
	if _, e := f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: r.Execution.ID, Body: "result"}); e != nil {
		t.Fatal(e)
	}
	if !at().After(asked) {
		t.Fatal("submit not recorded as progress")
	}
}
