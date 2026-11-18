// Package httpapi defines HTTP route contributions independent of business plugins.
package httpapi

import (
	"context"
	"net/http"
)

type Route struct {
	Method    string
	Path      string // Relative to the transport's API prefix; starts with '/'.
	Operation string
	Handler   http.Handler
}

type Module interface{ Routes() []Route }

// ModuleLifecycle is optional. The HTTP owner opens and closes it exactly once;
// it must not also be registered as an application Resource.
type ModuleLifecycle interface {
	Open(ctx context.Context) error
	Close(ctx context.Context) error
}
