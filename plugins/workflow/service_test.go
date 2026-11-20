package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/delivery"
	"github.com/lengzhao/taskloom/cap/execution"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	"github.com/lengzhao/taskloom/cap/record"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/plugins/security"
	"github.com/lengzhao/taskloom/runtime/config"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fixture struct {
	graph        *build.Result
	ctx          context.Context
	tasks        taskCommands
	taskQueries  workspace.Queries
	ex           executionCommands
	results      resultCommands
	controls     controlCommands
	query        api.Queries
	security     *security.Service
	owner, agent string
	tx           unitofwork.Manager
	exStore      execution.Store
	commits      commit.Log
	deliveries   delivery.Reader
	reviews      acceptance.Reader
}

func setup(t *testing.T, override map[string]any) *fixture {
	t.Helper()
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.yaml")
	if override == nil {
		override = map[string]any{}
	}
	override["sql"] = map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}}
	b, _ := json.Marshal(override)
	if e := os.WriteFile(overlay, b, 0600); e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	_, graph, e := config.BuildFiles(ctx, "../../base.yaml", overlay)
	if e != nil {
		t.Fatal(e)
	}
	resource, _ := build.GetByID[lifecycle.Resource](graph, "sql")
	if e = resource.Open(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { resource.Close(ctx) })
	f := &fixture{ctx: ctx, graph: graph}
	commands, _ := build.GetByID[workspace.Commands](graph, "workspace")
	f.taskQueries, _ = build.GetByID[workspace.Queries](graph, "workspace")
	f.tasks = taskCommands{commands, f.taskQueries}
	f.query, _ = build.GetByID[api.Queries](graph, "workflow-query")
	ex, _ := build.GetByID[api.ExecutionCommands](graph, "execution-commands")
	results, _ := build.GetByID[api.ResultCommands](graph, "result-commands")
	controls, _ := build.GetByID[api.ControlCommands](graph, "flow-commands")
	f.ex = executionCommands{ex, readBack{f.query}}
	f.results = resultCommands{results, readBack{f.query}}
	f.controls = controlCommands{controls, readBack{f.query}}
	f.security, _ = build.GetByID[*security.Service](graph, "security")
	f.tx, _ = build.GetByID[unitofwork.Manager](graph, "sql")
	f.exStore, _ = build.GetByID[execution.Store](graph, "sql")
	f.commits, _ = build.GetByID[commit.Log](graph, "sql")
	f.deliveries, _ = build.GetByID[delivery.Reader](graph, "sql")
	f.reviews, _ = build.GetByID[acceptance.Reader](graph, "sql")
	if e := f.security.Bootstrap(ctx, security.AdminInput{Tenant: "t", Subject: "owner", Operator: "test", Reason: "fixture", Password: "AdminPassword123!"}); e != nil {
		t.Fatal(e)
	}
	issued, e := f.security.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: "owner", Password: "AdminPassword123!"})
	if e != nil {
		t.Fatal(e)
	}
	f.owner = issued.Secret
	if e = f.security.Create(ctx, f.owner, "agent", identity.Principal{Tenant: "t", ID: "agent", Kind: "agent", OwnerID: "owner"}); e != nil {
		t.Fatal(e)
	}
	for _, purpose := range []string{"business"} {
		issued, e = f.security.Issue(ctx, f.owner, purpose, capsecurity.IssueInput{Tenant: "t", Subject: "agent", ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		f.agent = issued.Secret
	}
	return f
}
func (f *fixture) create(t *testing.T) workspace.Detail {
	t.Helper()
	d, e := f.tasks.Create(f.ctx, f.owner, "create", workspace.Create{Title: "task", Goal: "goal", Criteria: "criteria"})
	if e != nil {
		t.Fatal(e)
	}
	return d
}
func (f *fixture) read(t *testing.T, id string) api.Snapshot {
	t.Helper()
	s, e := f.query.Read(f.ctx, f.owner, id)
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func (f *fixture) open(t *testing.T, d workspace.Detail) api.Snapshot {
	t.Helper()
	s := f.read(t, d.Task.ID)
	s, e := f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{TaskID: d.Task.ID, Subject: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.ex.Open(f.ctx, f.agent, "open", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion})
	if e != nil {
		t.Fatal(e)
	}
	return s
}

func TestApprovalCompletesTheTask(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	round := s.Position.RoundID
	var e error
	s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "immutable result"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.State != "accepting" || s.Position.RoundID != round || s.Review == nil {
		t.Fatalf("submission %+v", s)
	}
	submitted := *s.Result
	events, _ := build.GetByID[record.EventLog](f.graph, "sql")
	before, e := events.ReadEvents(f.ctx, "t", 0, 1000)
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: submitted.CommitID, Outcome: "approved", Reason: "符合标准"}})
	if e != nil {
		t.Fatal(e)
	}
	after, e := events.ReadEvents(f.ctx, "t", 0, 1000)
	if e != nil || len(after) != len(before)+1 || after[len(after)-1].Kind != record.EventTaskChanged {
		t.Fatal("a command must write exactly one task event", len(before), len(after), e)
	}
	timeline, e := f.commits.List(f.ctx, "t", d.Task.ID, 0, 100)
	if e != nil {
		t.Fatal(e)
	}
	if last := timeline[len(timeline)-1]; last.Kind != acceptance.KindApproved || last.RefID != submitted.CommitID || last.Body != "符合标准" {
		t.Fatal("verdict missing from the timeline", last)
	}
	if s.Position.Status != "completed" || s.Position.RoundID != round || s.NextActor != "" {
		t.Fatalf("approval did not complete %+v", s.Position)
	}
	c, e := f.commits.Get(f.ctx, "t", d.Task.ID, submitted.CommitID)
	if e != nil || c.Body != "immutable result" {
		t.Fatal("result content drift", c, e)
	}
	// Completed is final: rework is a new task, so the completion stands.
	if _, e = f.controls.Restart(f.ctx, f.owner, "reopen", api.Restart{TaskID: d.Task.ID, RoundID: s.Position.RoundID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("completed task reopened", e)
	}
	if now := f.read(t, d.Task.ID); now.Position.Status != "completed" || now.Position.RoundID != round || now.Result == nil || now.Result.CommitID != submitted.CommitID {
		t.Fatal("rejected reopen changed the task", now.Position, now.Result)
	}
}

