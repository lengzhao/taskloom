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

// RequestSecurity enforces the shared Origin/CSRF envelope and extracts an
// existing credential. Domain authentication and authorization still run per use case.
type RequestSecurity interface {
	Credential(w http.ResponseWriter, r *http.Request) (token string, ok bool)
}
