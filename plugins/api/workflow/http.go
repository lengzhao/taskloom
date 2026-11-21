package workflow

import (
	"context"
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	work "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/runtime/httpx"
	"net/http"
	"strings"
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
func (m *Module) Routes() []httpapi.Route {
	return []httpapi.Route{
		{Method: "GET", Path: "/inbox", Operation: "workflow.inbox", Handler: http.HandlerFunc(m.serve)},
		{Method: "GET", Path: "/overview", Operation: "workflow.overview", Handler: http.HandlerFunc(m.serve)},
		{Method: "GET", Path: "/tasks/{id}/workflow", Operation: "workflow.read", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/execution/assign", Operation: "execution.assign", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/execution/open", Operation: "execution.open", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/results", Operation: "result.submit", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/review", Operation: "result.review", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/questions", Operation: "question.ask", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/questions/{qid}/answer", Operation: "question.answer", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/questions/{qid}/close", Operation: "question.close", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/cancel", Operation: "task.cancel", Handler: http.HandlerFunc(m.serve)},
		{Method: "POST", Path: "/tasks/{id}/restart", Operation: "task.restart", Handler: http.HandlerFunc(m.serve)},
	}
}

// after answers a command with the caller's current snapshot. The receipt
// keeps only the command's Ref, so a replay also shows current facts.
func (m *Module) after(ctx context.Context, token, taskID string) func(work.Ref, error) (any, error) {
	return func(_ work.Ref, err error) (any, error) {
		if err != nil {
			return nil, err
		}
		return m.d.Workflow.Read(ctx, token, taskID)
	}
}
func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	token := httpx.Credential(r)
	ctx := r.Context()
	path := r.URL.Path
	taskID := r.PathValue("id")
	key := r.Header.Get("Idempotency-Key")
	var result any
	var err error
	switch {
	case strings.HasSuffix(path, "/questions"):
		var in work.Ask
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.after(ctx, token, taskID)(m.d.Questions.Ask(ctx, token, key, in))
	case strings.HasSuffix(path, "/answer"):
		var in work.Answer
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID, in.QuestionID = taskID, r.PathValue("qid")
		result, err = m.after(ctx, token, taskID)(m.d.Questions.Answer(ctx, token, key, in))
	case strings.HasSuffix(path, "/close"):
		var in work.CloseQuestion
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID, in.QuestionID = taskID, r.PathValue("qid")
		result, err = m.after(ctx, token, taskID)(m.d.Questions.Close(ctx, token, key, in))
	case strings.HasSuffix(path, "/overview"):
		result, err = m.d.Workflow.Overview(ctx, token, r.URL.Query()["id"])
	case strings.HasSuffix(path, "/inbox"):
		result, err = m.d.Workflow.Inbox(ctx, token, r.URL.Query()["id"])
	case strings.HasSuffix(path, "/workflow"):
		result, err = m.d.Workflow.Read(ctx, token, taskID)
	case strings.HasSuffix(path, "/execution/assign"):
		var in work.Assignment
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.after(ctx, token, taskID)(m.d.Executions.Assign(ctx, token, key, in))
	case strings.HasSuffix(path, "/execution/open"):
		var in work.Open
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.after(ctx, token, taskID)(m.d.Executions.Open(ctx, token, key, in))
	case strings.HasSuffix(path, "/results"):
		var in work.Submit
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.after(ctx, token, taskID)(m.d.Results.Submit(ctx, token, key, in))
	case strings.HasSuffix(path, "/review"):
		var in work.Review
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.after(ctx, token, taskID)(m.d.Results.Review(ctx, token, key, in))
	case strings.HasSuffix(path, "/cancel"):
		var in work.Cancel
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.after(ctx, token, taskID)(m.d.Controls.Cancel(ctx, token, key, in))
	case strings.HasSuffix(path, "/restart"):
		var in work.Restart
		if !httpx.Decode(w, r, &in) {
			return
		}
		in.TaskID = taskID
		result, err = m.after(ctx, token, taskID)(m.d.Controls.Restart(ctx, token, key, in))
	}
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, result)
}
