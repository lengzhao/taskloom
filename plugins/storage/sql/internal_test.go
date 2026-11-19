package sql

import (
	"bytes"
	"context"
	"errors"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	local "github.com/lengzhao/taskloom/plugins/identity/local"
	"github.com/lengzhao/taskloom/plugins/security"
)

func TestMigrationRejectsDriftAndRecoversAfterRepair(t *testing.T) {
	ctx := context.Background()
	c := Config{DSN: filepath.Join(t.TempDir(), "db")}
	s, _ := New(c)
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.db.Model(&migrationRecord{}).Where("id = ?", "identity/001").Update("checksum", "wrong").Error; err != nil {
		t.Fatal(err)
	}
	probe, _ := New(c)
	if err := probe.Open(ctx); !errors.Is(err, fault.ErrUnavailable) {
		t.Fatalf("schema drift accepted: %v", err)
	}
	// Removing the incomplete ledger record allows idempotent migration to resume.
	if err := s.db.Delete(&migrationRecord{}, "id = ?", "identity/001").Error; err != nil {
		t.Fatal(err)
	}
	if err := probe.Open(ctx); err != nil {
		t.Fatal(err)
	}
	_ = probe.Close(ctx)
	_ = s.Close(ctx)
}
func TestForeignKeysAndCapturedWriteFailurePoisonTransaction(t *testing.T) {
	ctx := context.Background()
	s, _ := New(Config{DSN: filepath.Join(t.TempDir(), "db")})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	err := s.Do(ctx, func(tx context.Context) error {
		_ = s.SaveCredential(tx, identity.Credential{ID: "invalid", Digest: "digest", Principal: identity.Principal{Tenant: "missing", ID: "missing"}})
		return s.CreatePrincipal(tx, identity.Principal{Tenant: "t", ID: "u", Kind: "human"})
	})
	if !errors.Is(err, unitofwork.ErrRollback) {
		t.Fatalf("captured FK failure committed: %v", err)
	}
	if _, err = s.Resolve(ctx, "t", "u"); !errors.Is(err, fault.ErrNotFound) {
		t.Fatalf("write leaked: %v", err)
	}
}
func TestCancelledAndEscapedTransactions(t *testing.T) {
	ctx := context.Background()
	s, _ := New(Config{DSN: filepath.Join(t.TempDir(), "db")})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	var escaped context.Context
	if err := s.Do(ctx, func(tx context.Context) error { escaped = tx; return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Resolve(escaped, "t", "u"); !errors.Is(err, unitofwork.ErrTransaction) {
		t.Fatalf("escaped transaction: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Do(cancelled, func(context.Context) error { t.Fatal("cancelled callback ran"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestCaughtNestedPanicPoisonsTransaction(t *testing.T) {
	ctx := context.Background()
	s, _ := New(Config{DSN: filepath.Join(t.TempDir(), "db")})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	defer s.Close(ctx)
	err := s.Do(ctx, func(tx context.Context) error {
		func() {
			defer func() { _ = recover() }()
			_ = s.Do(tx, func(context.Context) error { panic("nested failure") })
		}()
		return s.CreatePrincipal(tx, identity.Principal{Tenant: "t", ID: "u", Kind: "human"})
	})
	if !errors.Is(err, unitofwork.ErrRollback) {
		t.Fatalf("caught panic committed: %v", err)
	}
}
func TestEncodedSQLiteSafetyOverridesRejected(t *testing.T) {
	s, _ := New(Config{DSN: filepath.Join(t.TempDir(), "db") + "?%5Ffk=off"})
	if err := s.Open(context.Background()); err == nil {
		_ = s.Close(context.Background())
		t.Fatal("encoded override accepted")
	}
}

func TestCloseHonorsDeadlineWhileTransactionIsActive(t *testing.T) {
	ctx := context.Background()
	s, _ := New(Config{DSN: filepath.Join(t.TempDir(), "db")})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		_ = s.Do(ctx, func(context.Context) error { close(entered); <-release; return nil })
	}()
	<-entered
	closeCtx, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- s.Close(closeCtx) }()
	select {
	case err := <-result:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("deadline: %v", err)
		}
	case <-time.After(200 * time.Millisecond):
		t.Error("Close ignored deadline")
	}
	close(release)
	<-done
	_ = s.Close(ctx)
}

func TestCredentialPersistenceContainsOnlyDigestAndExpiryIsChecked(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "db")
	s, _ := New(Config{DSN: path})
	if err := s.Open(ctx); err != nil {
		t.Fatal(err)
	}
	svc := security.New(security.Deps{Passwords: s, Transactions: s, Principals: s, Credentials: s, Bootstrap: s, Members: s, Audit: s, Events: s})
	if err := svc.Bootstrap(ctx, security.AdminInput{Tenant: "t", Subject: "admin", Operator: "op", Reason: "test", Password: "AdminPassword123!"}); err != nil {
		t.Fatal(err)
	}
	issued, err := svc.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: "admin", Password: "AdminPassword123!"})
	if err != nil {
		t.Fatal(err)
	}
	var row credentialRow
	if err = s.db.First(&row, "id = ?", issued.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Digest == issued.Secret || len(row.Digest) != 64 {
		t.Fatal("credential digest missing")
	}
	if err = s.db.Model(&credentialRow{}).Where("id = ?", issued.ID).Update("expires_at", time.Now().Add(-time.Second)).Error; err != nil {
		t.Fatal(err)
	}
	auth, _ := local.New(local.Deps{Store: s})
	if _, err = auth.Authenticate(ctx, issued.Secret); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("expired credential: %v", err)
	}
	if err = s.Close(ctx); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(issued.Secret)) {
		t.Fatal("plaintext credential persisted")
	}
}

func TestFreshSchemaHasRelationshipsAndNoGrantTable(t *testing.T) {
	s, _ := New(Config{DSN: filepath.Join(t.TempDir(), "db")})
	ctx := context.Background()
	if e := s.Open(ctx); e != nil {
		t.Fatal(e)
	}
	defer s.Close(ctx)
	if s.db.Migrator().HasTable("grant_rows") {
		t.Fatal("legacy grant schema remains")
	}
	if !s.db.Migrator().HasTable(&memberRow{}) || !s.db.Migrator().HasTable(&participantRow{}) {
		t.Fatal("missing relationship schema")
	}
}
