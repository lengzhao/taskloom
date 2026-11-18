package ready

import (
	"context"
	"strings"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/task"
)

type State struct{}

func init()                               { pluginkit.Register("state/ready", func() (*State, error) { return &State{}, nil }) }
func (*State) Describe() flow.Description { return flow.Description{Key: "ready", Version: "1"} }
func (*State) CheckEnter(context.Context, task.Task) (flow.Check, error) {
	return flow.Check{Status: flow.Allow}, nil
}
func (*State) Enter(context.Context, task.Task) error { return nil }
func (*State) CheckComplete(_ context.Context, t task.Task) (flow.Check, error) {
	if strings.TrimSpace(t.Goal) == "" {
		return flow.Check{Status: flow.Wait, Reason: "请补充任务目标"}, nil
	}
	if strings.TrimSpace(t.Criteria) == "" {
		return flow.Check{Status: flow.Wait, Reason: "请补充验收标准"}, nil
	}
	return flow.Check{Status: flow.Allow}, nil
}
