package completed

import (
	"context"
	"errors"
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
)

type Deps struct {
	Results delivery.Reader `json:"results"`
}
type State struct{ d Deps }

func init() {
	pluginkit.Register("state/completed", func(_ struct{}, d Deps) (*State, error) {
		if d.Results == nil {
			return nil, fault.ErrInput
		}
		return &State{d}, nil
	})
}
func (*State) Describe() flow.Description {
	return flow.Description{Key: "completed", Version: "1", Role: "completed"}
}
func (*State) CheckEnter(context.Context, flow.Context) (flow.Check, error) {
	return flow.Check{Status: flow.Allow}, nil
}
func (*State) Enter(context.Context, flow.Context) error { return nil }
func (s *State) CheckComplete(ctx context.Context, c flow.Context) (flow.Check, error) {
	v, e := s.d.Results.CurrentSubmission(ctx, c.Task.Tenant, c.Task.ID, c.Position.RoundID)
	if errors.Is(e, fault.ErrNotFound) {
		return flow.Check{Status: flow.Wait, Reason: "没有当前交付"}, nil
	}
	if e != nil {
		return flow.Check{}, e
	}
	if v.BasisVersion != c.Task.BasisVersion {
		return flow.Check{Status: flow.Wait, Reason: "依据不匹配"}, nil
	}
	return flow.Check{Status: flow.Allow}, nil
}
