package workflow_test

import (
	"errors"
	"github.com/lengzhao/taskloom/cap/acceptance"
	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
	"slices"
	"testing"
)

func TestOpenQuestionWaitsOnOwnerAndHoldsCompletion(t *testing.T) {
	f := setup(t, nil)
	qs, ok := f.questions(), true
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
	sub := *s.Result
	if s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: sub.CommitID, Outcome: "approved"}}); e != nil {
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
	qs := f.questions()
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

// A question may be addressed to any active member who can help: it lands in
// their inbox, only they can answer, and completion waits for them like for the owner.
func TestQuestionToMemberWaitsOnThem(t *testing.T) {
	f := setup(t, nil)
	qs := f.questions()
	helper := f.human(t, "helper")
	outsider := f.human(t, "outsider")
	d := f.create(t)
	s := f.open(t, d)
	round := s.Position.RoundID
	ask := func(key, to string) (api.Snapshot, error) {
		return qs.Ask(f.ctx, f.agent, key, api.Ask{TaskID: d.Task.ID, RoundID: round, Body: "这个接口的鉴权怎么配？", Addressee: to})
	}
	if _, e := ask("not-member", "nobody"); !errors.Is(e, fault.ErrInput) {
		t.Fatal("asked a non-member", e)
	}
	if _, e := ask("self", "agent"); !errors.Is(e, fault.ErrInput) {
		t.Fatal("asked itself", e)
	}
	// A member not on the task joins it by being asked, so they can read and answer.
	if _, e := f.query.Read(f.ctx, outsider, d.Task.ID); e == nil {
		t.Fatal("outsider reads the task before being asked")
	}
	s, e := ask("ask-outsider", "outsider")
	if e != nil {
		t.Fatal(e)
	}
	if got := f.read(t, d.Task.ID).Participants; !slices.Contains(got, "outsider") {
		t.Fatal("asked member not added as participant", got)
	}
	o, e := f.query.Read(f.ctx, outsider, d.Task.ID)
	if e != nil || o.Viewer != "outsider" || len(o.Questions) != 1 || o.Questions[0].Addressee != "outsider" {
		t.Fatal("asked member cannot read the question", o.Questions, e)
	}
	if _, e = qs.Answer(f.ctx, outsider, "outsider-answer", api.Answer{TaskID: d.Task.ID, QuestionID: s.Questions[0].ID, Body: "问 helper"}); e != nil {
		t.Fatal(e)
	}
	if e := f.tasks.SetParticipant(f.ctx, f.owner, "add-helper", workspace.Participation{TaskID: d.Task.ID, Subject: "helper", Present: true}); e != nil {
		t.Fatal(e)
	}
	s, e = ask("ask-helper", "helper")
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
	if s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: s.Result.CommitID, Outcome: "approved"}}); e != nil || s.Position.Status == "completed" {
		t.Fatal("completed before the helper answered", e, s.Position)
	}
	if s, e = qs.Answer(f.ctx, helper, "helper-answer", api.Answer{TaskID: d.Task.ID, QuestionID: q.ID, Body: "用 Bearer 令牌"}); e != nil || s.Position.Status != "completed" {
		t.Fatal("helper answer did not release completion", e, s.Position)
	}
}

// A question nobody will answer can be closed with a reason by its asker or
// the owner; it then stops holding completion. The addressee answers instead.
func TestCloseQuestionReleasesCompletion(t *testing.T) {
	f := setup(t, nil)
	qs := f.questions()
	helper := f.human(t, "helper")
	d := f.create(t)
	s := f.open(t, d)
	round := s.Position.RoundID
	asked, e := qs.Ask(f.ctx, f.agent, "ask", api.Ask{TaskID: d.Task.ID, RoundID: round, Body: "哪个环境？", Addressee: "helper"})
	if e != nil {
		t.Fatal(e)
	}
	q := asked.Questions[0]
	if _, e = qs.Close(f.ctx, helper, "addressee-close", api.CloseQuestion{TaskID: d.Task.ID, QuestionID: q.ID, Reason: "不想答"}); !errors.Is(e, az.ErrForbidden) {
		t.Fatal("addressee closed the question", e)
	}
	if _, e = qs.Close(f.ctx, f.agent, "no-reason", api.CloseQuestion{TaskID: d.Task.ID, QuestionID: q.ID}); !errors.Is(e, fault.ErrInput) {
		t.Fatal("closed without a reason", e)
	}
	s = f.read(t, d.Task.ID)
	if s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "result"}); e != nil {
		t.Fatal(e)
	}
	if s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: s.Result.CommitID, Outcome: "approved"}}); e != nil || s.Position.Status == "completed" {
		t.Fatal("completed with an open question", e, s.Position)
	}
	if s, e = qs.Close(f.ctx, f.owner, "owner-close", api.CloseQuestion{TaskID: d.Task.ID, QuestionID: q.ID, Reason: "已从文档确认"}); e != nil || s.Position.Status != "completed" {
		t.Fatal("closing did not release completion", e, s.Position)
	}
	if _, e = qs.Close(f.ctx, f.owner, "again", api.CloseQuestion{TaskID: d.Task.ID, QuestionID: q.ID, Reason: "again"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("closed twice", e)
	}
}
