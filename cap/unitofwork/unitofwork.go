// Package unitofwork defines database-independent transaction ownership.
package unitofwork

import (
	"context"
	"errors"
)

var (
	ErrConflict    = errors.New("version or idempotency conflict")
	ErrNotFound    = errors.New("not found")
	ErrRollback    = errors.New("transaction is rollback-only")
	ErrTransaction = errors.New("active owning transaction required")
	ErrUnavailable = errors.New("storage unavailable")
)

// Do joins an existing transaction from ctx. Any nested failure poisons the
// entire transaction. Callbacks must not start goroutines or remote effects.
type Manager interface {
	Do(context.Context, func(context.Context) error) error
}
