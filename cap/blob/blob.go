// Package blob stores file bytes under opaque keys. It shares no transaction
// with SQL: a caller writes the bytes first, registers them in SQL, and
// deletes them again when that registration fails.
package blob

import (
	"context"
	"io"
)

// Store keeps immutable byte objects. Callers put each object under a fresh
// random key and never reuse it, so no store relies on detecting an existing
// key (the directory store still refuses one with fault.ErrConflict). Get of a missing key is fault.ErrNotFound. Delete of a
// missing key succeeds, so a compensating delete can be retried.
type Store interface {
	Put(ctx context.Context, key string, r io.Reader) error
	Get(ctx context.Context, key string) (io.ReadCloser, error)
	Delete(ctx context.Context, key string) error
}
