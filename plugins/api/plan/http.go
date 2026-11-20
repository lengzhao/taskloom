// Package plan adapts placing a subtask to HTTP; rules live in the command.
package plan

import (
	"net/http"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	capplan "github.com/lengzhao/taskloom/cap/plan"
	"github.com/lengzhao/taskloom/runtime/httpx"
)

type Deps struct {
	Plan capplan.Commands `json:"plan"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("api/plan", func(_ struct{}, d Deps) (*Module, error) {
		if d.Plan == nil {
			return nil, fault.ErrInput
		}
		return &Module{d}, nil
	})
}

func (m *Module) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "POST", Path: "/tasks/{id}/parent", Operation: "task.plan", Handler: http.HandlerFunc(m.serve)}}
}

// serve places the task {id} under ParentID.
func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	var in capplan.Placement
	if !httpx.Decode(w, r, &in) {
		return
	}
	in.ChildID = r.PathValue("id")
	if err := m.d.Plan.Place(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"), in); err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, map[string]bool{"ok": true})
}
