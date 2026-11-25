package workflow_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/cap/workspace"
)

// Paging returns every task exactly once across pages, in ID order, and the
// last page carries no cursor; without Limit the whole list comes back.
func TestListPagesCoverEveryTaskOnce(t *testing.T) {
	f := setup(t, nil)
	for i := 0; i < 5; i++ {
		if _, e := f.tasks.Create(f.ctx, f.owner, fmt.Sprint("create-", i), workspace.Create{Title: fmt.Sprint("task ", i), Goal: "goal"}); e != nil {
			t.Fatal(e)
		}
	}
	all, next, e := f.query.List(f.ctx, f.owner, api.ListFilter{})
	if e != nil || len(all) != 5 || next != "" {
		t.Fatal("unpaged list", len(all), next, e)
	}
	seen, cursor, pages := map[string]bool{}, "", 0
	for {
		rows, next, e := f.query.List(f.ctx, f.owner, api.ListFilter{Limit: 2, Cursor: cursor})
		if e != nil {
			t.Fatal(e)
		}
		pages++
		if len(rows) > 2 {
			t.Fatal("page over limit", len(rows))
		}
		for _, r := range rows {
			if seen[r.Task.ID] {
				t.Fatal("task on two pages", r.Task.ID)
			}
			seen[r.Task.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != 5 || pages != 3 {
		t.Fatal("pages", pages, len(seen))
	}
	// An exactly full last page has no cursor either.
	rows, next, e := f.query.List(f.ctx, f.owner, api.ListFilter{Limit: 5})
	if e != nil || len(rows) != 5 || next != "" {
		t.Fatal("full single page", len(rows), next, e)
	}
	for _, bad := range []api.ListFilter{{Limit: -1}, {Limit: api.MaxListLimit + 1}, {Limit: 2, IDs: []string{"x"}}, {Cursor: "x"}} {
		if _, _, e := f.query.List(f.ctx, f.owner, bad); !errors.Is(e, fault.ErrInput) {
			t.Fatal("bad filter accepted", bad, e)
		}
	}
}