// The basis may change while work runs. Work on the old basis can no longer
// submit, a result on it stops counting, and the executor claims again.
func TestBasisEditDuringWorkRequiresNewClaim(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	old := *s.Execution
	first, e := f.results.Submit(f.ctx, f.agent, "first", api.Submit{TaskID: d.Task.ID, ExecutionID: old.ID, Body: "first"})
	if e != nil || first.Position.State != "accepting" {
		t.Fatal("first result", first.Position, e)
	}
	edited, e := f.tasks.Edit(f.ctx, f.owner, "edit-running", workspace.Edit{TaskID: d.Task.ID, Title: "task", Goal: "new goal", Criteria: "criteria", BasisVersion: d.Task.BasisVersion})
	if e != nil {
		t.Fatal("basis edit rejected", e)
	}
	if edited.Position.State != "executing" || edited.Position.Waiting != "结果依据已失效" {
		t.Fatal("stale result still under review", edited.Position)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "stale", api.Submit{TaskID: d.Task.ID, ExecutionID: old.ID, Body: "stale"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old basis submitted", e)
	}
	agentView, e := f.query.Read(f.ctx, f.agent, d.Task.ID)
	if e != nil || agentView.Blocked[api.ActionSubmit] != api.ReasonBasisChanged || !agentView.CanOpen {
		t.Fatal("agent not told to claim again", agentView.Blocked, agentView.CanOpen, e)
	}
	if _, e = f.ex.Open(f.ctx, f.agent, "stale-open", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("claim on unseen basis", e)
	}
	s, e = f.ex.Open(f.ctx, f.agent, "reopen", api.Open{TaskID: d.Task.ID, BasisVersion: edited.Task.BasisVersion})
	if e != nil || s.Execution.ID == old.ID {
		t.Fatal("new claim", e)
	}
	if s, e = f.results.Submit(f.ctx, f.agent, "second", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "second"}); e != nil || s.Position.State != "accepting" {
		t.Fatal("new result", s.Position, e)
	}
}

