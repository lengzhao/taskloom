package accepting

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"strings"
)

type Deps struct {
	Results delivery.Reader    `json:"results"`
	Reviews acceptance.Records `json:"reviews"`
	Members az.MemberReader    `json:"members"`
}
type State struct{ d Deps }

func New(_ struct{}, d Deps) (*State, error) {
	if d.Results == nil || d.Reviews == nil || d.Members == nil {
		return nil, fault.ErrInput
	}
	return &State{d}, nil
}
func init() { pluginkit.Register("state/accepting", New) }
func (*State) Describe() flow.Description {
	return flow.Description{Key: "accepting", Version: "1", Role: flow.RoleAccepting}
}
func (*State) CheckEnter(context.Context, flow.Context) (flow.Check, error) {
	return flow.Check{Status: flow.Allow}, nil
}
func (s *State) Enter(ctx context.Context, c flow.Context) error {
	v, e := s.d.Results.CurrentSubmission(ctx, c.Task.Tenant, c.Task.ID, c.Position.RoundID)
	if errors.Is(e, fault.ErrNotFound) {
		return nil
	}
	if e != nil {
		return e
	}
	key := fmt.Sprintf("%s/%s/%s", c.Position.RoundID, v.ID, c.Task.Owner)
	return s.d.Reviews.EnsureReview(ctx, acceptance.Review{Tenant: c.Task.Tenant, TaskID: c.Task.ID, ID: fmt.Sprintf("%x", sha256.Sum256([]byte(key))), RoundID: c.Position.RoundID, SubmissionID: v.ID, CommitID: v.CommitID, Reviewer: c.Task.Owner, Outcome: "pending", BasisVersion: c.Task.BasisVersion})
}
func (s *State) CheckComplete(ctx context.Context, c flow.Context) (flow.Check, error) {
	v, e := s.d.Results.CurrentSubmission(ctx, c.Task.Tenant, c.Task.ID, c.Position.RoundID)
	if errors.Is(e, fault.ErrNotFound) {
		return flow.Check{Status: flow.Wait, Reason: "等待当前结果"}, nil
	}
	if e != nil {
		return flow.Check{}, e
	}
	r, e := s.d.Reviews.GetReview(ctx, c.Task.Tenant, c.Task.ID, c.Position.RoundID, v.ID)
	if errors.Is(e, fault.ErrNotFound) {
		return flow.Check{Status: flow.Wait, Reason: "等待验收记录"}, nil
	}
	if e != nil {
		return flow.Check{}, e
	}
	member, e := s.d.Members.GetMember(ctx, c.Task.Tenant, r.Reviewer)
	if e != nil || !member.Active {
		return flow.Check{Status: flow.Wait, Reason: "验收人资格已失效"}, nil
	}
	if r.Outcome != "approved" || r.CommitID != v.CommitID || r.BasisVersion != c.Task.BasisVersion || r.Reviewer != c.Task.Owner {
		return flow.Check{Status: flow.Wait, Reason: "等待当前结果人工验收"}, nil
	}
	return flow.Check{Status: flow.Allow}, nil
}
func (s *State) RecordVerdict(ctx context.Context, c flow.Context, p identity.Principal, in acceptance.Verdict) error {
	if p.Kind != "human" || p.ID != c.Task.Owner {
		return az.ErrForbidden
	}
	if (in.Outcome != "approved" && in.Outcome != "rejected") || len(in.Reason) > 20000 {
		return fault.ErrInput
	}
	// A rejection returns work to the executor, which needs to know what to change.
	if in.Outcome == "rejected" && strings.TrimSpace(in.Reason) == "" {
		return fault.ErrInput
	}
	v, e := s.d.Results.CurrentSubmission(ctx, c.Task.Tenant, c.Task.ID, c.Position.RoundID)
	if e != nil {
		return e
	}
	if v.ID != in.SubmissionID || v.CommitID != in.CommitID || v.BasisVersion != c.Task.BasisVersion {
		return fault.ErrConflict
	}
	r, e := s.d.Reviews.GetReview(ctx, c.Task.Tenant, c.Task.ID, c.Position.RoundID, v.ID)
	if e != nil {
		return e
	}
	r.Outcome = in.Outcome
	r.Reason = in.Reason
	return s.d.Reviews.SaveVerdict(ctx, r)
}
