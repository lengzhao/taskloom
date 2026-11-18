// Package command owns the transaction/receipt/audit lifecycle of named commands.
// It does not dispatch arbitrary actions or discover business services.
package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/lengzhao/taskloom/cap/authorization"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/unitofwork"
)

// Six separate dependencies are intentional: transaction, request persistence,
// authentication, authorization, audit and events must not become one service.
type Deps struct {
	Transactions  unitofwork.Manager       `json:"transactions"`
	Requests      record.Receipts          `json:"requests"`
	Identity      identity.Authenticator   `json:"identity"`
	Authorization authorization.Authorizer `json:"authorization"`
	Audit         record.AuditWriter       `json:"audit"`
	Events        record.EventWriter       `json:"events"`
}
type Runner struct{ d Deps }

func New(d Deps) (*Runner, error) {
	if d.Transactions == nil || d.Requests == nil || d.Identity == nil || d.Authorization == nil || d.Audit == nil || d.Events == nil {
		return nil, errors.New("command dependencies required")
	}
	return &Runner{d}, nil
}

// Run is a code-level wrapper around a typed domain input and explicit callback.
// The callback must validate resource tenancy/current business versions. It must
// not perform remote IO or return secrets intended to be shown only once.
func Run[T any](ctx context.Context, r capcommand.Handler, req capcommand.Request, input T, apply func(context.Context, identity.Principal) ([]byte, error)) ([]byte, error) {
	if req.Key == "" || len(req.Key) > 128 || req.Operation == "" || len(req.Operation) > 128 {
		return nil, fault.ErrConflict
	}
	canonical, err := json.Marshal(struct {
		Operation string
		Resource  string
		Input     T
	}{req.Operation, req.Resource, input})
	if err != nil {
		return nil, errors.New("command input cannot be encoded")
	}
	h := sha256.Sum256(canonical)
	digest := hex.EncodeToString(h[:])
	return r.Do(ctx, req, digest, apply)
}

func (r *Runner) Do(ctx context.Context, req capcommand.Request, digest string, apply func(context.Context, identity.Principal) ([]byte, error)) ([]byte, error) {
	if req.Key == "" || len(req.Key) > 128 || req.Operation == "" || len(req.Operation) > 128 {
		return nil, fault.ErrConflict
	}
	var result []byte
	var actor identity.Principal
	err := r.d.Transactions.Do(ctx, func(tx context.Context) error {
		var err error
		actor, err = r.d.Identity.Authenticate(tx, req.Token)
		if err != nil {
			return err
		}
		if err = r.d.Authorization.Check(tx, actor, req.Operation, req.Resource); err != nil {
			return err
		}
		if req.ReadOperation != "" {
			if err = r.d.Authorization.Check(tx, actor, req.ReadOperation, req.Resource); err != nil {
				return err
			}
		}
		key := record.RequestKey{Tenant: actor.Tenant, Subject: actor.ID, Operation: req.Operation, Key: req.Key}
		old, err := r.d.Requests.FindReceipt(tx, key)
		if err == nil {
			if old.Digest != digest {
				return fault.ErrConflict
			}
			result = append([]byte(nil), old.Result...)
			return nil
		}
		if !errors.Is(err, fault.ErrNotFound) {
			return err
		}
		result, err = apply(tx, actor)
		if err != nil {
			return err
		}
		if err = r.d.Audit.AppendAudit(tx, actorAudit(actor, req.Operation, req.Resource, "committed")); err != nil {
			return err
		}
		if err = r.d.Events.AppendEvent(tx, record.Event{Tenant: actor.Tenant, Resource: req.Resource, Kind: req.Operation}); err != nil {
			return err
		}
		return r.d.Requests.SaveReceipt(tx, record.Receipt{RequestKey: key, Digest: digest, Result: result})
	})
	if err != nil {
		// Denial auditing is explicit, separate from the failed write transaction,
		// and contains no raw error, credential, request body or replay result.
		if actor.ID != "" {
			auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
			defer cancel()
			auditErr := r.d.Transactions.Do(auditCtx, func(tx context.Context) error {
				return r.d.Audit.AppendAudit(tx, actorAudit(actor, req.Operation, req.Resource, "rejected"))
			})
			if auditErr != nil {
				return nil, errors.Join(err, auditErr)
			}
		}
		return nil, err
	}
	return result, nil
}

func actorAudit(p identity.Principal, op, resource, outcome string) record.Audit {
	return record.Audit{Tenant: p.Tenant, Subject: p.ID, Operation: op, Resource: resource, Outcome: outcome, OwnerID: p.OwnerID, InstanceID: p.InstanceID, Incarnation: p.Incarnation}
}
