// Package ownership adapts a task handover to HTTP; rules live in the command.
package ownership

import (
	"net/http"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	api "github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/runtime/httpx"
)

type Deps struct {
	Ownership api.Ownership `json:"ownership"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("api/ownership", func(_ struct{}, d Deps) (*Module, error) {
		if d.Ownership == nil {
			return nil, fault.ErrInput
		}
		return &Module{d}, nil
	})
}

func (m *Module) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "POST", Path: "/tasks/{id}/owner", Operation: "task.owner", Handler: http.HandlerFunc(m.serve)}}
}

// serve hands the task {id} over to Owner, pinning SeenOwner.
func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	var in api.Handover
	if !httpx.Decode(w, r, &in) {
		return
	}
	in.TaskID = r.PathValue("id")
	ref, err := m.d.Ownership.Handover(r.Context(), httpx.Credential(r), r.Header.Get("Idempotency-Key"), in)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, ref)
}
