// Package lifecycle defines process ownership contracts, without business state.
package lifecycle

import "context"

// Resource.Open must clean its own partial failure. Close releases a successful Open.
type Resource interface {
	Open(context.Context) error
	Close(context.Context) error
}

// Component.Run blocks until stopped or failed. Both methods must honor cancellation.
// Stop may race with Run starting, and must be idempotent.
type Component interface {
	Run(context.Context) error
	Stop(context.Context) error
}

type App interface {
	Run(context.Context) error
	Stop(context.Context) error
}
