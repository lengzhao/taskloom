// Package health contributes process liveness only; it does not claim storage readiness.
package health

import (
	"net/http"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/httpapi"
)

type Module struct{}

func init()                 { pluginkit.Register("api/health", New) }
func New() (*Module, error) { return &Module{}, nil }
func (*Module) Routes() []httpapi.Route {
	return []httpapi.Route{{
		Method: http.MethodGet, Path: "/healthz", Operation: "process.health",
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Cache-Control", "no-store")
			_, _ = w.Write([]byte("{\"status\":\"ok\",\"scope\":\"process\"}\n"))
		}),
	}}
}
