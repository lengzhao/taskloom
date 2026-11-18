package ready

import (
	"context"
	"strings"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/flow"
)

type State struct{}

func init() { pluginkit.Register("state/ready", func() (*State, error) { return &State{}, nil }) }
func (*State) Describe() flow.Description {
	return flow.Description{Key: "ready", Version: "1", Role: "ready"}
}
func (*State) CheckEnter(context.Context, flow.Context) (flow.Check, error) {
	return flow.Check{Status: flow.Allow}, nil
}
func (*State) Enter(context.Context, flow.Context) error { return nil }
func (*State) CheckComplete(_ context.Context, current flow.Context) (flow.Check, error) {
	if strings.TrimSpace(current.Task.Goal) == "" {
		return flow.Check{Status: flow.Wait, Reason: "请补充任务目标"}, nil
	}
	if strings.TrimSpace(current.Task.Criteria) == "" {
		return flow.Check{Status: flow.Wait, Reason: "请补充验收标准"}, nil
	}
	return flow.Check{Status: flow.Allow}, nil
}
