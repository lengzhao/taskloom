package workflow_test

import (
	"errors"
	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
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
	if len(s.Questions) != 1 || s.Questions[0].Addressee != "owner" || s.NextActor != api.ActorAddressee {
		t.Fatalf("question not waiting on owner %+v", s.Questions)
	}
	q := s.Questions[0]
	items, e := f.query.Inbox(f.ctx, f.owner)
	if e != nil || len(items) != 1 || items[0].TaskID != d.Task.ID {
		t.Fatal("owner inbox misses question", items, e)
	}

	// The agent keeps working: submit and approve. Completion still waits for the answer.
	s = f.read(t, d.Task.ID)
	s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "result"})
	if e != nil {
		t.Fatal(e)
	}
	sub := *s.Submission
	if s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{SubmissionID: sub.ID, CommitID: sub.CommitID, Outcome: "approved"}}); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	if s.Position.Status != "active" || s.Position.Waiting != "等待被提问人回答问题" || s.NextActor != api.ActorAddressee {
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
	s, e = f.controls.Cancel(f.ctx, f.owner, "cancel", api.Cancel{TaskID: d.Task.ID})
	if e != nil {
		t.Fatal(e)
	}
	if len(s.Questions) != 0 {
		t.Fatal("cancelled task still shows open questions")
	}
	if _, e = qs.Answer(f.ctx, f.owner, "late", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "late"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("answered question of inactive task", e)
	}
	s = f.read(t, d.Task.ID)
	if s, e = f.controls.Restart(f.ctx, f.owner, "reopen", api.Restart{TaskID: d.Task.ID, RoundID: s.Position.RoundID}); e != nil {
		t.Fatal(e)
	}
	if len(s.Questions) != 0 || s.Position.RoundID == q.RoundID {
		t.Fatal("old-round question carried into new round", s.Questions)
	}
	if _, e = qs.Answer(f.ctx, f.owner, "old-round", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "x"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("answered obsolete question", e)
	}
}

// A question may be addressed to a participant who can help: it lands in their
// inbox, only they can answer, and completion waits for them like for the owner.
func TestQuestionToParticipantWaitsOnThem(t *testing.T) {
	f := setup(t, nil)
	qs, _ := build.GetByID[api.QuestionCommands](f.graph, "question-commands")
	helper := f.human(t, "helper")
	f.human(t, "outsider")
	d := f.create(t)
	s := f.open(t, d)
	round := s.Position.RoundID
	ask := func(key, to string) (api.Snapshot, error) {
		return qs.Ask(f.ctx, f.agent, key, api.Ask{TaskID: d.Task.ID, RoundID: round, Body: "这个接口的鉴权怎么配？", Addressee: to})
	}
	if _, e := ask("not-participant", "outsider"); !errors.Is(e, fault.ErrInput) {
		t.Fatal("asked a non-participant", e)
	}
	if _, e := ask("self", "agent"); !errors.Is(e, fault.ErrInput) {
		t.Fatal("asked itself", e)
	}
	if e := f.tasks.SetParticipant(f.ctx, f.owner, "add-helper", workspace.Participation{TaskID: d.Task.ID, Subject: "helper", Present: true}); e != nil {
		t.Fatal(e)
	}
	s, e := ask("ask-helper", "helper")
	if e != nil {
		t.Fatal(e)
	}
	q := s.Questions[0]
	if q.Addressee != "helper" || s.NextActor != api.ActorAddressee {
		t.Fatal("question not addressed to helper", q.Addressee, s.NextActor)
	}
	if items, _ := f.query.Inbox(f.ctx, helper); len(items) != 1 || items[0].TaskID != d.Task.ID {
		t.Fatal("helper inbox misses the question", items)
	}
	if items, _ := f.query.Inbox(f.ctx, f.owner); len(items) != 0 {
		t.Fatal("owner is not the one asked", items)
	}
	if _, e = qs.Answer(f.ctx, f.owner, "owner-answer", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "x"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("owner answered a question asked of the helper", e)
	}
	s = f.read(t, d.Task.ID)
	s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "result"})
	if e != nil {
		t.Fatal(e)
	}
	if s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{SubmissionID: s.Submission.ID, CommitID: s.Submission.CommitID, Outcome: "approved"}}); e != nil || s.Position.Status == "completed" {
		t.Fatal("completed before the helper answered", e, s.Position)
	}
	if s, e = qs.Answer(f.ctx, helper, "helper-answer", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "用 Bearer 令牌"}); e != nil || s.Position.Status != "completed" {
		t.Fatal("helper answer did not release completion", e, s.Position)
	}
}
