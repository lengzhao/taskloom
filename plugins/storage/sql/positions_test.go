package sql_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/task"
)

// Positions keep only stored fields; a save replaces the previous value.
func TestSavePositionReplacesStoredFields(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "store.db"))
	ctx := context.Background()
	p := flow.Position{Tenant: "t", TaskID: "a", RoundID: "r", Acceptance: flow.AcceptanceManual, Status: flow.PhaseActive}
	e := s.Do(ctx, func(tx context.Context) error {
		if e := s.CreateTask(tx, task.Task{Tenant: "t", ID: "a", BasisVersion: 1}); e != nil {
			return e
		}
		if e := s.CreatePosition(tx, p); e != nil {
			return e
		}
		p.RoundID, p.Status = "second", flow.PhaseCompleted
		return s.SavePosition(tx, p)
	})
	if e != nil {
		t.Fatal(e)
	}
	got, e := s.GetPosition(ctx, "t", "a")
	if e != nil || got != p {
		t.Fatalf("position %+v %v", got, e)
	}
}