func TestCancelFencesWritesImmediately(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	old := *s.Execution
	var e error
	s, e = f.controls.Cancel(f.ctx, f.owner, "cancel", api.Cancel{TaskID: d.Task.ID})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.Status != "cancelled" || s.Execution.Writable || s.NextActor != "" {
		t.Fatal("cancel did not fence", s.Execution, s.NextActor)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "late", api.Submit{TaskID: d.Task.ID, ExecutionID: old.ID, Body: "late"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("late write", e)
	}
	if items, _ := f.query.Inbox(f.ctx, f.agent); len(items) != 0 {
		t.Fatal("cancelled task still waits on the agent", items)
	}
	s, e = f.controls.Restart(f.ctx, f.owner, "reopen", api.Restart{TaskID: d.Task.ID, RoundID: s.Position.RoundID})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.ex.Open(f.ctx, f.agent, "new-open", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion})
	if e != nil {
		t.Fatal(e)
	}
	if s.Execution.ID == old.ID {
		t.Fatal("generation not advanced")
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "old-again", api.Submit{TaskID: d.Task.ID, ExecutionID: old.ID, Body: "old"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old generation wrote into the new round", e)
	}
}
func TestConcurrentClaimAndSilenceKeepsExecution(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.read(t, d.Task.ID)
	var e error
	s, e = f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{TaskID: d.Task.ID, Subject: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	in := api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion}
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, key := range []string{"claim-a", "claim-b"} {
		wg.Add(1)
		go func(key string) { defer wg.Done(); _, e := f.ex.Open(f.ctx, f.agent, key, in); errs <- e }(key)
	}
	wg.Wait()
	close(errs)
	ok, conflict := 0, 0
	for e := range errs {
		if e == nil {
			ok++
		} else if errors.Is(e, fault.ErrConflict) {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if ok != 1 || conflict != 1 {
		t.Fatal(ok, conflict)
	}
	// Silence never expires an execution: a blind retry still finds it running.
	s = f.read(t, d.Task.ID)
	if !s.Execution.Writable {
		t.Fatal("execution changed without a command")
	}
	if _, e = f.ex.Open(f.ctx, f.agent, "blind-retry", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("blind retry", e)
	}
}

// Approval completes a task, so a replacement candidate can only arrive while a
// review is pending; it replaces that review and an old verdict cannot apply.
func TestNewCandidateReplacesPendingReview(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	var e error
	s, e = f.results.Submit(f.ctx, f.agent, "first", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "first"})
	if e != nil {
		t.Fatal(e)
	}
	old := *s.Result
	s, e = f.results.Submit(f.ctx, f.agent, "second", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "second"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Review == nil || s.Review.Outcome != "pending" || s.Review.CommitID == old.CommitID {
		t.Fatal("old review inherited")
	}
	if _, e = f.results.Review(f.ctx, f.owner, "stale-review", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: old.CommitID, Outcome: "approved"}}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old review click", e)
	}
}
func TestFlowWithoutAcceptanceCompletesOnResult(t *testing.T) {
	f := setup(t, map[string]any{"flow": map[string]any{"config": map[string]any{"acceptance": "none"}}})
	d := f.create(t)
	s := f.open(t, d)
	var e error
	s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "auto result"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Review != nil || s.Position.Status != "completed" {
		t.Fatal("auto flow did not complete on submission", s.Position)
	}
}

func TestSameRoundRetryFencesOldExecutionAndReopenAdoptsBasis(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	old := *s.Execution
	oldRound := s.Position.RoundID
	// A restarted agent on a new instance claims again; the old generation is fenced.
	other, e := f.security.Issue(f.ctx, f.owner, "other-instance", capsecurity.IssueInput{Tenant: "t", Subject: "agent", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	s, e = f.ex.Open(f.ctx, other.Secret, "retry", api.Open{TaskID: d.Task.ID, BasisVersion: d.Task.BasisVersion})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.RoundID != oldRound || s.Execution.ID == old.ID {
		t.Fatal("retry switched round or reused generation")
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "old-result", api.Submit{TaskID: d.Task.ID, ExecutionID: old.ID, Body: "late old"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old execution wrote", e)
	}
	// Active work is never restarted: a new round comes only from reopening a cancelled task.
	s = f.read(t, d.Task.ID)
	if _, e = f.controls.Restart(f.ctx, f.owner, "return", api.Restart{TaskID: d.Task.ID, RoundID: s.Position.RoundID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("active task restarted", e)
	}
	if now := f.read(t, d.Task.ID); now.Position.RoundID != oldRound || now.Execution == nil || !now.Execution.Writable {
		t.Fatal("rejected restart changed the task", now.Position, now.Execution)
	}
	if s, e = f.controls.Cancel(f.ctx, f.owner, "cancel", api.Cancel{TaskID: d.Task.ID}); e != nil {
		t.Fatal(e)
	}
	s, e = f.controls.Restart(f.ctx, f.owner, "reopen", api.Restart{TaskID: d.Task.ID, RoundID: s.Position.RoundID})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.RoundID == oldRound || s.Result != nil {
		t.Fatal("reopen did not isolate round")
	}
	edited, e := f.tasks.Edit(f.ctx, f.owner, "adopt-basis", workspace.Edit{TaskID: d.Task.ID, Title: "new goal", Goal: "new", Criteria: "new", BasisVersion: d.Task.BasisVersion})
	if e != nil {
		t.Fatal("new round could not adopt basis", e)
	}
	if edited.Task.Owner != d.Task.Owner || edited.Task.BasisVersion != 2 {
		t.Fatal("owner changed or basis not versioned")
	}
}

// The receipt keeps only the command's Ref: a replay returns that Ref, while
// facts are read at the time of the replay, never the stale state of the write.
func TestReplayReturnsSameRefAndCurrentFacts(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	results, _ := build.GetByID[api.ResultCommands](f.graph, "result-commands")
	in := api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "result"}
	first, e := results.Submit(f.ctx, f.agent, "submit", in)
	if e != nil {
		t.Fatal(e)
	}
	if first.TaskID != d.Task.ID || first.RoundID != s.Position.RoundID {
		t.Fatal("ref does not name the task and round", first)
	}
	submitted := f.read(t, d.Task.ID).Result
	if _, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{TaskID: d.Task.ID, Verdict: acceptance.Verdict{CommitID: submitted.CommitID, Outcome: "approved"}}); e != nil {
		t.Fatal(e)
	}
	replayed, e := results.Submit(f.ctx, f.agent, "submit", in)
	if e != nil || replayed != first {
		t.Fatal("replay changed the ref", replayed, first, e)
	}
	if now := f.read(t, d.Task.ID); now.Position.Status != flow.PhaseCompleted {
		t.Fatal("current facts not visible after replay", now.Position)
	}
	receipts, _ := build.GetByID[record.Receipts](f.graph, "sql")
	stored, e := receipts.FindReceipt(f.ctx, record.RequestKey{Tenant: "t", Subject: "agent", Operation: "result.submit", Key: "submit"})
	if e != nil {
		t.Fatal(e)
	}
	var fields map[string]any
	if e = json.Unmarshal(stored.Result, &fields); e != nil {
		t.Fatal(e)
	}
	for k := range fields {
		if k != "TaskID" && k != "RoundID" && k != "ExecutionID" {
			t.Fatal("receipt stores derivable facts", k)
		}
	}
}
