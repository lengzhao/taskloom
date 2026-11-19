package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/task"
)

func TestImmutableCommitsAndTenantBoundary(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "db"))
	ctx := context.Background()
	err := s.Do(ctx, func(tx context.Context) error {
		if err := s.CreateTask(tx, task.Task{Tenant: "t", ID: "a", BasisVersion: 1}); err != nil {
			return err
		}
		return s.Append(tx, commit.Commit{Tenant: "t", TaskID: "a", ID: "c1", Author: "u", Kind: "comment", Body: "first"})
	})
	if err != nil {
		t.Fatal(err)
	}
	err = s.Do(ctx, func(tx context.Context) error {
		return s.Append(tx, commit.Commit{Tenant: "t", TaskID: "a", ID: "c1", Body: "overwrite"})
	})
	if !errors.Is(err, fault.ErrConflict) {
		t.Fatalf("overwrite: %v", err)
	}
	err = s.Do(ctx, func(tx context.Context) error {
		return s.Append(tx, commit.Commit{Tenant: "t", TaskID: "a", ID: "c2", Author: "u", Kind: "comment", Body: "correction", RefID: "c1"})
	})
	if err != nil {
		t.Fatal(err)
	}
	cs, err := s.List(ctx, "t", "a", 0, 10)
	if err != nil || len(cs) != 2 || cs[0].Body != "first" || cs[1].RefID != "c1" {
		t.Fatalf("commits %v %v", cs, err)
	}
	if _, err = s.Get(ctx, "other", "a", "c1"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatal(err)
	}
}

func TestBasisChangePreservesIdentityAndOwner(t *testing.T) {
	ctx := context.Background()
	s := open(t, filepath.Join(t.TempDir(), "db"))
	if e := s.Do(ctx, func(tx context.Context) error {
		return s.CreateTask(tx, task.Task{Tenant: "t", ID: "a", Owner: "owner", BasisVersion: 1})
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.Do(ctx, func(tx context.Context) error {
		updated, e := s.UpdateBasis(tx, task.BasisChange{Tenant: "t", TaskID: "a", Title: "new", Goal: "goal", Criteria: "criteria"})
		if e != nil {
			return e
		}
		if updated.Owner != "owner" || updated.ID != "a" || updated.Tenant != "t" || updated.BasisVersion != 2 {
			t.Fatalf("basis update %+v", updated)
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
}
