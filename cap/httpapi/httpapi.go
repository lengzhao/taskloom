// Package httpapi defines HTTP route contributions independent of business plugins.
package httpapi

import (
	"net/http"
)

type Route struct {
	Method    string
	Path      string // Relative to the transport's API prefix; starts with '/'.
	Operation string
	Handler   http.Handler
}

type Module interface{ Routes() []Route }
