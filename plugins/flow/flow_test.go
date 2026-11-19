package flow_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/fault"
	capflow "github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/question"
	"github.com/lengzhao/taskloom/cap/task"
	flow "github.com/lengzhao/taskloom/plugins/flow"
	store "github.com/lengzhao/taskloom/plugins/storage/sql"
)

func open(t *testing.T) *store.Store {
	t.Helper()
	s, e := store.New(store.Config{Driver: "sqlite", DSN: filepath.Join(t.TempDir(), "db")})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Open(context.Background()); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { s.Close(context.Background()) })
	return s
}

func controller(t *testing.T, s *store.Store, mode capflow.Acceptance) *flow.Controller {
	t.Helper()
	c, e := flow.New(flow.Config{Acceptance: mode}, flow.Deps{Positions: s, Results: s, Reviews: s, Questions: s, Executions: s})
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func expect(t *testing.T, p capflow.Position, e error, status capflow.Phase, step capflow.Step, waiting string) {
	t.Helper()
	if e != nil || p.Status != status || p.State != step || p.Waiting != waiting {
		t.Fatalf("got %s/%s %q %v, want %s/%s %q", p.Status, p.State, p.Waiting, e, status, step, waiting)
	}
}

// The step is a function of facts; completion needs a current result, the
// owner's approval in manual mode and no open question.
func TestStepIsDerivedFromFacts(t *testing.T) {
	s := open(t)
	c := controller(t, s, "")
	tk := task.Task{Tenant: "t", ID: "a", Owner: "u", Version: 1, BasisVersion: 1}
	e := s.Do(context.Background(), func(ctx context.Context) error {
		if e := s.CreateTask(ctx, tk); e != nil {
			return e
		}
		p, e := c.Initialize(ctx, tk)
		expect(t, p, e, capflow.PhaseActive, capflow.StepReady, "请补充任务目标")
		if _, e = c.CheckOpen(ctx, tk); !errors.Is(e, fault.ErrConflict) {
			t.Fatal("claim allowed without a basis", e)
		}
		tk.Goal, tk.Criteria = "g", "c"
		p, e = c.Refresh(ctx, tk)
		expect(t, p, e, capflow.PhaseActive, capflow.StepExecuting, "等待有效结果提交")
		sub := delivery.Submission{Tenant: "t", TaskID: "a", ID: "s1", RoundID: p.RoundID, CommitID: "c1", BasisVersion: 1}
		if e = s.RegisterSubmission(ctx, sub); e != nil {
			return e
		}
		p, e = c.Refresh(ctx, tk)
		expect(t, p, e, capflow.PhaseActive, capflow.StepAccepting, "等待负责人验收")
		if e = c.CheckEdit(ctx, tk); !errors.Is(e, fault.ErrConflict) {
			t.Fatal("basis edit allowed under review", e)
		}
		stale := tk
		stale.BasisVersion = 2
		p, e = c.Read(ctx, stale)
		expect(t, p, e, capflow.PhaseActive, capflow.StepExecuting, "结果依据已失效")
		if e = s.CreateQuestion(ctx, question.Question{Tenant: "t", TaskID: "a", ID: "q", RoundID: p.RoundID, Asker: "x", Addressee: "u", Status: question.StatusOpen}); e != nil {
			return e
		}
		review := acceptance.Review{Tenant: "t", TaskID: "a", ID: "r", RoundID: p.RoundID, SubmissionID: "s1", CommitID: "c1", Reviewer: "u", Outcome: acceptance.OutcomeApproved, BasisVersion: 1}
		if e = s.SaveReview(ctx, review); e != nil {
			return e
		}
		p, e = c.Refresh(ctx, tk)
		expect(t, p, e, capflow.PhaseActive, capflow.StepAccepting, "等待被提问人回答问题")
		q, e := s.GetQuestion(ctx, "t", "a", "q")
		if e != nil {
			return e
		}
		q.Status = question.StatusAnswered
		if e = s.AnswerQuestion(ctx, q); e != nil {
			return e
		}
		before := p.Revision
		p, e = c.Refresh(ctx, tk)
		expect(t, p, e, capflow.PhaseCompleted, capflow.StepCompleted, "")
		if p.Revision != before+1 {
			t.Fatal("completion did not advance the revision", p.Revision)
		}
		p, e = c.Reopen(ctx, tk)
		expect(t, p, e, capflow.PhaseActive, capflow.StepExecuting, "等待有效结果提交")
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
}

// A task keeps the acceptance mode it was created with, and a position
// without a recorded mode is rejected instead of guessed.
func TestAcceptanceIsFixedPerTask(t *testing.T) {
	s := open(t)
	manual, none := controller(t, s, capflow.AcceptanceManual), controller(t, s, capflow.AcceptanceNone)
	tk := task.Task{Tenant: "t", ID: "a", Owner: "u", Goal: "g", Criteria: "c", Version: 1, BasisVersion: 1}
	e := s.Do(context.Background(), func(ctx context.Context) error {
		if e := s.CreateTask(ctx, tk); e != nil {
			return e
		}
		p, e := manual.Initialize(ctx, tk)
		if e != nil {
			return e
		}
		if e = s.RegisterSubmission(ctx, delivery.Submission{Tenant: "t", TaskID: "a", ID: "s1", RoundID: p.RoundID, CommitID: "c1", BasisVersion: 1}); e != nil {
			return e
		}
		// The deployment switched to no acceptance; this task still waits for review.
		p, e = none.Refresh(ctx, tk)
		expect(t, p, e, capflow.PhaseActive, capflow.StepAccepting, "等待负责人验收")
		if e = s.CreateTask(ctx, task.Task{Tenant: "t", ID: "old", Version: 1}); e != nil {
			return e
		}
		if e = s.CreatePosition(ctx, capflow.Position{Revision: 1, Status: capflow.PhaseActive, Tenant: "t", TaskID: "old", RoundID: "r"}); e != nil {
			return e
		}
		if _, e = none.Read(ctx, task.Task{Tenant: "t", ID: "old"}); !errors.Is(e, fault.ErrConflict) {
			t.Fatal("position without acceptance mode accepted", e)
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = flow.New(flow.Config{Acceptance: "maybe"}, flow.Deps{Positions: s, Results: s, Reviews: s, Questions: s, Executions: s}); e == nil {
		t.Fatal("unknown acceptance mode accepted")
	}
}
