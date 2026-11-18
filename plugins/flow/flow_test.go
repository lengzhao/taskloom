package flow_test

import (
	"context"
	"errors"
	"github.com/lengzhao/taskloom/cap/fault"
	capflow "github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/task"
	flow "github.com/lengzhao/taskloom/plugins/flow"
	ready "github.com/lengzhao/taskloom/plugins/state/ready"
	store "github.com/lengzhao/taskloom/plugins/storage/sql"
	"path/filepath"
	"testing"
)

type testState struct {
	*ready.State
	result capflow.Check
}

func (s testState) CheckComplete(context.Context, task.Task) (capflow.Check, error) {
	return s.result, nil
}
func TestTypedCompletionAndDefinitionFence(t *testing.T) {
	ctx := context.Background()
	db, e := store.New(store.Config{DSN: filepath.Join(t.TempDir(), "db")})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Open(ctx); e != nil {
		t.Fatal(e)
	}
	defer db.Close(ctx)
	state := &ready.State{}
	controller, e := flow.New(flow.Config{ID: "preparation", Version: "1"}, flow.Deps{Definitions: db, Positions: db, State: state})
	if e != nil {
		t.Fatal(e)
	}
	taskValue := task.Task{Tenant: "t", ID: "a", Version: 1}
	e = db.Do(ctx, func(tx context.Context) error {
		if e := db.CreateTask(tx, taskValue); e != nil {
			return e
		}
		p, e := controller.Initialize(tx, taskValue)
		if e != nil {
			return e
		}
		if p.Complete || p.Waiting == "" || p.State != "ready" {
			t.Fatalf("waiting %+v", p)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	taskValue.Goal = "goal"
	taskValue.Criteria = "criteria"
	e = db.Do(ctx, func(tx context.Context) error {
		p, e := controller.Refresh(tx, taskValue)
		if e != nil {
			return e
		}
		if !p.Complete || p.Waiting != "" {
			t.Fatalf("complete %+v", p)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.Do(ctx, func(tx context.Context) error {
		return db.EnsureDefinition(tx, capflow.DefinitionRef{ID: "preparation", Version: "1", Digest: "changed"})
	}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("definition fence", e)
	}
	invalid, _ := flow.New(flow.Config{ID: "bad", Version: "1"}, flow.Deps{Definitions: db, Positions: db, State: testState{state, capflow.Check{Status: "unknown"}}})
	if e = db.Do(ctx, func(tx context.Context) error {
		_, e := invalid.Initialize(tx, task.Task{Tenant: "t", ID: "b"})
		return e
	}); e == nil {
		t.Fatal("invalid check accepted")
	}
	if _, e = db.GetPosition(ctx, "t", "b"); !errors.Is(e, fault.ErrNotFound) {
		t.Fatal("invalid result leaked position", e)
	}
}
