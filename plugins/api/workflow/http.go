package workflow

import (
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	work "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/runtime/httpx"
	"net/http"
	"strings"
)

type Deps struct {
	Executions work.ExecutionCommands  `json:"executions"`
	Results    work.ResultCommands     `json:"results"`
	Controls   work.ControlCommands    `json:"controls"`
	Workflow   work.Queries            `json:"workflow"`
	Security   httpapi.RequestSecurity `json:"security"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("api/workflow", func(_ struct{}, d Deps) (*Module, error) {
		if d.Executions == nil || d.Results == nil || d.Controls == nil || d.Workflow == nil || d.Security == nil {
			return nil, fault.ErrInput
		}
		return &Module{d}, nil
	})
}
func (m *Module) Routes() []httpapi.Route {
	return []httpapi.Route{
		{Method: "GET", Path: "/tasks/{id}/workflow", Operation: "workflow.read", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/execution/assign", Operation: "execution.assign", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/execution/open", Operation: "execution.open", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/execution/renew", Operation: "execution.renew", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/execution/end", Operation: "execution.end", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/execution/proof", Operation: "execution.prove", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/execution/isolate", Operation: "execution.isolate", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/results", Operation: "result.submit", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/review", Operation: "result.review", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/withdraw", Operation: "result.withdraw", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/cancel", Operation: "task.cancel", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/return", Operation: "task.return", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/reopen", Operation: "task.reopen", Handler: http.HandlerFunc(m.serve)},
	}
}
func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	token, ok := m.d.Security.Credential(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	path := r.URL.Path
	taskID := r.PathValue("id")
	key := r.Header.Get("Idempotency-Key")
	var result any
	var err error
	switch {
	case strings.HasSuffix(path, "/workflow"):
		result, err = m.d.Workflow.Read(ctx, token, taskID)
	case strings.HasSuffix(path, "/execution/assign"):
		var in work.Assignment
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Executions.Assign(ctx, token, key, in)
	case strings.HasSuffix(path, "/execution/open"):
		var in work.Open
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Executions.Open(ctx, token, key, in)
	case strings.HasSuffix(path, "/execution/renew"), strings.HasSuffix(path, "/execution/end"):
		var in work.ExecutionRef
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		if strings.HasSuffix(path, "/renew") {
			result, err = m.d.Executions.Renew(ctx, token, key, in)
		} else {
			result, err = m.d.Executions.End(ctx, token, key, in)
		}
	case strings.HasSuffix(path, "/execution/isolate"):
		var in work.Isolation
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Controls.Isolate(ctx, token, key, in)
	case strings.HasSuffix(path, "/execution/proof"):
		var in work.StopEvidence
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Executions.ProveStopped(ctx, token, key, in)
	case strings.HasSuffix(path, "/results"):
		var in work.Submit
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Results.Submit(ctx, token, key, in)
	case strings.HasSuffix(path, "/review"):
		var in work.Review
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Results.Review(ctx, token, key, in)
	case strings.HasSuffix(path, "/withdraw"):
		var in work.Withdraw
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.d.Results.Withdraw(ctx, token, key, in)
	case strings.HasSuffix(path, "/cancel"), strings.HasSuffix(path, "/return"), strings.HasSuffix(path, "/reopen"):
		var in work.Basis
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		switch {
		case strings.HasSuffix(path, "/cancel"):
			result, err = m.d.Controls.Cancel(ctx, token, key, in)
		case strings.HasSuffix(path, "/return"):
			result, err = m.d.Controls.Return(ctx, token, key, in)
		default:
			result, err = m.d.Controls.Reopen(ctx, token, key, in)
		}

	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, result)
}
