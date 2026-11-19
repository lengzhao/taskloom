package checkpoint

import (
	"context"
	"errors"
	"github.com/lengzhao/pluginkit"
	az "github.com/lengzhao/taskloom/cap/authorization"
	capcheck "github.com/lengzhao/taskloom/cap/checkpoint"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
)

type Deps struct {
	Records capcheck.Records `json:"records"`
	Results delivery.Reader  `json:"results"`
}
type State struct{ d Deps }

func init() {
	pluginkit.Register("state/checkpoint", func(_ struct{}, d Deps) (*State, error) {
		if d.Records == nil || d.Results == nil {
			return nil, fault.ErrInput
		}
		return &State{d}, nil
	})
}
func (*State) Describe() flow.Description { return flow.Description{Key: "checkpoint", Version: "1"} }
func (*State) StepKey() string            { return "checkpoint" }
func (*State) CheckEnter(context.Context, flow.Context) (flow.Check, error) {
	return flow.Check{Status: flow.Allow}, nil
}
func (s *State) record(ctx context.Context, c flow.Context) (capcheck.Record, error) {
	v, e := s.d.Results.CurrentSubmission(ctx, c.Task.Tenant, c.Task.ID, c.Position.RoundID)
	return capcheck.Record{Tenant: c.Task.Tenant, TaskID: c.Task.ID, RoundID: c.Position.RoundID, SubmissionID: v.ID, CommitID: v.CommitID, BasisVersion: c.Task.BasisVersion}, e
}
func (s *State) Enter(ctx context.Context, c flow.Context) error {
	r, e := s.record(ctx, c)
	if errors.Is(e, fault.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	return s.d.Records.EnsureCheckpoint(ctx, r)
}
func (s *State) CheckComplete(ctx context.Context, c flow.Context) (flow.Check, error) {
	r, e := s.record(ctx, c)
	if errors.Is(e, fault.ErrNotFound) {
		return flow.Check{Status: flow.Wait, Reason: "等待结果"}, nil
	}
	if e != nil {
		return flow.Check{}, e
	}
	r, e = s.d.Records.ReadCheckpoint(ctx, r)
	if errors.Is(e, fault.ErrNotFound) {
		return flow.Check{Status: flow.Wait, Reason: "等待确认记录"}, nil
	}
	if e != nil {
		return flow.Check{}, e
	}
	if !r.Confirmed {
		return flow.Check{Status: flow.Wait, Reason: "等待当前结果的外部确认"}, nil
	}
	return flow.Check{Status: flow.Allow}, nil
}
func (s *State) Confirm(ctx context.Context, c flow.Context, p identity.Principal, in capcheck.Input) error {
	if len(in.Note) > 20000 {
		return fault.ErrInput
	}
	if p.Kind != "human" || p.ID != c.Task.Owner {
		return az.ErrForbidden
	}
	r, e := s.record(ctx, c)
	if e != nil {
		return e
	}
	if r.SubmissionID != in.SubmissionID || r.CommitID != in.CommitID {
		return fault.ErrConflict
	}
	existing, e := s.d.Records.ReadCheckpoint(ctx, r)
	if e != nil {
		return e
	}
	if existing.Confirmed {
		return fault.ErrConflict
	}
	r.Confirmed = true
	r.Actor = p.ID
	r.Note = in.Note
	return s.d.Records.ConfirmCheckpoint(ctx, r)
}
