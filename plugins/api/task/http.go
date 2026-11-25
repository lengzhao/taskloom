// Package task adapts the task's own commands to HTTP: listing, reading,
// creating and editing a task, its timeline, participants, project, owner and
// place in the work breakdown. Rules live in the commands; every task write
// answers with the caller's current task view, the same as GET /tasks/{id}.
package task

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/plan"
	work "github.com/lengzhao/taskloom/cap/workflow"
	api "github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/runtime/httpx"
)

type Deps struct {
	Tasks     api.Commands  `json:"tasks"`
	Ownership api.Ownership `json:"ownership"`
	Plan      plan.Commands `json:"plan"`
	Views     work.Queries  `json:"views"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("api/task", func(_ struct{}, d Deps) (*Module, error) {
		if d.Tasks == nil || d.Ownership == nil || d.Plan == nil || d.Views == nil {
			return nil, errors.New("task capabilities required")
		}
		return &Module{d}, nil
	})
}

func (m *Module) Routes() []httpapi.Route {
	h := func(method, path, op string, f func(http.ResponseWriter, *http.Request)) httpapi.Route {
		return httpapi.Route{Method: method, Path: path, Operation: op, Handler: http.HandlerFunc(f)}
	}
	return []httpapi.Route{
		h("GET", "/tasks", "tasks.list", m.list),
		h("POST", "/tasks", "tasks.create", m.create),
		h("GET", "/tasks/{id}", "tasks.get", m.get),
		h("PATCH", "/tasks/{id}", "tasks.edit", m.edit),
		h("POST", "/tasks/{id}/commits", "commits.append", m.comment),
		h("POST", "/tasks/{id}/participants", "participants.set", m.participants),
		h("POST", "/tasks/{id}/project", "task.project", m.project),
		h("POST", "/tasks/{id}/owner", "task.owner", m.owner),
		h("POST", "/tasks/{id}/parent", "task.plan", m.parent),
	}
}

func key(r *http.Request) string { return r.Header.Get("Idempotency-Key") }

func (m *Module) list(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := work.ListFilter{IDs: q["id"]}
	if raw := q.Get("mine"); raw != "" {
		mine, err := strconv.ParseBool(raw)
		if err != nil {
			httpx.Fail(w, fault.Input(fault.CodeInvalid, "mine"))
			return
		}
		f.Mine = mine
	}
	out, err := m.d.Views.List(r.Context(), httpx.Credential(r), f)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, out)
}

func (m *Module) get(w http.ResponseWriter, r *http.Request) {
	before := int64(0)
	if raw := r.URL.Query().Get("before"); raw != "" {
		var err error
		if before, err = strconv.ParseInt(raw, 10, 64); err != nil {
			httpx.Fail(w, fault.Input(fault.CodeInvalid, "before"))
			return
		}
	}
	v, err := m.d.Views.Read(r.Context(), httpx.Credential(r), r.PathValue("id"), before)
	if err != nil {
		httpx.Fail(w, err)
		return
	}
	httpx.Write(w, v)
}

func (m *Module) create(w http.ResponseWriter, r *http.Request) {
	var in api.Create
	if !httpx.Decode(w, r, &in) {
		return
	}
	token := httpx.Credential(r)
	ref, err := m.d.Tasks.Create(r.Context(), token, key(r), in)
	httpx.View(r.Context(), w, m.d.Views, token, ref.TaskID, err)
}

func (m *Module) edit(w http.ResponseWriter, r *http.Request) {
	var in api.Edit
	if !httpx.Decode(w, r, &in) {
		return
	}
	in.TaskID = r.PathValue("id")
	token := httpx.Credential(r)
	_, err := m.d.Tasks.Edit(r.Context(), token, key(r), in)
	httpx.View(r.Context(), w, m.d.Views, token, in.TaskID, err)
}

func (m *Module) comment(w http.ResponseWriter, r *http.Request) {
	var in api.Comment
	if !httpx.Decode(w, r, &in) {
		return
	}
	in.TaskID = r.PathValue("id")
	token := httpx.Credential(r)
	_, err := m.d.Tasks.Comment(r.Context(), token, key(r), in)
	httpx.View(r.Context(), w, m.d.Views, token, in.TaskID, err)
}

func (m *Module) participants(w http.ResponseWriter, r *http.Request) {
	var in api.Participation
	if !httpx.Decode(w, r, &in) {
		return
	}
	in.TaskID = r.PathValue("id")
	token := httpx.Credential(r)
	err := m.d.Tasks.SetParticipant(r.Context(), token, key(r), in)
	httpx.View(r.Context(), w, m.d.Views, token, in.TaskID, err)
}

func (m *Module) project(w http.ResponseWriter, r *http.Request) {
	var in api.ProjectMove
	if !httpx.Decode(w, r, &in) {
		return
	}
	in.TaskID = r.PathValue("id")
	token := httpx.Credential(r)
	err := m.d.Tasks.SetProject(r.Context(), token, key(r), in)
	httpx.View(r.Context(), w, m.d.Views, token, in.TaskID, err)
}

func (m *Module) owner(w http.ResponseWriter, r *http.Request) {
	var in api.Handover
	if !httpx.Decode(w, r, &in) {
		return
	}
	in.TaskID = r.PathValue("id")
	token := httpx.Credential(r)
	_, err := m.d.Ownership.Handover(r.Context(), token, key(r), in)
	httpx.View(r.Context(), w, m.d.Views, token, in.TaskID, err)
}

func (m *Module) parent(w http.ResponseWriter, r *http.Request) {
	var in plan.Placement
	if !httpx.Decode(w, r, &in) {
		return
	}
	in.ChildID = r.PathValue("id")
	token := httpx.Credential(r)
	err := m.d.Plan.Place(r.Context(), token, key(r), in)
	httpx.View(r.Context(), w, m.d.Views, token, in.ChildID, err)
}
