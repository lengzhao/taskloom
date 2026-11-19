package workflow

import (
	"context"
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/command"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workflow"
)

type ExecutionDeps struct {
	Principals   identity.PrincipalReader `json:"principals"`
	Participants az.ParticipantWriter     `json:"participants"`
	Members      az.MemberReader          `json:"members"`
	Commands     command.Handler          `json:"commands"`
	Tasks        task.Reader              `json:"tasks"`
	Flow         flow.Controller          `json:"flow"`
	Executions   execution.Store          `json:"executions"`
	Snapshots    api.FactsReader          `json:"snapshots"`
	Events       record.EventWriter       `json:"events"`
}
type ResultDeps struct {
	Commands   command.Handler        `json:"commands"`
	Tasks      task.Reader            `json:"tasks"`
	Flow       flow.Controller        `json:"flow"`
	Executions execution.ResultAccess `json:"executions"`
	Results    delivery.Store         `json:"results"`
	Commits    commit.Log             `json:"commits"`
	Accepting  acceptance.Actions     `json:"accepting,omitempty"`
	Snapshots  api.FactsReader        `json:"snapshots"`
	Events     record.EventWriter     `json:"events"`
}
type ControlDeps struct {
	Commands   command.Handler    `json:"commands"`
	Tasks      task.Reader        `json:"tasks"`
	Flow       flow.Controller    `json:"flow"`
	Executions execution.Store    `json:"executions"`
	Snapshots  api.FactsReader    `json:"snapshots"`
	Events     record.EventWriter `json:"events"`
}
type ExecutionModule struct{ s *Service }
type ResultModule struct{ s *Service }
type ControlModule struct{ s *Service }

func init() {
	pluginkit.Register("commands/execution", NewExecution)
	pluginkit.Register("commands/result", NewResults)
	pluginkit.Register("commands/flow", NewControl)
}
func NewExecution(c Config, d ExecutionDeps) (*ExecutionModule, error) {
	if c.LeaseSeconds == 0 {
		c.LeaseSeconds = 60
	}
	if c.LeaseSeconds < 1 || c.LeaseSeconds > 3600 || d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Executions == nil || d.Principals == nil || d.Members == nil || d.Participants == nil || d.Snapshots == nil || d.Events == nil {
		return nil, fault.ErrInput
	}
	return &ExecutionModule{&Service{c, dependencies{Commands: d.Commands, Tasks: d.Tasks, Flow: d.Flow, Executions: d.Executions, ExecutionWrites: d.Executions, Principals: d.Principals, Members: d.Members, Participants: d.Participants, Snapshots: d.Snapshots, Events: d.Events}}}, nil
}
func NewResults(_ struct{}, d ResultDeps) (*ResultModule, error) {
	if d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Executions == nil || d.Results == nil || d.Commits == nil || d.Snapshots == nil || d.Events == nil {
		return nil, fault.ErrInput
	}
	return &ResultModule{&Service{d: dependencies{Commands: d.Commands, Tasks: d.Tasks, Flow: d.Flow, Executions: d.Executions, ResultExecutions: d.Executions, Results: d.Results, Commits: d.Commits, Accepting: d.Accepting, Snapshots: d.Snapshots, Events: d.Events}}}, nil
}
func NewControl(_ struct{}, d ControlDeps) (*ControlModule, error) {
	if d.Commands == nil || d.Tasks == nil || d.Flow == nil || d.Executions == nil || d.Snapshots == nil || d.Events == nil {
		return nil, fault.ErrInput
	}
	return &ControlModule{&Service{d: dependencies{Commands: d.Commands, Tasks: d.Tasks, Flow: d.Flow, Executions: d.Executions, ExecutionWrites: d.Executions, Snapshots: d.Snapshots, Events: d.Events}}}, nil
}

func (m *ExecutionModule) Open(ctx context.Context, token, key string, in api.Open) (api.Snapshot, error) {
	return m.s.Open(ctx, token, key, in)
}

func (m *ExecutionModule) Renew(ctx context.Context, token, key string, in api.ExecutionRef) (api.Snapshot, error) {
	return m.s.Renew(ctx, token, key, in)
}

func (m *ExecutionModule) End(ctx context.Context, token, key string, in api.ExecutionRef) (api.Snapshot, error) {
	return m.s.End(ctx, token, key, in)
}

func (m *ExecutionModule) ProveStopped(ctx context.Context, token, key string, in api.StopEvidence) (api.Snapshot, error) {
	return m.s.ProveStopped(ctx, token, key, in)
}

func (m *ResultModule) Submit(ctx context.Context, token, key string, in api.Submit) (api.Snapshot, error) {
	return m.s.Submit(ctx, token, key, in)
}

func (m *ResultModule) Review(ctx context.Context, token, key string, in api.Review) (api.Snapshot, error) {
	return m.s.Review(ctx, token, key, in)
}

func (m *ResultModule) Withdraw(ctx context.Context, token, key string, in api.Withdraw) (api.Snapshot, error) {
	return m.s.Withdraw(ctx, token, key, in)
}

func (m *ControlModule) Cancel(ctx context.Context, token, key string, in api.Basis) (api.Snapshot, error) {
	return m.s.Cancel(ctx, token, key, in)
}

func (m *ControlModule) Return(ctx context.Context, token, key string, in api.Basis) (api.Snapshot, error) {
	return m.s.Return(ctx, token, key, in)
}

func (m *ControlModule) Reopen(ctx context.Context, token, key string, in api.Basis) (api.Snapshot, error) {
	return m.s.Reopen(ctx, token, key, in)
}

func (m *ExecutionModule) Assign(ctx context.Context, token, key string, in api.Assignment) (api.Snapshot, error) {
	return m.s.Assign(ctx, token, key, in)
}

func (m *ControlModule) Isolate(ctx context.Context, token, key string, in api.Isolation) (api.Snapshot, error) {
	return m.s.Isolate(ctx, token, key, in)
}
