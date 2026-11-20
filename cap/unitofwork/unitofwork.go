// Package unitofwork defines database-independent transaction ownership.
package unitofwork

import "context"

// Do joins an existing transaction from ctx. Any nested failure poisons the
// entire transaction. Callbacks must not start goroutines or remote effects.
type Manager interface {
	Do(ctx context.Context, apply func(tx context.Context) error) error
}
