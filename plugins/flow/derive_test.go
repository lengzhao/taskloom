package flow_test

import (
	"context"
	"testing"

	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	capflow "github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/plan"
	"github.com/lengzhao/taskloom/cap/question"
	"github.com/lengzhao/taskloom/cap/task"
)

// facts lists what one derivation case writes before reading the position.
type facts struct {
	mode     capflow.Acceptance
	noGoal   bool
	result   bool   // a current result on the task's basis
	verdict  string // "", acceptance.KindApproved or acceptance.KindRejected
	child    string // "", "required" or "optional": a direct subtask
	childOK  bool   // the subtask is completed
	question bool   // an unanswered question in the current round
	cancel   bool
}

// The step and what it waits on follow the documented order (docs/task-flow.md
// "推导什么"): rejection, cancellation and the priority of required subtasks
// over open questions in Waiting.
func TestDerivationTable(t *testing.T) {
	for _, c := range []struct {
		name     string
		f        facts
		status   capflow.Phase
		step     capflow.Step
		waiting  string
		subtasks bool
	}{
		{"manual rejected waits for resubmission", facts{mode: capflow.AcceptanceManual, result: true, verdict: acceptance.KindRejected}, capflow.PhaseActive, capflow.StepAccepting, "验收未通过，等待重新提交", false},
		// Rejection is an earlier unmet condition, so pending subtasks and open questions do not replace it.
		{"rejection outranks subtasks and questions", facts{mode: capflow.AcceptanceManual, result: true, verdict: acceptance.KindRejected, child: "required", question: true}, capflow.PhaseActive, capflow.StepAccepting, "验收未通过，等待重新提交", false},
		{"manual approved with a required subtask pending", facts{mode: capflow.AcceptanceManual, result: true, verdict: acceptance.KindApproved, child: "required"}, capflow.PhaseActive, capflow.StepAccepting, "等待必需子任务完成", true},
		{"cancelled keeps the derived step", facts{mode: capflow.AcceptanceManual, cancel: true}, capflow.PhaseCancelled, capflow.StepExecuting, "任务已取消", false},
		{"cancelled without a goal", facts{mode: capflow.AcceptanceManual, noGoal: true, cancel: true}, capflow.PhaseCancelled, capflow.StepReady, "任务已取消", false},
		{"cancelled after rejection", facts{mode: capflow.AcceptanceManual, result: true, verdict: acceptance.KindRejected, cancel: true}, capflow.PhaseCancelled, capflow.StepAccepting, "任务已取消", false},
		{"cancelled hides subtasks and questions", facts{mode: capflow.AcceptanceManual, child: "required", question: true, cancel: true}, capflow.PhaseCancelled, capflow.StepExecuting, "任务已取消", false},
		// Executing waits for its result first; the subtask wait replaces that text.
		{"executing: required subtask before question", facts{mode: capflow.AcceptanceManual, child: "required", question: true}, capflow.PhaseActive, capflow.StepExecuting, "等待必需子任务完成", true},
		{"none with result: required subtask before question", facts{mode: capflow.AcceptanceNone, result: true, child: "required", question: true}, capflow.PhaseActive, capflow.StepExecuting, "等待必需子任务完成", true},
		{"approved: required subtask before question", facts{mode: capflow.AcceptanceManual, result: true, verdict: acceptance.KindApproved, child: "required", question: true}, capflow.PhaseActive, capflow.StepAccepting, "等待必需子任务完成", true},
		{"optional subtask does not wait", facts{mode: capflow.AcceptanceNone, result: true, child: "optional", question: true}, capflow.PhaseActive, capflow.StepExecuting, "等待被提问人回答问题", false},
		{"completed required subtask leaves the question", facts{mode: capflow.AcceptanceNone, result: true, child: "required", childOK: true, question: true}, capflow.PhaseActive, capflow.StepExecuting, "等待被提问人回答问题", false},
		{"ready ignores subtasks", facts{mode: capflow.AcceptanceManual, noGoal: true, child: "required"}, capflow.PhaseActive, capflow.StepReady, "请补充任务目标", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			s := open(t)
			ctl := controller(t, s, c.f.mode)
			tk := task.Task{Tenant: "t", ID: "a", Owner: "u", Goal: "g", BasisVersion: 1}
			if c.f.noGoal {
				tk.Goal = ""
			}
			e := s.Do(context.Background(), func(ctx context.Context) error {
				if e := s.CreateTask(ctx, tk); e != nil {
					return e
				}
				p, e := ctl.Initialize(ctx, tk, c.f.mode)
				if e != nil {
					return e
				}
				if c.f.result {
					if e = s.Append(ctx, commit.Commit{Tenant: "t", TaskID: "a", ID: "r1", Kind: "result", Body: "r", Author: "x", BasisVersion: 1}); e != nil {
						return e
					}
					if e = s.RegisterResult(ctx, delivery.Result{Tenant: "t", TaskID: "a", RoundID: p.RoundID, CommitID: "r1"}); e != nil {
						return e
					}
				}
				if c.f.verdict != "" {
					if e = s.Append(ctx, commit.Commit{Tenant: "t", TaskID: "a", ID: "v1", Kind: c.f.verdict, Body: "why", RefID: "r1", Author: "u", BasisVersion: 1}); e != nil {
						return e
					}
				}
				if c.f.child != "" {
					child := task.Task{Tenant: "t", ID: "b", Owner: "u", Goal: "g", BasisVersion: 1}
					if e = s.CreateTask(ctx, child); e != nil {
						return e
					}
					cp, e := ctl.Initialize(ctx, child, capflow.AcceptanceNone)
					if e != nil {
						return e
					}
					if c.f.childOK {
						cp.Status = capflow.PhaseCompleted
						cp.State, cp.Waiting = "", ""
						if e = s.SavePosition(ctx, cp); e != nil {
							return e
						}
					}
					if e = s.SetParent(ctx, plan.Link{Tenant: "t", ParentID: "a", ChildID: "b", Required: c.f.child == "required"}); e != nil {
						return e
					}
				}
				if c.f.question {
					if e = s.Append(ctx, commit.Commit{Tenant: "t", TaskID: "a", ID: "qc", Kind: "question", Body: "?", Author: "x", BasisVersion: 1}); e != nil {
						return e
					}
					if e = s.CreateQuestion(ctx, question.Question{Tenant: "t", TaskID: "a", ID: "q", RoundID: p.RoundID, CommitID: "qc", Addressee: "u"}); e != nil {
						return e
					}
				}
				if c.f.cancel {
					if _, e = ctl.Cancel(ctx, tk); e != nil {
						return e
					}
				}
				// Refresh never completes a task that still waits on something.
				p, e = ctl.Refresh(ctx, tk)
				expect(t, p, e, c.status, c.step, c.waiting)
				if p.WaitingOnSubtasks != c.subtasks {
					t.Fatalf("WaitingOnSubtasks %v, want %v", p.WaitingOnSubtasks, c.subtasks)
				}
				// Read derives the same position without writing.
				r, e := ctl.Read(ctx, tk)
				expect(t, r, e, c.status, c.step, c.waiting)
				return nil
			})
			if e != nil {
				t.Fatal(e)
			}
		})
	}
}
