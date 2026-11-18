// Package record contains narrow durable command, audit and event capabilities.
package record

import (
	"context"

	"github.com/lengzhao/taskloom/cap/identity"
)

type RequestKey struct{ Tenant, Subject, Operation, Key string }
type Receipt struct {
	RequestKey
	Digest string
	Result []byte
}
type Requests interface {
	FindReceipt(context.Context, RequestKey) (Receipt, error)
	SaveReceipt(context.Context, Receipt) error
}
type Audit struct {
	ID, Tenant, Subject, Operation, Resource, Outcome, Reason string
	OwnerID, InstanceID, Incarnation                          string
}
type Audits interface {
	AppendAudit(context.Context, Audit) error
}
type Event struct {
	ID                     string
	Sequence               int64
	Tenant, Resource, Kind string
	Payload                []byte
}

// ReadEvents is an internal outbox-reader capability, not an authorized feed API.
type Events interface {
	AppendEvent(context.Context, Event) error
	ReadEvents(context.Context, string, int64, int) ([]Event, error)
}
type Revisions interface {
	Advance(context.Context, string, string, int64) (int64, error)
}

// ActorAudit avoids logging a request payload or credential.
func ActorAudit(p identity.Principal, op, resource, outcome string) Audit {
	return Audit{Tenant: p.Tenant, Subject: p.ID, Operation: op, Resource: resource, Outcome: outcome, OwnerID: p.OwnerID, InstanceID: p.InstanceID, Incarnation: p.Incarnation}
}
