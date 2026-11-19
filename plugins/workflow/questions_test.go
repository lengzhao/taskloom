package workflow_test

import (
	"errors"
	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/workflow"
	"testing"
)

func TestOpenQuestionWaitsOnOwnerAndHoldsCompletion(t *testing.T) {
	f := setup(t, nil)
	qs, ok := build.GetByID[api.QuestionCommands](f.graph, "question-commands")
	if !ok {
		t.Fatal("question-commands missing")
	}
	var e error
	d := f.create(t)
	s := f.open(t, d)
	round := s.Position.RoundID
	if _, e = qs.Ask(f.ctx, f.owner, "self", api.Ask{TaskID: d.Task.ID, RoundID: round, Body: "?"}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("owner asked itself", e)
	}
	if _, e = qs.Ask(f.ctx, f.agent, "blank", api.Ask{TaskID: d.Task.ID, RoundID: round, Body: "  "}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("blank question", e)
	}
	if _, e = qs.Ask(f.ctx, f.agent, "stale", api.Ask{TaskID: d.Task.ID, RoundID: "old", Body: "?"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("stale round question", e)
	}
	s, e = qs.Ask(f.ctx, f.agent, "ask", api.Ask{TaskID: d.Task.ID, RoundID: round, Body: "导出格式用 CSV 还是 Excel？"})
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Questions) != 1 || s.Questions[0].Addressee != "owner" || s.NextActor != api.ActorOwner {
		t.Fatalf("question not waiting on owner %+v", s.Questions)
	}
	q := s.Questions[0]
	items, e := f.query.Inbox(f.ctx, f.owner)
	if e != nil || len(items) != 1 || items[0].TaskID != d.Task.ID {
		t.Fatal("owner inbox misses question", items, e)
	}

	// The agent keeps working: submit, approve and prove stop. Completion still waits.
	s = f.read(t, d.Task.ID)
	s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "result"})
	if e != nil {
		t.Fatal(e)
	}
	sub := *s.Submission
	if s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{Basis: basis(d, s), Verdict: acceptance.Verdict{SubmissionID: sub.ID, CommitID: sub.CommitID, Outcome: "approved"}}); e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "proof", proof(s)); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	if s.Position.Status != "active" || s.Position.Waiting != "等待负责人回答问题" || s.NextActor != api.ActorOwner {
		t.Fatalf("open question did not hold completion %+v", s.Position)
	}

	if _, e = qs.Answer(f.ctx, f.agent, "agent-answer", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "CSV"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("agent answered owner question", e)
	}
	if s, e = qs.Answer(f.ctx, f.owner, "answer", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "用 CSV"}); e != nil {
		t.Fatal(e)
	}
	if s.Position.Status != "completed" || len(s.Questions) != 0 {
		t.Fatalf("answer did not release completion %+v", s.Position)
	}
	if _, e = qs.Answer(f.ctx, f.owner, "answer-again", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "x"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("answered twice", e)
	}
	stored, e := f.commits.Get(f.ctx, "t", d.Task.ID, q.CommitID)
	if e != nil || stored.Kind != "question" {
		t.Fatal("question text not on timeline", stored, e)
	}
}

func TestQuestionFromOldRoundIsObsolete(t *testing.T) {
	f := setup(t, nil)
	qs, _ := build.GetByID[api.QuestionCommands](f.graph, "question-commands")
	d := f.create(t)
	s := f.open(t, d)
	s, e := qs.Ask(f.ctx, f.agent, "ask", api.Ask{TaskID: d.Task.ID, RoundID: s.Position.RoundID, Body: "?"})
	if e != nil {
		t.Fatal(e)
	}
	q := s.Questions[0]
	s, e = f.controls.Cancel(f.ctx, f.owner, "cancel", basis(d, s))
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Questions) != 0 {
		t.Fatal("cancelled task still shows open questions")
	}
	if _, e = qs.Answer(f.ctx, f.owner, "late", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "late"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("answered question of inactive task", e)
	}
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "proof", proof(f.read(t, d.Task.ID))); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	if s, e = f.controls.Reopen(f.ctx, f.owner, "reopen", basis(d, s)); e != nil {
		t.Fatal(e)
	}
	if len(s.Questions) != 0 || s.Position.RoundID == q.RoundID {
		t.Fatal("old-round question carried into new round", s.Questions)
	}
	if _, e = qs.Answer(f.ctx, f.owner, "old-round", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "x"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("answered obsolete question", e)
	}
}
