// Package record contains narrow durable command, audit and event capabilities.
package record

import (
	"context"
	"time"
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
	OwnerID, CredentialID                                     string
	At                                                        time.Time
}
type AuditWriter interface {
	AppendAudit(ctx context.Context, value Audit) error
}

// AuditReader lists a tenant's audit trail, newest first.
type AuditReader interface {
	ListAudit(ctx context.Context, tenant string, limit int) ([]Audit, error)
}

// Event says that a resource changed; readers re-read current facts.
type Event struct {
	Sequence               int64
	Tenant, Resource, Kind string
}

// Each use case appends exactly one event naming what changed; the command
// runtime writes none of its own.
const (
	// EventTaskChanged names a task whose facts changed; Resource is the task ID.
	EventTaskChanged = "task.changed"
	// EventAccessChanged means memberships, participants or projects changed.
	EventAccessChanged = "access.changed"
)

type EventWriter interface {
	AppendEvent(ctx context.Context, value Event) error
}

// EventLog is used where one consumer really both records and scans events.
type EventLog interface {
	// Internal outbox access, never an authorized user feed.
	ReadEvents(ctx context.Context, tenant string, after int64, limit int) ([]Event, error)
	// EventFloor is the highest pruned sequence: a reader positioned below it
	// may have missed events and must resync.
	EventFloor(ctx context.Context) (int64, error)
	EventWriter
}
