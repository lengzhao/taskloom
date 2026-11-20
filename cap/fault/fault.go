// Package fault defines shared result categories without storage ownership.
package fault

import "errors"

var (
	ErrConflict    = errors.New("version or idempotency conflict")
	ErrNotFound    = errors.New("not found")
	ErrUnavailable = errors.New("storage unavailable")
	ErrInput       = errors.New("invalid input")
	// ErrTooLarge rejects content over a configured size limit.
	ErrTooLarge = errors.New("content too large")
)
