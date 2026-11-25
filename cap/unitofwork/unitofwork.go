// Package unitofwork defines database-independent transaction ownership.
package unitofwork

import "context"

// Do joins an existing transaction from ctx. Any nested failure poisons the
// entire transaction. Callbacks must not start goroutines or remote effects.
type Manager interface {
	Do(ctx context.Context, apply func(tx context.Context) error) error
}

// Viewer runs reads on one consistent snapshot without taking the write lock,
// so reads do not queue behind (or block) commands. Inside an existing
// transaction it joins that transaction instead. Any write attempted in a
// view fails and poisons it; a reader that may write uses Manager.
type Viewer interface {
	View(ctx context.Context, read func(tx context.Context) error) error
}
