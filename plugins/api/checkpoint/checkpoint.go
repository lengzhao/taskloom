package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	capcheck "github.com/lengzhao/taskloom/cap/checkpoint"
	capcommand "github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	"github.com/lengzhao/taskloom/runtime/command"
	"io"
	"net/http"
)

type Deps struct {
	Security      httpapi.RequestSecurity `json:"security"`
	Commands      capcommand.Handler      `json:"commands"`
	Tasks         task.Reader             `json:"tasks"`
	Flow          flow.Controller         `json:"flow"`
	Step          capcheck.Actions        `json:"step"`
	Authorization az.Authorizer           `json:"authorization"`
	Events        record.EventWriter      `json:"events"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("api/checkpoint", func(_ struct{}, d Deps) (*Module, error) {
		if d.Security == nil || d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Step == nil || d.Authorization == nil || d.Events == nil {
			return nil, fault.ErrInput
		}
		return &Module{d}, nil
	})
}
func (m *Module) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "POST", Path: "/tasks/{id}/checkpoint", Operation: "checkpoint.confirm", Handler: http.HandlerFunc(m.serve)}}
}

type input struct {
	RoundID                       string
	ExpectedVersion, BasisVersion int64
	capcheck.Input
}

func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	token, ok := m.d.Security.Credential(w, r)
	if !ok {
		return
	}
	var in input
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&in) != nil {
		http.Error(w, "invalid input", 400)
		return
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "invalid input", 400)
		return
	}
	taskID := r.PathValue("id")
	b, e := command.Run(r.Context(), m.d.Commands, capcommand.Request{Token: token, Key: r.Header.Get("Idempotency-Key"), Operation: "checkpoint.confirm", Resource: taskID, ReadOperation: "task.read", Authorize: func(ctx context.Context, p identity.Principal) error {
		return m.d.Authorization.Check(ctx, p, "task.edit", taskID)
	}}, in, func(ctx context.Context, p identity.Principal) ([]byte, error) {
		t, e := m.d.Tasks.GetTask(ctx, p.Tenant, taskID)
		if e != nil {
			return nil, e
		}
		pos, e := m.d.Flow.Read(ctx, p.Tenant, taskID)
		if e != nil {
			return nil, e
		}
		if pos.Status != flow.PhaseActive || pos.RoundID != in.RoundID || pos.Revision != in.ExpectedVersion || t.BasisVersion != in.BasisVersion {
			return nil, fault.ErrConflict
		}
		current, e := m.d.Flow.Step(ctx, t, m.d.Step.StepKey())
		if e != nil {
			return nil, e
		}
		if e = m.d.Step.Confirm(ctx, current, p, in.Input); e != nil {
			return nil, e
		}
		pos, e = m.d.Flow.Refresh(ctx, t)
		if e != nil {
			return nil, e
		}
		if e = m.d.Events.AppendEvent(ctx, record.Event{Tenant: p.Tenant, Resource: taskID, Kind: "task.changed"}); e != nil {
			return nil, e
		}
		return json.Marshal(pos)
	})
	if e != nil {
		code := 500
		if errors.Is(e, fault.ErrConflict) {
			code = 409
		} else if errors.Is(e, identity.ErrUnauthenticated) {
			code = 401
		} else if errors.Is(e, az.ErrForbidden) {
			code = 403
		} else if errors.Is(e, fault.ErrInput) {
			code = 400
		}
		http.Error(w, "checkpoint not applied", code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}
