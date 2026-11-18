package command_test

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	"github.com/lengzhao/taskloom/domain/security"
	access "github.com/lengzhao/taskloom/plugins/access/default"
	local "github.com/lengzhao/taskloom/plugins/identity/local"
	store "github.com/lengzhao/taskloom/plugins/storage/sql"
	"github.com/lengzhao/taskloom/runtime/command"
)

func fixture(t *testing.T) (*store.Store, *command.Runner, *security.Service, string) {
	t.Helper()
	s, err := store.New(store.Config{DSN: filepath.Join(t.TempDir(), "db")})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close(context.Background()) })
	id, err := local.New(local.Deps{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	az, err := access.New(access.Config{Actions: []string{"test.write", "test.read"}}, access.Deps{Store: s})
	if err != nil {
		t.Fatal(err)
	}
	runner, err := command.New(command.Deps{Transactions: s, Requests: s, Identity: id, Authorization: az, Audit: s, Events: s})
	if err != nil {
		t.Fatal(err)
	}
	svc := security.New(security.Deps{Commands: runner, Transactions: s, Identities: s, Grants: s, Audit: s, Events: s})
	issued, err := svc.Bootstrap(context.Background(), security.BootstrapInput{Tenant: "t", Subject: "admin", Operator: "test-operator", Reason: "initial setup", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	return s, runner, svc, issued.Secret
}
func TestConcurrentIdempotencyAndRevocation(t *testing.T) {
	s, r, svc, token := fixture(t)
	ctx := context.Background()
	grant := authorization.Grant{Tenant: "t", Subject: "admin", Action: "test.write", Scope: authorization.Scope{Kind: "task", ID: "a"}}
	if err := svc.Grant(ctx, token, "grant", grant); err != nil {
		t.Fatal(err)
	}
	req := command.Request{Token: token, Operation: "test.write", Key: "same", Scope: grant.Scope}
	var writes atomic.Int32
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := command.Run(ctx, r, req, struct{ Value int }{7}, func(tx context.Context, p identity.Principal) ([]byte, error) {
				writes.Add(1)
				_, err := s.Advance(tx, p.Tenant, "a", 0)
				return []byte("result"), err
			})
			if err == nil && string(out) != "result" {
				err = errors.New("bad replay")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if writes.Load() != 1 {
		t.Fatalf("writes=%d", writes.Load())
	}
	_, err := command.Run(ctx, r, req, struct{ Value int }{8}, func(context.Context, identity.Principal) ([]byte, error) {
		t.Fatal("conflicting payload ran")
		return nil, nil
	})
	if !errors.Is(err, unitofwork.ErrConflict) {
		t.Fatalf("conflict: %v", err)
	}
	if err = svc.Ungrant(ctx, token, "revoke-grant", grant); err != nil {
		t.Fatal(err)
	}
	_, err = command.Run(ctx, r, req, struct{ Value int }{7}, func(context.Context, identity.Principal) ([]byte, error) {
		t.Fatal("revoked replay ran")
		return nil, nil
	})
	if !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("replay leaked: %v", err)
	}
}
func TestCommandRollbackAndTenantIsolation(t *testing.T) {
	s, r, svc, token := fixture(t)
	ctx := context.Background()
	g := authorization.Grant{Tenant: "t", Subject: "admin", Action: "test.write", Scope: authorization.Scope{Kind: "task", ID: "a"}}
	if err := svc.Grant(ctx, token, "grant", g); err != nil {
		t.Fatal(err)
	}
	req := command.Request{Token: token, Operation: "test.write", Key: "failure", Scope: g.Scope}
	failure := errors.New("failed business validation")
	_, err := command.Run(ctx, r, req, 1, func(tx context.Context, p identity.Principal) ([]byte, error) {
		if _, err := s.Advance(tx, p.Tenant, "a", 0); err != nil {
			return nil, err
		}
		return nil, failure
	})
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	// A retry with the same key and payload may commit because nothing survived.
	_, err = command.Run(ctx, r, req, 1, func(tx context.Context, p identity.Principal) ([]byte, error) {
		_, err := s.Advance(tx, p.Tenant, "a", 0)
		return nil, err
	})
	if err != nil {
		t.Fatal(err)
	}
	events, err := s.ReadEvents(ctx, "other", 0, 100)
	if err != nil || len(events) != 0 {
		t.Fatalf("cross tenant event: %v %v", events, err)
	}
	g.Tenant = "other"
	if err = svc.Grant(ctx, token, "cross-tenant", g); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatalf("cross tenant grant: %v", err)
	}
	req.Operation = "unknown"
	if _, err = command.Run(ctx, r, req, 1, func(context.Context, identity.Principal) ([]byte, error) {
		t.Fatal("unknown operation ran")
		return nil, nil
	}); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal(err)
	}
}
func TestBootstrapOnceAndCredentialRevocation(t *testing.T) {
	_, r, svc, token := fixture(t)
	ctx := context.Background()
	if _, err := svc.Bootstrap(ctx, security.BootstrapInput{Tenant: "other", Subject: "admin", Operator: "op", Reason: "again", ExpiresAt: time.Now().Add(time.Hour)}); !errors.Is(err, unitofwork.ErrConflict) {
		t.Fatalf("second bootstrap: %v", err)
	}
	issued, err := svc.Issue(ctx, token, "issue", security.IssueInput{Tenant: "t", Subject: "admin", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Revoke(ctx, token, "revoke", security.RevokeInput{Tenant: "t", CredentialID: issued.ID}); err != nil {
		t.Fatal(err)
	}
	req := command.Request{Token: issued.Secret, Key: "denied", Operation: "identity.issue", Scope: authorization.Scope{Kind: "tenant", ID: "t"}}
	if _, err = command.Run(ctx, r, req, 1, func(context.Context, identity.Principal) ([]byte, error) {
		t.Fatal("revoked credential ran")
		return nil, nil
	}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatalf("revoked: %v", err)
	}
	// Secret must never be persisted in command receipts or events.
	if issued.Secret == "" {
		t.Fatal("empty credential")
	}
}

var _ record.Events = (*store.Store)(nil)

func TestRecoveryRevokesOldCredentials(t *testing.T) {
	_, r, svc, token := fixture(t)
	ctx := context.Background()
	recovered, err := svc.Recover(ctx, security.BootstrapInput{Tenant: "t", Subject: "admin", Operator: "local-operator", Reason: "lost credential", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	req := command.Request{Token: token, Operation: "identity.issue", Key: "old", Scope: authorization.Scope{Kind: "tenant", ID: "t"}}
	if _, err = command.Run(ctx, r, req, 1, func(context.Context, identity.Principal) ([]byte, error) {
		t.Fatal("old token usable")
		return nil, nil
	}); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if _, err = svc.Issue(ctx, recovered.Secret, "new", security.IssueInput{Tenant: "t", Subject: "admin", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
}
func TestIssuanceReplayDoesNotRediscloseSecret(t *testing.T) {
	s, _, svc, token := fixture(t)
	ctx := context.Background()
	in := security.IssueInput{Tenant: "t", Subject: "admin", ExpiresAt: time.Now().Add(time.Hour)}
	first, err := svc.Issue(ctx, token, "once", in)
	if err != nil {
		t.Fatal(err)
	}
	again, err := svc.Issue(ctx, token, "once", in)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != again.ID || again.Secret != "" {
		t.Fatal("replay redisclosed/reissued secret")
	}
	receipt, err := s.FindReceipt(ctx, record.RequestKey{Tenant: "t", Subject: "admin", Operation: "identity.issue", Key: "once"})
	if err != nil {
		t.Fatal(err)
	}
	if string(receipt.Result) == first.Secret {
		t.Fatal("plaintext persisted")
	}
}

func TestAgentBindingsAreCredentialScopedAndDoNotInheritOwner(t *testing.T) {
	s, r, svc, token := fixture(t)
	ctx := context.Background()
	p := identity.Principal{Tenant: "t", ID: "agent", Kind: "agent", OwnerID: "admin"}
	if err := svc.Create(ctx, token, "agent", p); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Issue(ctx, token, "agent-1", security.IssueInput{Tenant: "t", Subject: "agent", InstanceID: "instance", Incarnation: "boot-1", Purpose: "business", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Issue(ctx, token, "agent-2", security.IssueInput{Tenant: "t", Subject: "agent", InstanceID: "instance", Incarnation: "boot-2", Purpose: "runtime", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	auth, _ := local.New(local.Deps{Store: s})
	a, err := auth.Authenticate(ctx, first.Secret)
	if err != nil {
		t.Fatal(err)
	}
	b, err := auth.Authenticate(ctx, second.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.Incarnation != "boot-1" || b.Incarnation != "boot-2" {
		t.Fatal("runtime binding drift")
	}
	req := command.Request{Token: first.Secret, Operation: "identity.issue", Key: "forbidden", Scope: authorization.Scope{Kind: "tenant", ID: "t"}}
	if _, err = command.Run(ctx, r, req, 1, func(context.Context, identity.Principal) ([]byte, error) {
		t.Fatal("inherited owner permission")
		return nil, nil
	}); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal(err)
	}
	// Even an explicit business grant cannot let a runtime proof credential write.
	if err = svc.Grant(ctx, token, "agent-grant", authorization.Grant{Tenant: "t", Subject: "agent", Action: "test.write", Scope: authorization.Scope{Kind: "task", ID: "a"}}); err != nil {
		t.Fatal(err)
	}
	req.Token = second.Secret
	req.Operation = "test.write"
	req.Scope = authorization.Scope{Kind: "task", ID: "a"}
	if _, err = command.Run(ctx, r, req, 1, func(context.Context, identity.Principal) ([]byte, error) {
		t.Fatal("runtime credential used for business")
		return nil, nil
	}); !errors.Is(err, authorization.ErrForbidden) {
		t.Fatal(err)
	}
}

type failingEvents struct{ record.Events }

func (f failingEvents) AppendEvent(ctx context.Context, e record.Event) error {
	if err := f.Events.AppendEvent(ctx, e); err != nil {
		return err
	}
	return errors.New("injected outbox failure")
}
func TestOutboxFailureRollsBackFactsAndReceipt(t *testing.T) {
	s, _, svc, token := fixture(t)
	ctx := context.Background()
	scope := authorization.Scope{Kind: "task", ID: "a"}
	if err := svc.Grant(ctx, token, "grant", authorization.Grant{Tenant: "t", Subject: "admin", Action: "test.write", Scope: scope}); err != nil {
		t.Fatal(err)
	}
	auth, _ := local.New(local.Deps{Store: s})
	az, _ := access.New(access.Config{Actions: []string{"test.write"}}, access.Deps{Store: s})
	runner, _ := command.New(command.Deps{Transactions: s, Requests: s, Identity: auth, Authorization: az, Audit: s, Events: failingEvents{s}})
	before, err := s.ReadEvents(ctx, "t", 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	req := command.Request{Token: token, Operation: "test.write", Key: "outbox-failure", Scope: scope}
	_, err = command.Run(ctx, runner, req, 1, func(tx context.Context, p identity.Principal) ([]byte, error) {
		_, err := s.Advance(tx, p.Tenant, "a", 0)
		return []byte("never committed"), err
	})
	if err == nil {
		t.Fatal("outbox failure accepted")
	}
	if _, err = s.FindReceipt(ctx, record.RequestKey{Tenant: "t", Subject: "admin", Operation: "test.write", Key: req.Key}); !errors.Is(err, unitofwork.ErrNotFound) {
		t.Fatalf("receipt leaked: %v", err)
	}
	after, err := s.ReadEvents(ctx, "t", 0, 100)
	if err != nil || len(after) != len(before) {
		t.Fatal("outbox write leaked")
	}
	if err = s.Do(ctx, func(tx context.Context) error { _, err := s.Advance(tx, "t", "a", 0); return err }); err != nil {
		t.Fatalf("revision leaked: %v", err)
	}
}
