package sql

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lengzhao/taskloom/cap/record"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := New(Config{DSN: filepath.Join(t.TempDir(), "db")})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	return s
}

// A view reads a snapshot but refuses every write, and cannot start a command.
func TestViewRefusesWrites(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if s.rdb == s.db {
		t.Fatal("file-backed SQLite should have a separate read pool")
	}
	err := s.View(ctx, func(tx context.Context) error {
		return s.AppendEvent(tx, record.Event{Tenant: "t", Resource: "x", Kind: record.EventTaskChanged})
	})
	if !errors.Is(err, ErrTransaction) {
		t.Fatalf("write inside a view: %v", err)
	}
	err = s.View(ctx, func(tx context.Context) error {
		return s.Do(tx, func(context.Context) error { return nil })
	})
	if !errors.Is(err, ErrTransaction) {
		t.Fatalf("command inside a view: %v", err)
	}
	// A view inside a command joins it and sees its uncommitted writes.
	err = s.Do(ctx, func(tx context.Context) error {
		if err := s.AppendEvent(tx, record.Event{Tenant: "t", Resource: "x", Kind: record.EventTaskChanged}); err != nil {
			return err
		}
		return s.View(tx, func(tx context.Context) error {
			es, err := s.ReadEvents(tx, "t", 0, 10)
			if err == nil && len(es) != 1 {
				t.Errorf("joined view saw %d events", len(es))
			}
			return err
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	// Committed writes are visible to a later view.
	if err = s.View(ctx, func(tx context.Context) error {
		es, err := s.ReadEvents(tx, "t", 0, 10)
		if err == nil && len(es) != 1 {
			t.Errorf("view after commit saw %d events", len(es))
		}
		return err
	}); err != nil {
		t.Fatal(err)
	}
}

// Pruning removes expired events and receipts, raises the event floor to the
// last pruned sequence, and keeps everything younger than the retention.
func TestPruneRaisesEventFloor(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	appendEvent := func() {
		t.Helper()
		if err := s.Do(ctx, func(tx context.Context) error {
			return s.AppendEvent(tx, record.Event{Tenant: "t", Resource: "x", Kind: record.EventTaskChanged})
		}); err != nil {
			t.Fatal(err)
		}
	}
	appendEvent()
	appendEvent()
	if err := s.Do(ctx, func(tx context.Context) error {
		return s.SaveReceipt(tx, record.Receipt{RequestKey: record.RequestKey{Tenant: "t", Subject: "s", Operation: "op", Key: "k"}, Digest: "d"})
	}); err != nil {
		t.Fatal(err)
	}
	if f, err := s.EventFloor(ctx); err != nil || f != 0 {
		t.Fatal("fresh floor", f, err)
	}
	// Nothing is old enough yet.
	if err := s.prune(ctx, time.Now(), time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.ReadEvents(ctx, "t", 0, 10); len(es) != 2 {
		t.Fatal("young events pruned", es)
	}
	es, _ := s.ReadEvents(ctx, "t", 0, 10)
	last := es[1].Sequence
	// Two hours later everything has expired.
	if err := s.prune(ctx, time.Now().Add(2*time.Hour), time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.ReadEvents(ctx, "t", 0, 10); len(es) != 0 {
		t.Fatal("expired events kept", es)
	}
	if f, err := s.EventFloor(ctx); err != nil || f != last {
		t.Fatal("floor", f, last, err)
	}
	if _, err := s.FindReceipt(ctx, record.RequestKey{Tenant: "t", Subject: "s", Operation: "op", Key: "k"}); err == nil {
		t.Fatal("expired receipt kept")
	}
	// The floor never moves back.
	appendEvent()
	if err := s.prune(ctx, time.Now(), time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	if f, _ := s.EventFloor(ctx); f != last {
		t.Fatal("floor moved", f)
	}
}

func TestRetentionHasAFloor(t *testing.T) {
	if _, err := New(Config{DSN: "x", EventRetention: "1h"}); err == nil {
		t.Fatal("retention under 24h accepted")
	}
}

// A database created before command/002 and workflow/002 upgrades in place:
// receipts gain created_at (backfilled, so none expire at once) and
// assignments gain an empty assigner.
func TestUpgradeAddsRetentionAndAssignerColumns(t *testing.T) {
	ctx := context.Background()
	c := Config{DSN: filepath.Join(t.TempDir(), "db")}
	s, _ := New(c)
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		"DROP INDEX idx_receipt_created",
		"DROP INDEX idx_audit_tenant_created",
		"ALTER TABLE receipt_rows DROP COLUMN created_at",
		"ALTER TABLE assignment_rows DROP COLUMN assigner",
		"DELETE FROM migration_records WHERE id IN ('command/002', 'workflow/002')",
		"INSERT INTO receipt_rows (row_key, digest) VALUES ('old', 'd')",
		"INSERT INTO assignment_rows (row_key, subject) VALUES ('a', 'worker')",
	} {
		if err := s.db.Exec(q).Error; err != nil {
			t.Fatal(q, err)
		}
	}
	_ = s.Close(ctx)
	s, _ = New(c)
	if err := s.Open(ctx); err != nil {
		t.Fatal("upgrade", err)
	}
	defer s.Close(ctx)
	var r receiptRecord
	if err := s.db.First(&r, "row_key = ?", "old").Error; err != nil || r.CreatedAt.IsZero() {
		t.Fatal("receipt not backfilled", r, err)
	}
	if !s.db.Migrator().HasIndex(&auditRow{}, "idx_audit_tenant_created") {
		t.Fatal("audit time index missing after upgrade")
	}
	var a assignmentRecord
	if err := s.db.First(&a, "row_key = ?", "a").Error; err != nil || a.Subject != "worker" || a.Assigner != "" {
		t.Fatal("assignment after upgrade", a, err)
	}
}

// A backlog larger than one batch is removed across several transactions.
func TestPruneDrainsBacklogInBatches(t *testing.T) {
	ctx := context.Background()
	s := openTemp(t)
	if err := s.Do(ctx, func(tx context.Context) error {
		for i := 0; i < pruneBatch+5; i++ {
			if err := s.AppendEvent(tx, record.Event{Tenant: "t", Resource: "x", Kind: record.EventTaskChanged}); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.prune(ctx, time.Now().Add(2*time.Hour), time.Hour, time.Hour); err != nil {
		t.Fatal(err)
	}
	if es, _ := s.ReadEvents(ctx, "t", 0, 10); len(es) != 0 {
		t.Fatal("backlog left", len(es))
	}
	if f, _ := s.EventFloor(ctx); f == 0 {
		t.Fatal("floor not raised")
	}
}
