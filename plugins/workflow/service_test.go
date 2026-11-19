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
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	api "github.com/lengzhao/taskloom/cap/workflow"
	workspace "github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/domain/security"
	_ "github.com/lengzhao/taskloom/plugins"
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
	tasks        workspace.Commands
	taskQueries  workspace.Queries
	ex           api.ExecutionCommands
	results      api.ResultCommands
	controls     api.ControlCommands
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
	f.tasks, _ = build.GetByID[workspace.Commands](graph, "workspace")
	f.taskQueries, _ = build.GetByID[workspace.Queries](graph, "workspace")
	f.ex, _ = build.GetByID[api.ExecutionCommands](graph, "execution-commands")
	f.results, _ = build.GetByID[api.ResultCommands](graph, "result-commands")
	f.controls, _ = build.GetByID[api.ControlCommands](graph, "flow-commands")
	f.query, _ = build.GetByID[api.Queries](graph, "workflow-query")
	f.security, _ = build.GetByID[*security.Service](graph, "security")
	f.tx, _ = build.GetByID[unitofwork.Manager](graph, "sql")
	f.exStore, _ = build.GetByID[execution.Store](graph, "sql")
	f.commits, _ = build.GetByID[commit.Log](graph, "sql")
	f.deliveries, _ = build.GetByID[delivery.Reader](graph, "sql")
	f.reviews, _ = build.GetByID[acceptance.Reader](graph, "sql")
	issued, e := f.security.Bootstrap(ctx, security.BootstrapInput{Tenant: "t", Subject: "owner", Operator: "test", Reason: "fixture", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	f.owner = issued.Secret
	if e = f.security.Create(ctx, f.owner, "agent", identity.Principal{Tenant: "t", ID: "agent", Kind: "agent", OwnerID: "owner"}); e != nil {
		t.Fatal(e)
	}
	for _, purpose := range []string{"business"} {
		issued, e = f.security.Issue(ctx, f.owner, purpose, capsecurity.IssueInput{Tenant: "t", Subject: "agent", InstanceID: "machine", Incarnation: "boot", ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		f.agent = issued.Secret
	}
	return f
}
func basis(d workspace.Detail, s api.Snapshot) api.Basis {
	return api.Basis{TaskID: d.Task.ID, RoundID: s.Position.RoundID, BasisVersion: d.Task.BasisVersion, ExpectedRevision: s.Position.Revision}
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
	s, e := f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{Basis: basis(d, s), Subject: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.ex.Open(f.ctx, f.agent, "open", api.Open{Basis: basis(d, s)})
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
	if _, e := f.tasks.Edit(f.ctx, f.owner, "edit-running", workspace.Edit{TaskID: d.Task.ID, Title: "changed", ExpectedVersion: d.Task.Version}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("running basis edited", e)
	}
	var e error
	s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "immutable result"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.State != "accepting" || s.Position.RoundID != round || s.Review == nil {
		t.Fatalf("submission %+v", s)
	}
	submitted := *s.Submission
	s, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{Basis: basis(d, s), Verdict: acceptance.Verdict{SubmissionID: submitted.ID, CommitID: submitted.CommitID, Outcome: "approved"}})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.Status != "completed" || s.Position.RoundID != round || s.NextActor != "" {
		t.Fatalf("approval did not complete %+v", s.Position)
	}
	c, e := f.commits.Get(f.ctx, "t", d.Task.ID, submitted.CommitID)
	if e != nil || c.Body != "immutable result" {
		t.Fatal("result content drift", c, e)
	}
	if _, e = f.results.Withdraw(f.ctx, f.owner, "withdraw-terminal", api.Withdraw{Basis: basis(d, s), SubmissionID: submitted.ID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("terminal modified", e)
	}
	reopened, e := f.controls.Reopen(f.ctx, f.owner, "reopen", basis(d, s))
	if e != nil {
		t.Fatal(e)
	}
	if reopened.Position.RoundID == round || reopened.Submission != nil || reopened.Review != nil || reopened.Execution.Writable {
		t.Fatal("old result or execution inherited")
	}
	old, e := f.deliveries.GetSubmission(f.ctx, "t", submitted.ID)
	if e != nil || old.CommitID != submitted.CommitID {
		t.Fatal("history lost", e)
	}
}
func TestCancelFencesWritesImmediately(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	old := *s.Execution
	var e error
	s, e = f.controls.Cancel(f.ctx, f.owner, "cancel", basis(d, s))
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.Status != "cancelled" || s.Execution.Writable || s.NextActor != "" {
		t.Fatal("cancel did not fence", s.Execution, s.NextActor)
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "late", api.Submit{Basis: basis(d, s), ExecutionID: old.ID, Generation: old.Generation, Body: "late"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("late write", e)
	}
	if items, _ := f.query.Inbox(f.ctx, f.agent); len(items) != 0 {
		t.Fatal("cancelled task still waits on the agent", items)
	}
	s, e = f.controls.Reopen(f.ctx, f.owner, "reopen", basis(d, s))
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.ex.Open(f.ctx, f.agent, "new-open", api.Open{Basis: basis(d, s)})
	if e != nil {
		t.Fatal(e)
	}
	if s.Execution.Generation <= old.Generation || s.Execution.ID == old.ID {
		t.Fatal("generation not advanced")
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "old-again", api.Submit{Basis: basis(d, s), ExecutionID: old.ID, Generation: old.Generation, Body: "old"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old generation wrote into the new round", e)
	}
}
func TestConcurrentClaimAndUnknownLeaseRecovery(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.read(t, d.Task.ID)
	var e error
	s, e = f.ex.Assign(f.ctx, f.owner, "assign", api.Assignment{Basis: basis(d, s), Subject: "agent"})
	if e != nil {
		t.Fatal(e)
	}
	in := api.Open{Basis: basis(d, s)}
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
	if _, e = f.ex.Open(f.ctx, f.agent, "blind-retry", api.Open{Basis: basis(d, s)}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("blind retry", e)
	}
}

// Approval completes a task, so a replacement candidate can only arrive while a
// review is pending; it replaces that review and withdrawing never restores it.
func TestNewCandidateReplacesPendingReviewAndWithdrawDoesNotRestoreOld(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	var e error
	s, e = f.results.Submit(f.ctx, f.agent, "first", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "first"})
	if e != nil {
		t.Fatal(e)
	}
	old := *s.Submission
	s, e = f.results.Submit(f.ctx, f.agent, "second", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "second"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Review == nil || s.Review.Outcome != "pending" || s.Review.SubmissionID == old.ID {
		t.Fatal("old review inherited")
	}
	if _, e = f.results.Review(f.ctx, f.owner, "stale-review", api.Review{Basis: basis(d, s), Verdict: acceptance.Verdict{SubmissionID: old.ID, CommitID: old.CommitID, Outcome: "approved"}}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old review click", e)
	}
	s, e = f.results.Withdraw(f.ctx, f.owner, "withdraw", api.Withdraw{Basis: basis(d, s), SubmissionID: s.Submission.ID})
	if e != nil {
		t.Fatal(e)
	}
	if s.Submission != nil || s.Review != nil {
		t.Fatal("old candidate restored")
	}
}
func TestFlowWithoutAcceptanceCompletesOnSubmission(t *testing.T) {
	f := setup(t, map[string]any{"flow-standard": map[string]any{"deps": map[string]any{"states": []string{"ready", "executing", "completed"}}}})
	d := f.create(t)
	s := f.open(t, d)
	var e error
	s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "auto result"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Review != nil || s.Position.Status != "completed" {
		t.Fatal("auto flow did not complete on submission", s.Position)
	}
}

func TestSameRoundRetryFencesOldGenerationAndReturnAdoptsBasis(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	old := *s.Execution
	oldRound := s.Position.RoundID
	// A restarted agent on a new instance claims again; the old generation is fenced.
	other, e := f.security.Issue(f.ctx, f.owner, "other-instance", capsecurity.IssueInput{Tenant: "t", Subject: "agent", InstanceID: "machine-2", Incarnation: "boot-2", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	s, e = f.ex.Open(f.ctx, other.Secret, "retry", api.Open{Basis: basis(d, s)})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.RoundID != oldRound || s.Execution.Generation <= old.Generation {
		t.Fatal("retry switched round or reused generation")
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "old-result", api.Submit{Basis: basis(d, s), ExecutionID: old.ID, Generation: old.Generation, Body: "late old"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old execution wrote", e)
	}
	s = f.read(t, d.Task.ID)
	s, e = f.controls.Return(f.ctx, f.owner, "return", basis(d, s))
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.RoundID == oldRound || s.Submission != nil {
		t.Fatal("return did not isolate round")
	}
	edited, e := f.tasks.Edit(f.ctx, f.owner, "adopt-basis", workspace.Edit{TaskID: d.Task.ID, Title: "new goal", Goal: "new", Criteria: "new", ExpectedVersion: d.Task.Version})
	if e != nil {
		t.Fatal("new round could not adopt basis", e)
	}
	if edited.Task.Owner != d.Task.Owner || edited.Task.BasisVersion != 2 {
		t.Fatal("owner changed or basis not versioned")
	}
}
