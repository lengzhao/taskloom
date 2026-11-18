// Package record contains narrow durable command, audit and event capabilities.
package record

import (
	"context"
)

type RequestKey struct{ Tenant, Subject, Operation, Key string }
type Receipt struct {
	RequestKey
	Digest string
	Result []byte
}
type Receipts interface {
	FindReceipt(ctx context.Context, key RequestKey) (Receipt, error)
	SaveReceipt(ctx context.Context, value Receipt) error
}
type Audit struct {
	ID, Tenant, Subject, Operation, Resource, Outcome, Reason string
	OwnerID, InstanceID, Incarnation                          string
}
type AuditWriter interface {
	AppendAudit(ctx context.Context, value Audit) error
}
type Event struct {
	ID                     string
	Sequence               int64
	Tenant, Resource, Kind string
	Payload                []byte
}

type EventWriter interface {
	AppendEvent(ctx context.Context, value Event) error
}

// EventLog is used where one consumer really both records and scans events.
type EventLog interface {
	// Internal outbox access, never an authorized user feed.
	ReadEvents(ctx context.Context, tenant string, after int64, limit int) ([]Event, error)
	EventWriter
}
