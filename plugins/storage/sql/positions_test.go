package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/task"
)

func TestSavePositionRejectsStaleRevision(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "store.db"))
	ctx := context.Background()
	p := flow.Position{Tenant: "t", TaskID: "a", RoundID: "r", State: "ready", Status: flow.PhaseActive}
	e := s.Do(ctx, func(tx context.Context) error {
		if e := s.CreateTask(tx, task.Task{Tenant: "t", ID: "a", Version: 1}); e != nil {
			return e
		}
		if e := s.CreatePosition(tx, p); e != nil {
			return e
		}
		p.Revision, p.Waiting = 1, "first"
		return s.SavePosition(tx, p, 0)
	})
	if e != nil {
		t.Fatal(e)
	}
	stale := p
	stale.Revision, stale.Waiting = 1, "lost update"
	if e = s.Do(ctx, func(tx context.Context) error { return s.SavePosition(tx, stale, 0) }); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("stale writer accepted", e)
	}
	got, e := s.GetPosition(ctx, "t", "a")
	if e != nil || got.Revision != 1 || got.Waiting != "first" {
		t.Fatalf("position overwritten %+v %v", got, e)
	}
}
