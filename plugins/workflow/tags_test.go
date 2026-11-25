package workflow_test

import (
	"errors"
	"fmt"
	"slices"
	"testing"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/catalog"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
)

// tag creates a vocabulary entry as the administrator.
func (f *fixture) tag(t *testing.T, name string) catalog.Tag {
	t.Helper()
	c := f.catalog(t)
	id, e := c.SaveTag(f.ctx, f.owner, "tag-"+name, catalog.TagEdit{Name: name})
	if e != nil {
		t.Fatal(e)
	}
	v, e := c.Tag(f.ctx, f.owner, id)
	if e != nil {
		t.Fatal(e)
	}
	return v
}

func (f *fixture) taskTags(t *testing.T, id string) []string {
	t.Helper()
	v, e := f.query.Read(f.ctx, f.owner, id, 0)
	if e != nil {
		t.Fatal(e)
	}
	return v.Task.Tags
}

// C24: the vocabulary is the administrator's; anyone who creates a task may
// pick from it; afterwards only the owner or an administrator changes tags.
func TestTagVocabulary(t *testing.T) {
	f := setup(t, nil)
	lead := f.human(t, "lead")
	c := f.catalog(t)
	security := f.tag(t, "安全")
	if _, e := c.SaveTag(f.ctx, lead, "by-member", catalog.TagEdit{Name: "x"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("a member maintained the vocabulary", e)
	}
	if _, e := c.SaveTag(f.ctx, f.agent, "by-agent", catalog.TagEdit{Name: "x"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("an Agent maintained the vocabulary", e)
	}
	if _, e := c.SaveTag(f.ctx, f.owner, "dup", catalog.TagEdit{Name: " 安全 "}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("duplicate name accepted", e)
	}
	if _, e := c.SaveTag(f.ctx, f.owner, "dup-case", catalog.TagEdit{Name: "Debt"}); e != nil {
		t.Fatal(e)
	}
	if _, e := c.SaveTag(f.ctx, f.owner, "dup-case-2", catalog.TagEdit{Name: "debt"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("names differing only in case accepted", e)
	}
	if _, e := c.SaveTag(f.ctx, f.owner, "stale", catalog.TagEdit{ID: security.ID, Name: "安全", ExpectedVersion: 0}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("stale version accepted", e)
	}
	for _, who := range []string{lead, f.agent} {
		tags, e := c.Tags(f.ctx, who)
		if e != nil || len(tags) != 2 {
			t.Fatal("members and Agents read the vocabulary", len(tags), e)
		}
	}
}

func TestTaskTags(t *testing.T) {
	f := setup(t, nil)
	lead := f.human(t, "lead")
	a, b := f.tag(t, "a"), f.tag(t, "b")
	old := f.tag(t, "old")
	if _, e := f.catalog(t).SaveTag(f.ctx, f.owner, "archive", catalog.TagEdit{ID: old.ID, Name: old.Name, Archived: true, ExpectedVersion: old.Version}); e != nil {
		t.Fatal(e)
	}

	// Chosen at creation; unknown or archived tags roll the creation back.
	d, e := f.tasks.Create(f.ctx, f.owner, "tagged", workspace.Create{Title: "t", Goal: "g", Tags: []string{a.ID, a.ID}})
	if e != nil || !slices.Equal(d.Task.Tags, []string{a.ID}) {
		t.Fatal("tags at creation", d.Task.Tags, e)
	}
	before := len(f.taskList(t))
	for key, tags := range map[string][]string{"unknown": {"nope"}, "archived": {old.ID}} {
		if _, e := f.tasks.Create(f.ctx, f.owner, key, workspace.Create{Title: "t", Tags: tags}); !errors.Is(e, fault.ErrInput) {
			t.Fatal(key, "tag accepted", e)
		}
	}
	if after := len(f.taskList(t)); after != before {
		t.Fatal("a refused creation left a task", before, after)
	}

	// The owner adds and removes; tags change no basis and fence no execution.
	f.open(t, d)
	if e = f.tasks.SetTags(f.ctx, f.owner, "add", workspace.TagChange{TaskID: d.Task.ID, Add: []string{b.ID}, Remove: []string{a.ID}}); e != nil {
		t.Fatal(e)
	}
	if got := f.taskTags(t, d.Task.ID); !slices.Equal(got, []string{b.ID}) {
		t.Fatal("add/remove", got)
	}
	s := f.read(t, d.Task.ID)
	if f.basis(t, d.Task.ID) != 1 || s.Execution == nil || !s.Execution.Writable {
		t.Fatal("tags changed the basis or fenced the execution", f.basis(t, d.Task.ID), s.Execution)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "done"}); e != nil {
		t.Fatal("execution lost after a tag change", e)
	}

	// Only the owner or an administrator changes tags afterwards.
	if e = f.tasks.SetTags(f.ctx, f.agent, "agent", workspace.TagChange{TaskID: d.Task.ID, Add: []string{a.ID}}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("the handling Agent changed tags", e)
	}
	if e = f.tasks.SetTags(f.ctx, lead, "stranger", workspace.TagChange{TaskID: d.Task.ID, Add: []string{a.ID}}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("a non-owner changed tags", e)
	}
	if e = f.tasks.SetTags(f.ctx, f.owner, "archived", workspace.TagChange{TaskID: d.Task.ID, Add: []string{old.ID}}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("archived tag added", e)
	}

	// An archived tag already on a task stays and can be removed.
	d2, e := f.tasks.Create(f.ctx, f.owner, "keep", workspace.Create{Title: "t", Tags: []string{b.ID}})
	if e != nil {
		t.Fatal(e)
	}
	v, _ := f.catalog(t).Tag(f.ctx, f.owner, b.ID)
	if _, e = f.catalog(t).SaveTag(f.ctx, f.owner, "archive-b", catalog.TagEdit{ID: b.ID, Name: b.Name, Archived: true, ExpectedVersion: v.Version}); e != nil {
		t.Fatal(e)
	}
	if got := f.taskTags(t, d2.Task.ID); !slices.Equal(got, []string{b.ID}) {
		t.Fatal("archiving removed the tag from a task", got)
	}
	if e = f.tasks.SetTags(f.ctx, f.owner, "drop", workspace.TagChange{TaskID: d2.Task.ID, Remove: []string{b.ID}}); e != nil || len(f.taskTags(t, d2.Task.ID)) != 0 {
		t.Fatal("archived tag could not be removed", e)
	}

	// At most catalog.MaxTaskTags.
	many := []string{}
	for i := 0; i <= catalog.MaxTaskTags; i++ {
		many = append(many, f.tag(t, fmt.Sprintf("t%d", i)).ID)
	}
	if _, e = f.tasks.Create(f.ctx, f.owner, "many", workspace.Create{Title: "t", Tags: many}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("too many tags accepted", e)
	}
}

// An Agent splitting its task may tag the subtask; subtasks do not inherit.
func TestAgentTagsItsSubtask(t *testing.T) {
	f := setup(t, nil)
	a, b := f.tag(t, "a"), f.tag(t, "b")
	parent, e := f.tasks.Create(f.ctx, f.owner, "parent", workspace.Create{Title: "p", Goal: "g", Tags: []string{a.ID}})
	if e != nil {
		t.Fatal(e)
	}
	f.open(t, parent)
	ref, e := f.tasks.Commands.Create(f.ctx, f.agent, "split", workspace.Create{Title: "part", Goal: "g", ParentID: parent.Task.ID, Tags: []string{b.ID}})
	if e != nil {
		t.Fatal(e)
	}
	if got := f.taskTags(t, ref.TaskID); !slices.Equal(got, []string{b.ID}) {
		t.Fatal("subtask tags: only what the Agent chose", got)
	}
}

func (f *fixture) taskList(t *testing.T) []api.Summary {
	t.Helper()
	l, _, e := f.query.List(f.ctx, f.owner, api.ListFilter{})
	if e != nil {
		t.Fatal(e)
	}
	return l
}
