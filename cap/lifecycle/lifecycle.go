// Package lifecycle defines process ownership contracts, without business state.
package lifecycle

import "context"

// Resource.Open must clean its own partial failure. Close releases a successful Open.
type Resource interface {
	Open(ctx context.Context) error
	Close(ctx context.Context) error
}

// Component.Run blocks until stopped or failed. Both methods must honor cancellation.
// Stop may race with Run starting, and must be idempotent.
type Component interface {
	Run(ctx context.Context) error
	Stop(ctx context.Context) error
}

type App interface {
	Run(ctx context.Context) error
	Stop(ctx context.Context) error
}
