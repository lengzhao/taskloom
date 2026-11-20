// Package flow implements the task lifecycle as a function of task facts.
// It stores only phase, round and the acceptance mode fixed at creation; the
// step and what it waits on are recomputed from the basis, the current
// result, its review, open questions and required subtasks every time they
// are read.
package flow

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/fault"
	capflow "github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/plan"
	"github.com/lengzhao/taskloom/cap/question"
	"github.com/lengzhao/taskloom/cap/task"
)

type Config struct {
	// Acceptance is the mode new tasks are created with: manual (default) or none.
	Acceptance capflow.Acceptance `json:"acceptance"`
}

func (c *Config) SetDefaults() {
	if c.Acceptance == "" {
		c.Acceptance = capflow.AcceptanceManual
	}
}
func (c *Config) Validate() error {
	if !knownAcceptance(c.Acceptance) {
		return fmt.Errorf("unknown acceptance %q", c.Acceptance)
	}
	return nil
}

// Deps are the facts completion depends on, plus the stored position.
// Six fields, reviewed: Plan is one more real input to the same completion
// rule; a separate plugin would only move that rule, and the submit check and
// parent lookup reuse it so commands need not read relations themselves.
type Deps struct {
	Positions capflow.PositionStore `json:"positions"`
	Results   delivery.Reader       `json:"results"`
	// Commits reads the result commit, whose basis version decides validity.
	Commits   commit.Lookup     `json:"commits"`
	Reviews   acceptance.Reader `json:"reviews"`
	Questions question.Reader   `json:"questions"`
	// Plan reads the direct required subtasks a delivery waits for.
	Plan plan.Reader `json:"plan"`
}
type Controller struct {
	c Config
	d Deps
}

func init() { pluginkit.Register("task/flow", New) }

func New(c Config, d Deps) (*Controller, error) {
	c.SetDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if d.Positions == nil || d.Results == nil || d.Commits == nil || d.Reviews == nil || d.Questions == nil || d.Plan == nil {
		return nil, errors.New("flow requires positions, results, commits, reviews, questions and plan")
	}
	return &Controller{c, d}, nil
}

func knownAcceptance(a capflow.Acceptance) bool {
	return a == capflow.AcceptanceManual || a == capflow.AcceptanceNone
}

func id() string {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic("entropy unavailable")
	}
	return fmt.Sprintf("%x", b)
}

func (c *Controller) Initialize(ctx context.Context, t task.Task) (capflow.Position, error) {
	p := capflow.Position{Status: capflow.PhaseActive, Tenant: t.Tenant, TaskID: t.ID, RoundID: id(), Acceptance: c.c.Acceptance}
	if e := c.d.Positions.CreatePosition(ctx, p); e != nil {
		return p, e
	}
	p, _, e := c.derive(ctx, t, p)
	return p, e
}

// load reads the stored position. Rows written before the acceptance mode was
// recorded are rejected rather than guessed.
func (c *Controller) load(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.d.Positions.GetPosition(ctx, t.Tenant, t.ID)
	if e != nil {
		return p, e
	}
	if !knownAcceptance(p.Acceptance) {
		return p, fault.ErrConflict
	}
	p.State, p.Waiting, p.WaitingOnSubtasks = "", "", false
	return p, nil
}

// waitingChildren is the waiting text while a direct required subtask is not
// completed; children are not refreshed, each is complete only by its own rule.
const waitingChildren = "等待必需子任务完成"

// derive fills State and Waiting from facts. done reports that an active task
// satisfies completion: a valid result for the current basis, the owner's
// approval when acceptance is manual, no open question in the round and every
// direct required subtask completed.
func (c *Controller) derive(ctx context.Context, t task.Task, p capflow.Position) (capflow.Position, bool, error) {
	if p.Status == capflow.PhaseCompleted {
		p.State = capflow.StepCompleted
		return p, false, nil
	}
	state, waiting, e := c.result(ctx, t, p)
	if e != nil {
		return p, false, e
	}
	p.State, p.Waiting = state, waiting
	if p.Status == capflow.PhaseActive && state != capflow.StepReady && (state == capflow.StepExecuting || waiting == "") {
		pending, e := c.childrenPending(ctx, t)
		if e != nil {
			return p, false, e
		}
		if pending {
			p.Waiting, p.WaitingOnSubtasks = waitingChildren, true
		}
	}
	if p.Waiting == "" {
		open, e := c.d.Questions.OpenQuestions(ctx, t.Tenant, t.ID, p.RoundID)
		if e != nil {
			return p, false, e
		}
		if len(open) > 0 {
			p.Waiting = "等待被提问人回答问题"
		}
	}
	if p.Status == capflow.PhaseCancelled {
		p.Waiting = "任务已取消"
		return p, false, nil
	}
	return p, p.Waiting == "", nil
}

