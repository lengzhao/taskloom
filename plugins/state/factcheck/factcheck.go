package factcheck

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"strings"
)

type Config struct {
	RequiredText string `json:"requiredText"`
}
type Deps struct {
	Results delivery.Reader `json:"results"`
	Commits commit.Lookup   `json:"commits"`
}
type State struct {
	c Config
	d Deps
}

func init() {
	pluginkit.Register("state/factcheck", func(c Config, d Deps) (*State, error) {
		if c.RequiredText == "" || d.Results == nil || d.Commits == nil {
			return nil, fault.ErrInput
		}
		return &State{c, d}, nil
	})
}
func (s *State) Describe() flow.Description {
	return flow.Description{Key: "factcheck", Version: "1", Rules: fmt.Sprintf("%x", sha256.Sum256([]byte(s.c.RequiredText)))}
}
func (*State) CheckEnter(context.Context, flow.Context) (flow.Check, error) {
	return flow.Check{Status: flow.Allow}, nil
}
func (*State) Enter(context.Context, flow.Context) error { return nil }
func (s *State) CheckComplete(ctx context.Context, c flow.Context) (flow.Check, error) {
	v, e := s.d.Results.CurrentSubmission(ctx, c.Task.Tenant, c.Task.ID, c.Position.RoundID)
	if errors.Is(e, fault.ErrNotFound) {
		return flow.Check{Status: flow.Wait, Reason: "等待结果"}, nil
	}
	if e != nil {
		return flow.Check{}, e
	}
	body, e := s.d.Commits.Get(ctx, c.Task.Tenant, c.Task.ID, v.CommitID)
	if e != nil {
		return flow.Check{}, e
	}
	if !strings.Contains(body.Body, s.c.RequiredText) {
		return flow.Check{Status: flow.Wait, Reason: "结果未满足本地检查规则"}, nil
	}
	return flow.Check{Status: flow.Allow}, nil
}
