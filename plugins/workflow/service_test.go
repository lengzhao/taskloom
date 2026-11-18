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
	graph                 *build.Result
	ctx                   context.Context
	tasks                 workspace.Commands
	taskQueries           workspace.Queries
	ex                    api.ExecutionCommands
	results               api.ResultCommands
	controls              api.ControlCommands
	query                 api.Queries
	security              *security.Service
	owner, agent, runtime string
	tx                    unitofwork.Manager
	exStore               execution.Store
	commits               commit.Log
	deliveries            delivery.Reader
	reviews               acceptance.Reader
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
	for _, purpose := range []string{"business", "runtime"} {
		issued, e = f.security.Issue(ctx, f.owner, purpose, security.IssueInput{Tenant: "t", Subject: "agent", InstanceID: "machine", Incarnation: "boot", Purpose: purpose, ExpiresAt: time.Now().Add(time.Hour)})
		if e != nil {
			t.Fatal(e)
		}
		if purpose == "business" {
			f.agent = issued.Secret
		} else {
			f.runtime = issued.Secret
		}
	}
	return f
}
func basis(d workspace.Detail, s api.Snapshot) api.Basis {
	return api.Basis{TaskID: d.Task.ID, RoundID: s.Position.RoundID, BasisVersion: d.Task.BasisVersion, ExpectedVersion: s.Position.Revision}
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
func proof(s api.Snapshot) api.StopEvidence {
	v := s.Execution
	challenge := v.EndEvidenceID
	if v.StopRequestID != "" {
		challenge = v.StopRequestID
	}
	return api.StopEvidence{ExecutionRef: api.ExecutionRef{TaskID: v.TaskID, ExecutionID: v.ID, Generation: v.Generation}, Challenge: challenge, MainStopped: true, ChildrenStopped: true, ExternalSettled: true}
}
func TestResultApprovalWaitsForTrustedFullStopProof(t *testing.T) {
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
	if s.Position.Status != "active" || s.Position.Waiting == "" {
		t.Fatal("approved before stop", s.Position)
	}
	if _, e = f.ex.ProveStopped(f.ctx, f.agent, "forged-proof", proof(s)); e == nil {
		t.Fatal("business credential proved stop")
	}
	partial := proof(s)
	partial.ChildrenStopped = false
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "partial", partial); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	if s.Execution.Safe || s.Position.Status == "completed" {
		t.Fatal("partial proof released safety")
	}
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "proof", proof(s)); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	if s.Position.Status != "completed" || s.Position.RoundID != round {
		t.Fatalf("not completed %+v", s)
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
	if reopened.Position.RoundID == round || reopened.Submission != nil || reopened.Review != nil {
		t.Fatal("old result inherited")
	}
	old, e := f.deliveries.GetSubmission(f.ctx, "t", submitted.ID)
	if e != nil || old.CommitID != submitted.CommitID {
		t.Fatal("history lost", e)
	}
}
func TestCancelFencesWritesAndOldProofNeverReopens(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	old := *s.Execution
	var e error
	s, e = f.controls.Cancel(f.ctx, f.owner, "cancel", basis(d, s))
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.Status != "cancelled" || s.Execution.Writable || s.Execution.StopRequestID == "" {
		t.Fatal("cancel did not fence")
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "late", api.Submit{Basis: basis(d, s), ExecutionID: old.ID, Generation: old.Generation, Body: "late"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("late write", e)
	}
	if _, e = f.controls.Reopen(f.ctx, f.owner, "unsafe-reopen", basis(d, s)); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("unsafe reopen", e)
	}
	wrong := proof(s)
	wrong.Challenge = old.EndEvidenceID
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "wrong-challenge", wrong); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("wrong proof accepted", e)
	}
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "stop-proof", proof(s)); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	if s.Position.Status != "cancelled" {
		t.Fatal("proof reopened task")
	}
	s, e = f.controls.Reopen(f.ctx, f.owner, "safe-reopen", basis(d, s))
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
	before := s.Position.Revision
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "old-again", wrong); e == nil {
		t.Fatal("mismatched old challenge")
	}
	after := f.read(t, d.Task.ID)
	if after.Position.Revision != before {
		t.Fatal("old proof changed new position")
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
	s = f.read(t, d.Task.ID)
	v := *s.Execution
	if e = f.tx.Do(f.ctx, func(tx context.Context) error {
		v.LeaseUntil = time.Now().Add(-time.Second)
		return f.exStore.SaveExecution(tx, v)
	}); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	if s.Execution.Status != "unknown" {
		t.Fatal("expiry not unknown")
	}
	if _, e = f.ex.Open(f.ctx, f.agent, "blind-retry", api.Open{Basis: basis(d, s)}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("unknown blind retry", e)
	}
	if _, e = f.ex.Renew(f.ctx, f.agent, "renew", api.ExecutionRef{TaskID: d.Task.ID, ExecutionID: v.ID, Generation: v.Generation}); e != nil {
		t.Fatal(e)
	}
}
func TestNewCandidateInvalidatesApprovalAndWithdrawDoesNotRestoreOld(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	var e error
	s, e = f.results.Submit(f.ctx, f.agent, "first", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "first"})
	if e != nil {
		t.Fatal(e)
	}
	old := *s.Submission
	s, e = f.results.Review(f.ctx, f.owner, "approve-old", api.Review{Basis: basis(d, s), Verdict: acceptance.Verdict{SubmissionID: old.ID, CommitID: old.CommitID, Outcome: "approved"}})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.results.Submit(f.ctx, f.agent, "second", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "second"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Review == nil || s.Review.Outcome != "pending" || s.Review.SubmissionID == old.ID {
		t.Fatal("old approval inherited")
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
func TestFlowWithoutAcceptanceCompletesOnlyAfterSafety(t *testing.T) {
	f := setup(t, map[string]any{"flow-standard": map[string]any{"deps": map[string]any{"states": []string{"ready", "executing", "completed"}}}})
	d := f.create(t)
	s := f.open(t, d)
	var e error
	s, e = f.results.Submit(f.ctx, f.agent, "submit", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "auto result"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Review != nil || s.Position.Status == "completed" {
		t.Fatal("auto flow skipped safety")
	}
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "proof", proof(s)); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	if s.Position.Status != "completed" || s.Review != nil {
		t.Fatal("auto flow failed")
	}
}

func TestSameRoundRetryFencesOldGenerationAndReturnAdoptsBasis(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	old := *s.Execution
	oldRound := s.Position.RoundID
	if _, e := f.ex.ProveStopped(f.ctx, f.runtime, "proof-old", proof(s)); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	var e error
	s, e = f.ex.Open(f.ctx, f.agent, "retry", api.Open{Basis: basis(d, s)})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.RoundID != oldRound || s.Execution.Generation <= old.Generation {
		t.Fatal("retry switched round or reused generation")
	}
	if _, e = f.results.Submit(f.ctx, f.agent, "old-result", api.Submit{Basis: basis(d, s), ExecutionID: old.ID, Generation: old.Generation, Body: "late old"}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("old execution wrote", e)
	}
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "proof-retry", proof(s)); e != nil {
		t.Fatal(e)
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
func TestAdministrativeIsolationIsExplicitAndAllowsRecovery(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	var e error
	s, e = f.controls.Cancel(f.ctx, f.owner, "cancel", basis(d, s))
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.controls.Isolate(f.ctx, f.owner, "isolate", api.Isolation{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Reason: "isolated disposable test runtime", Scope: "test execution and every owned external operation"})
	if e != nil {
		t.Fatal(e)
	}
	if !s.Execution.Safe || s.Execution.Status != "isolated" || s.Execution.Writable {
		t.Fatal("isolation masqueraded as live execution")
	}
	s, e = f.controls.Reopen(f.ctx, f.owner, "reopen", basis(d, s))
	if e != nil || s.Position.Status != "active" {
		t.Fatal(e)
	}
}
func TestEndedExecutionCanSubmitButDoesNotProveSafety(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	var e error
	s, e = f.ex.End(f.ctx, f.agent, "end", api.ExecutionRef{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation})
	if e != nil {
		t.Fatal(e)
	}
	if s.Execution.Safe {
		t.Fatal("end implicitly proved stop")
	}
	s, e = f.results.Submit(f.ctx, f.agent, "late-submit", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "result after end"})
	if e != nil || s.Submission == nil {
		t.Fatal(e)
	}
}

func TestLeaseRenewalKeepsRevisionAndPendingReviewValid(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	s, e := f.results.Submit(f.ctx, f.agent, "submit", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "result"})
	if e != nil {
		t.Fatal(e)
	}
	pending := basis(d, s)
	submitted := *s.Submission
	ref := api.ExecutionRef{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation}
	feed, _ := build.GetByID[workspace.ChangeFeed](f.graph, "workspace")
	start, e := feed.Changes(f.ctx, f.owner, "")
	if e != nil {
		t.Fatal(e)
	}
	for _, key := range []string{"renew-1", "renew-2", "renew-3"} {
		renewed, e := f.ex.Renew(f.ctx, f.agent, key, ref)
		if e != nil {
			t.Fatal(e)
		}
		if renewed.Position.Revision != pending.ExpectedVersion {
			t.Fatalf("heartbeat changed flow revision %d -> %d", pending.ExpectedVersion, renewed.Position.Revision)
		}
	}
	changes, e := feed.Changes(f.ctx, f.owner, start.Cursor)
	if e != nil {
		t.Fatal(e)
	}
	if len(changes.Changes) != 0 {
		t.Fatalf("heartbeat emitted task changes %+v", changes.Changes)
	}
	if _, e = f.results.Review(f.ctx, f.owner, "approve", api.Review{Basis: pending, Verdict: acceptance.Verdict{SubmissionID: submitted.ID, CommitID: submitted.CommitID, Outcome: "approved"}}); e != nil {
		t.Fatal("review with pre-heartbeat version rejected", e)
	}
}