// result derives the step from the basis, the current result and its review.
func (c *Controller) result(ctx context.Context, t task.Task, p capflow.Position) (capflow.Step, string, error) {
	if strings.TrimSpace(t.Goal) == "" {
		return capflow.StepReady, "请补充任务目标", nil
	}
	if strings.TrimSpace(t.Criteria) == "" {
		return capflow.StepReady, "请补充验收标准", nil
	}
	v, e := c.d.Results.CurrentResult(ctx, t.Tenant, t.ID, p.RoundID)
	if errors.Is(e, fault.ErrNotFound) {
		return capflow.StepExecuting, "等待有效结果提交", nil
	}
	if e != nil {
		return "", "", e
	}
	result, e := c.d.Commits.Get(ctx, t.Tenant, t.ID, v.CommitID)
	if e != nil {
		return "", "", e
	}
	if result.BasisVersion != t.BasisVersion {
		return capflow.StepExecuting, "结果依据已失效", nil
	}
	if p.Acceptance == capflow.AcceptanceNone {
		return capflow.StepExecuting, "", nil
	}
	r, e := c.d.Reviews.LatestReview(ctx, t.Tenant, t.ID, v.CommitID)
	if errors.Is(e, fault.ErrNotFound) {
		return capflow.StepAccepting, "等待负责人验收", nil
	}
	if e != nil {
		return "", "", e
	}
	if r.Outcome == acceptance.OutcomeRejected {
		return capflow.StepAccepting, "验收未通过，等待重新提交", nil
	}
	if r.Outcome != acceptance.OutcomeApproved || r.BasisVersion != t.BasisVersion || r.Reviewer != t.Owner {
		return capflow.StepAccepting, "等待负责人验收", nil
	}
	return capflow.StepAccepting, "", nil
}

// childrenPending reports whether a direct required subtask is not completed.
// A cancelled subtask does not satisfy the parent either.
func (c *Controller) childrenPending(ctx context.Context, t task.Task) (bool, error) {
	links, e := c.d.Plan.Children(ctx, t.Tenant, t.ID)
	if e != nil {
		return false, e
	}
	for _, l := range links {
		if !l.Required {
			continue
		}
		child, e := c.d.Positions.GetPosition(ctx, l.Tenant, l.ChildID)
		if e != nil {
			return false, e
		}
		if child.Status != capflow.PhaseCompleted {
			return true, nil
		}
	}
	return false, nil
}

// CheckSubmit allows a result only once every direct required subtask is completed.
func (c *Controller) CheckSubmit(ctx context.Context, t task.Task) error {
	pending, e := c.childrenPending(ctx, t)
	if e != nil {
		return e
	}
	if pending {
		return fault.ErrConflict
	}
	return nil
}

// Parent names the task whose derived view changes with this one, or "".
func (c *Controller) Parent(ctx context.Context, t task.Task) (string, error) {
	l, e := c.d.Plan.Parent(ctx, t.Tenant, t.ID)
	if errors.Is(e, fault.ErrNotFound) {
		return "", nil
	}
	return l.ParentID, e
}

func (c *Controller) Read(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.load(ctx, t)
	if e != nil {
		return p, e
	}
	p, _, e = c.derive(ctx, t, p)
	return p, e
}

func (c *Controller) Refresh(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.load(ctx, t)
	if e != nil {
		return p, e
	}
	p, done, e := c.derive(ctx, t, p)
	if e != nil || !done {
		return p, e
	}
	p.Status = capflow.PhaseCompleted
	if p, e = c.save(ctx, p); e != nil {
		return p, e
	}
	return c.Read(ctx, t)
}

// save writes the stored fields of p.
func (c *Controller) save(ctx context.Context, p capflow.Position) (capflow.Position, error) {
	stored := p
	stored.State, stored.Waiting, stored.WaitingOnSubtasks = "", "", false
	return p, c.d.Positions.SavePosition(ctx, stored)
}

func (c *Controller) CheckOpen(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.Read(ctx, t)
	if e != nil {
		return p, e
	}
	if p.Status != capflow.PhaseActive || p.State == capflow.StepReady {
		return p, fault.ErrConflict
	}
	return p, nil
}

func (c *Controller) Cancel(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.load(ctx, t)
	if e != nil {
		return p, e
	}
	if p.Status != capflow.PhaseActive {
		return p, fault.ErrConflict
	}
	p.Status = capflow.PhaseCancelled
	if _, e = c.save(ctx, p); e != nil {
		return p, e
	}
	return c.Read(ctx, t)
}

// Restart reopens a cancelled task in a new active round; work and results of
// the old round stay in history. Only a cancelled task reopens: active work
// already has narrower ways back (review, reassignment, basis edit), and
// completed is final, so a completion never has to be taken back.
func (c *Controller) Restart(ctx context.Context, t task.Task) (capflow.Position, error) {
	p, e := c.load(ctx, t)
	if e != nil {
		return p, e
	}
	if p.Status != capflow.PhaseCancelled {
		return p, fault.ErrConflict
	}
	p.RoundID = id()
	p.Status = capflow.PhaseActive
	if _, e = c.save(ctx, p); e != nil {
		return p, e
	}
	return c.Refresh(ctx, t)
}
