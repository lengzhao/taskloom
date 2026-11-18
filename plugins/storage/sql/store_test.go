package sql_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	store "github.com/lengzhao/taskloom/plugins/storage/sql"
)

func open(t *testing.T, path string) *store.Store {
	t.Helper()
	s, err := store.New(store.Config{Driver: "sqlite", DSN: path})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return s
}
func TestRollbackAcrossRepositoriesAndNestedFailure(t *testing.T) {
	s := open(t, filepath.Join(t.TempDir(), "store.db"))
	ctx := context.Background()
	err := s.Do(ctx, func(tx context.Context) error {
		if err := s.CreatePrincipal(tx, identity.Principal{Tenant: "t", ID: "u", Kind: "human"}); err != nil {
			return err
		}
		_ = s.Do(tx, func(inner context.Context) error {
			if err := s.AppendEvent(inner, record.Event{Tenant: "t", Kind: "created"}); err != nil {
				return err
			}
			return errors.New("domain failure")
		})
		return nil
	})
	if !errors.Is(err, unitofwork.ErrRollback) {
		t.Fatalf("expected rollback-only, got %v", err)
	}
	if _, err = s.Resolve(ctx, "t", "u"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("principal leaked: %v", err)
	}
	es, err := s.ReadEvents(ctx, "t", 0, 100)
	if err != nil || len(es) != 0 {
		t.Fatalf("event leaked: %v %v", es, err)
	}
	if err = s.CreatePrincipal(ctx, identity.Principal{Tenant: "t", ID: "u", Kind: "human"}); !errors.Is(err, unitofwork.ErrTransaction) {
		t.Fatalf("unowned write: %v", err)
	}
}
func TestTaskInsertConcurrencyAndDurability(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	a := open(t, path)
	b := open(t, path)
	ctx := context.Background()
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, s := range []*store.Store{a, b} {
		wg.Add(1)
		go func(s *store.Store) {
			defer wg.Done()
			results <- s.Do(ctx, func(tx context.Context) error {
				err := s.CreateTask(tx, task.Task{Tenant: "t", ID: "r", Version: 1, BasisVersion: 1})
				if err != nil {
					return err
				}
				return s.AppendEvent(tx, record.Event{Tenant: "t", Kind: "changed", Resource: "r"})
			})
		}(s)
	}
	wg.Wait()
	close(results)
	success, conflict := 0, 0
	for err := range results {
		if err == nil {
			success++
		} else if errors.Is(err, fault.ErrConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflicts=%d", success, conflict)
	}
	c := open(t, path)
	es, err := c.ReadEvents(ctx, "t", 0, 100)
	if err != nil || len(es) != 1 {
		t.Fatalf("durable event: %v %v", es, err)
	}
}
func TestEventCursorCannotSkipLateCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.db")
	a := open(t, path)
	b := open(t, path)
	ctx := context.Background()
	entered, release := make(chan struct{}), make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		errs <- a.Do(ctx, func(tx context.Context) error {
			if err := a.AppendEvent(tx, record.Event{Tenant: "t", Kind: "first"}); err != nil {
				return err
			}
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered
	go func() {
		errs <- b.Do(ctx, func(tx context.Context) error { return b.AppendEvent(tx, record.Event{Tenant: "t", Kind: "second"}) })
	}()
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	es, err := a.ReadEvents(ctx, "t", 0, 1)
	if err != nil || len(es) != 1 || es[0].Kind != "first" {
		t.Fatalf("first page %v %v", es, err)
	}
	next, err := a.ReadEvents(ctx, "t", es[0].Sequence, 10)
	if err != nil || len(next) != 1 || next[0].Kind != "second" {
		t.Fatalf("next page %v %v", next, err)
	}
}
func TestConstructionDoesNotOpenDatabase(t *testing.T) {
	s, err := store.New(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "missing", "db")})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Open(context.Background()); !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("open error: %v", err)
	}
	if err = s.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = store.New(store.Config{Driver: "unknown", DSN: "secret"}); err == nil {
		t.Fatal("unknown driver accepted")
	}
}
