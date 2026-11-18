package executing

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
	pluginkit.Register("state/executing", func(_ struct{}, d Deps) (*State, error) {
		if d.Results == nil {
			return nil, fault.ErrInput
		}
		return &State{d}, nil
	})
}
func (*State) Describe() flow.Description {
	return flow.Description{Key: "executing", Version: "1", Role: flow.RoleExecuting}
}
func (*State) CheckEnter(context.Context, flow.Context) (flow.Check, error) {
	return flow.Check{Status: flow.Allow}, nil
}
func (*State) Enter(context.Context, flow.Context) error { return nil }
func (s *State) CheckComplete(ctx context.Context, c flow.Context) (flow.Check, error) {
	v, e := s.d.Results.CurrentSubmission(ctx, c.Task.Tenant, c.Task.ID, c.Position.RoundID)
	if errors.Is(e, fault.ErrNotFound) {
		return flow.Check{Status: flow.Wait, Reason: "等待有效结果提交"}, nil
	}
	if e != nil {
		return flow.Check{}, e
	}
	if v.BasisVersion != c.Task.BasisVersion {
		return flow.Check{Status: flow.Wait, Reason: "结果依据已失效"}, nil
	}
	return flow.Check{Status: flow.Allow}, nil
}
