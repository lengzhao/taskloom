package flow_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/commit"
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
	c, e := flow.New(flow.Config{Acceptance: mode}, flow.Deps{Positions: s, Results: s, Reviews: s, Questions: s, Commits: s, Plan: s})
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
	tk := task.Task{Tenant: "t", ID: "a", Owner: "u", BasisVersion: 1}
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
		if e = s.Append(ctx, commit.Commit{Tenant: "t", TaskID: "a", ID: "c1", Kind: "result", Body: "r", Author: "x", BasisVersion: 1}); e != nil {
			return e
		}
		sub := delivery.Result{Tenant: "t", TaskID: "a", RoundID: p.RoundID, CommitID: "c1"}
		if e = s.RegisterResult(ctx, sub); e != nil {
			return e
		}
		p, e = c.Refresh(ctx, tk)
		expect(t, p, e, capflow.PhaseActive, capflow.StepAccepting, "等待负责人验收")
		stale := tk
		stale.BasisVersion = 2
		p, e = c.Read(ctx, stale)
		expect(t, p, e, capflow.PhaseActive, capflow.StepExecuting, "结果依据已失效")
		if e = s.Append(ctx, commit.Commit{Tenant: "t", TaskID: "a", ID: "qc", Kind: "question", Body: "?", Author: "x", BasisVersion: 1}); e != nil {
			return e
		}
		if e = s.CreateQuestion(ctx, question.Question{Tenant: "t", TaskID: "a", ID: "q", RoundID: p.RoundID, CommitID: "qc", Addressee: "u"}); e != nil {
			return e
		}
		// A verdict is a commit referring to the result.
		if e = s.Append(ctx, commit.Commit{Tenant: "t", TaskID: "a", ID: "v", Kind: acceptance.KindApproved, RefID: "c1", Author: "u", BasisVersion: 1}); e != nil {
			return e
		}
		p, e = c.Refresh(ctx, tk)
		expect(t, p, e, capflow.PhaseActive, capflow.StepAccepting, "等待被提问人回答问题")
		q, e := s.GetQuestion(ctx, "t", "a", "q")
		if e != nil {
			return e
		}
		q.AnswerCommitID = "answer"
		if e = s.AnswerQuestion(ctx, q); e != nil {
			return e
		}
		p, e = c.Refresh(ctx, tk)
		expect(t, p, e, capflow.PhaseCompleted, capflow.StepCompleted, "")
		// Completed is final: rework is a new task.
		if _, e = c.Restart(ctx, tk); !errors.Is(e, fault.ErrConflict) {
			t.Fatal("completed task restarted", e)
		}
		if p, e = c.Read(ctx, tk); e != nil || p.Status != capflow.PhaseCompleted {
			t.Fatal("rejected restart changed the task", p, e)
		}
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
	tk := task.Task{Tenant: "t", ID: "a", Owner: "u", Goal: "g", Criteria: "c", BasisVersion: 1}
	e := s.Do(context.Background(), func(ctx context.Context) error {
		if e := s.CreateTask(ctx, tk); e != nil {
			return e
		}
		p, e := manual.Initialize(ctx, tk)
		if e != nil {
			return e
		}
		if e = s.Append(ctx, commit.Commit{Tenant: "t", TaskID: "a", ID: "c1", Kind: "result", Body: "r", Author: "x", BasisVersion: 1}); e != nil {
			return e
		}
		if e = s.RegisterResult(ctx, delivery.Result{Tenant: "t", TaskID: "a", RoundID: p.RoundID, CommitID: "c1"}); e != nil {
			return e
		}
		// The deployment switched to no acceptance; this task still waits for review.
		p, e = none.Refresh(ctx, tk)
		expect(t, p, e, capflow.PhaseActive, capflow.StepAccepting, "等待负责人验收")
		if e = s.CreateTask(ctx, task.Task{Tenant: "t", ID: "old"}); e != nil {
			return e
		}
		if e = s.CreatePosition(ctx, capflow.Position{Status: capflow.PhaseActive, Tenant: "t", TaskID: "old", RoundID: "r"}); e != nil {
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
	if _, e = flow.New(flow.Config{Acceptance: "maybe"}, flow.Deps{Positions: s, Results: s, Reviews: s, Questions: s, Commits: s, Plan: s}); e == nil {
		t.Fatal("unknown acceptance mode accepted")
	}
}
