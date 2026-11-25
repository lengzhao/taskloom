// Package workflow adapts the work on a task to HTTP: assigning, starting,
// delivering, accepting, asking and answering, cancelling and restarting.
// Rules live in the commands; every write answers with the caller's current
// task view, the same as GET /tasks/{id}.
package workflow

import (
	"context"
	"net/http"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	work "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/runtime/httpx"
)

type Deps struct {
	Executions work.ExecutionCommands `json:"executions"`
	Results    work.ResultCommands    `json:"results"`
	Controls   work.ControlCommands   `json:"controls"`
	Workflow   work.Queries           `json:"workflow"`
	Questions  work.QuestionCommands  `json:"questions"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("api/workflow", func(_ struct{}, d Deps) (*Module, error) {
		if d.Executions == nil || d.Results == nil || d.Controls == nil || d.Workflow == nil || d.Questions == nil {
			return nil, fault.ErrInput
		}
		return &Module{d}, nil
	})
}

// command decodes the body into In, fills the path IDs and runs one command.
func command[In any](m *Module, op, path string, set func(*In, *http.Request), run func(context.Context, string, string, In) (work.Ref, error)) httpapi.Route {
	return httpapi.Route{Method: "POST", Path: path, Operation: op, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in In
		if !httpx.Decode(w, r, &in) {
			return
		}
		set(&in, r)
		token := httpx.Credential(r)
		_, err := run(r.Context(), token, r.Header.Get("Idempotency-Key"), in)
		httpx.View(r.Context(), w, m.d.Workflow, token, r.PathValue("id"), err)
	})}
}

func (m *Module) Routes() []httpapi.Route {
	id := func(r *http.Request) string { return r.PathValue("id") }
	return []httpapi.Route{
		command(m, "execution.assign", "/tasks/{id}/assign", func(in *work.Assignment, r *http.Request) { in.TaskID = id(r) }, m.d.Executions.Assign),
		command(m, "execution.open", "/tasks/{id}/start", func(in *work.Open, r *http.Request) { in.TaskID = id(r) }, m.d.Executions.Open),
		command(m, "result.submit", "/tasks/{id}/results", func(in *work.Submit, r *http.Request) { in.TaskID = id(r) }, m.d.Results.Submit),
		command(m, "result.review", "/tasks/{id}/review", func(in *work.Review, r *http.Request) { in.TaskID = id(r) }, m.d.Results.Review),
		command(m, "question.ask", "/tasks/{id}/questions", func(in *work.Ask, r *http.Request) { in.TaskID = id(r) }, m.d.Questions.Ask),
		command(m, "question.answer", "/tasks/{id}/questions/{qid}/answer", func(in *work.Answer, r *http.Request) { in.TaskID, in.QuestionID = id(r), r.PathValue("qid") }, m.d.Questions.Answer),
		command(m, "question.close", "/tasks/{id}/questions/{qid}/close", func(in *work.CloseQuestion, r *http.Request) { in.TaskID, in.QuestionID = id(r), r.PathValue("qid") }, m.d.Questions.Close),
		command(m, "task.cancel", "/tasks/{id}/cancel", func(in *work.Cancel, r *http.Request) { in.TaskID = id(r) }, m.d.Controls.Cancel),
		command(m, "task.restart", "/tasks/{id}/restart", func(in *work.Restart, r *http.Request) { in.TaskID = id(r) }, m.d.Controls.Restart),
	}
}
