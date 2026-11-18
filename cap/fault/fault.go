// Package fault defines shared result categories without storage ownership.
package fault

import "errors"

var (
	ErrConflict    = errors.New("version or idempotency conflict")
	ErrNotFound    = errors.New("not found")
	ErrUnavailable = errors.New("storage unavailable")
	ErrInput       = errors.New("invalid input")
)
