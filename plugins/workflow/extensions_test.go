package workflow_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/acceptance"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/record"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/domain/security"
	"github.com/lengzhao/taskloom/runtime/config"
	"gopkg.in/yaml.v3"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func checkpointConfig(t *testing.T) map[string]any {
	t.Helper()
	b, e := os.ReadFile("../../examples/flows/with-checkpoint.config.yaml")
	if e != nil {
		t.Fatal(e)
	}
	var config map[string]any
	if e = yaml.Unmarshal(b, &config); e != nil {
		t.Fatal(e)
	}
	return config
}
func TestNewStatesAndAPIComposeWithoutChangingOriginalConsumers(t *testing.T) {
	override := checkpointConfig(t)
	override["flow-checkpoint"].(map[string]any)["deps"].(map[string]any)["states"] = []string{"ready", "executing", "factcheck", "checkpoint", "accepting", "completed"}
	override["factcheck"] = map[string]any{"use": "state/factcheck", "config": map[string]any{"requiredText": "verified"}, "deps": map[string]any{"results": "sql", "commits": "sql"}}
	f := setup(t, override)
	d := f.create(t)
	s := f.open(t, d)
	var e error
	s, e = f.results.Submit(f.ctx, f.agent, "checked-result", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "verified first"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.State != "checkpoint" || s.Review != nil {
		t.Fatal("future review entered early")
	}
	module, _ := build.GetByID[httpapi.Module](f.graph, "checkpoint-api")
	mux := http.NewServeMux()
	for _, r := range module.Routes() {
		mux.Handle(r.Method+" /v1"+r.Path, r.Handler)
	}
	confirm := func(key string, current api.Snapshot, submissionID, commitID string) int {
		t.Helper()
		in := map[string]any{"RoundID": current.Position.RoundID, "BasisVersion": d.Task.BasisVersion, "ExpectedVersion": current.Position.Revision, "SubmissionID": submissionID, "CommitID": commitID, "Note": "external input"}
		b, _ := json.Marshal(in)
		r := httptest.NewRequest("POST", "/v1/tasks/"+d.Task.ID+"/checkpoint", bytes.NewReader(b))
		r.Header.Set("Authorization", "Bearer "+f.owner)
		r.Header.Set("Idempotency-Key", key)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	first := *s.Submission
	if code := confirm("confirm-first", s, first.ID, first.CommitID); code != 200 {
		t.Fatal("checkpoint API", code)
	}
	s = f.read(t, d.Task.ID)
	if s.Position.State != "accepting" || s.Review == nil {
		t.Fatal("review not prepared")
	}
	s, e = f.results.Review(f.ctx, f.owner, "approve-first", api.Review{Basis: basis(d, s), Verdict: acceptance.Verdict{SubmissionID: first.ID, CommitID: first.CommitID, Outcome: "approved"}})
	if e != nil {
		t.Fatal(e)
	}
	s, e = f.results.Submit(f.ctx, f.agent, "bad-candidate", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "bad"})
	if e != nil {
		t.Fatal(e)
	}
	if s.Position.State != "accepting" || !strings.Contains(s.Position.Waiting, "factcheck") || s.Submission == nil || s.Review == nil || s.Review.Outcome != "pending" {
		t.Fatalf("prefix not rechecked %+v", s)
	}
	if code := confirm("stale-input", s, first.ID, first.CommitID); code != 409 {
		t.Fatal("old external input accepted", code)
	}
	s, e = f.results.Submit(f.ctx, f.agent, "third", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "verified third"})
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(s.Position.Waiting, "checkpoint") {
		t.Fatal("new evidence reused confirmation")
	}
	if code := confirm("confirm-third", s, s.Submission.ID, s.Submission.CommitID); code != 200 {
		t.Fatal(code)
	}
	s = f.read(t, d.Task.ID)
	s, e = f.results.Review(f.ctx, f.owner, "approve-third", api.Review{Basis: basis(d, s), Verdict: acceptance.Verdict{SubmissionID: s.Submission.ID, CommitID: s.Submission.CommitID, Outcome: "approved"}})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.ProveStopped(f.ctx, f.runtime, "proof", proof(s)); e != nil {
		t.Fatal(e)
	}
	s = f.read(t, d.Task.ID)
	if s.Position.Status != "completed" {
		t.Fatal("extension flow not completed")
	}
}

type failureState struct{}

func (failureState) Describe() flow.Description {
	return flow.Description{Key: "technical-failure", Version: "1"}
}
func (failureState) CheckEnter(context.Context, flow.Context) (flow.Check, error) {
	return flow.Check{Status: flow.Allow}, nil
}
func (failureState) Enter(context.Context, flow.Context) error {
	return errors.New("injected local storage failure")
}
func (failureState) CheckComplete(context.Context, flow.Context) (flow.Check, error) {
	return flow.Check{Status: flow.Allow}, nil
}

type collisionAPI struct{}

func (collisionAPI) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "POST", Path: "/tasks/{id}/review", Operation: "test.collision", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}}
}
func init() {
	pluginkit.Register("test/failure-state", func() (*failureState, error) { return &failureState{}, nil })
	pluginkit.Register("test/collision-api", func() (*collisionAPI, error) { return &collisionAPI{}, nil })
}
func TestSubmitTechnicalFailureRollsBackCommitAndDelivery(t *testing.T) {
	f := setup(t, map[string]any{"failure": map[string]any{"use": "test/failure-state"}, "flow-standard": map[string]any{"deps": map[string]any{"states": []string{"ready", "executing", "failure", "accepting", "completed"}}}})
	d := f.create(t)
	s := f.open(t, d)
	version := s.Position.Revision
	if _, e := f.results.Submit(f.ctx, f.agent, "failure", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "must rollback"}); e == nil {
		t.Fatal("technical failure accepted")
	}
	s = f.read(t, d.Task.ID)
	if s.Submission != nil || s.Review != nil || s.Position.State != "executing" || s.Position.Revision != version {
		t.Fatal("facts leaked")
	}
	cs, e := f.commits.List(f.ctx, "t", d.Task.ID, 0, 100)
	if e != nil || len(cs) != 1 {
		t.Fatal("commit leaked", len(cs), e)
	}
}
func TestNewModuleRouteConflictRejectedBeforeStart(t *testing.T) {
	dir := t.TempDir()
	p := dir + "/config.yaml"
	b, _ := json.Marshal(map[string]any{"collision": map[string]any{"use": "test/collision-api"}, "http": map[string]any{"deps": map[string]any{"modules": []string{"health", "workspace-api", "workflow-api", "collision"}}}})
	if e := os.WriteFile(p, b, 0600); e != nil {
		t.Fatal(e)
	}
	if _, _, e := config.BuildFiles(context.Background(), "../../base.yaml", p); e == nil {
		t.Fatal("route collision accepted")
	}
}
func TestIdenticalResultDifferentKeyDoesNotDuplicateCandidate(t *testing.T) {
	f := setup(t, nil)
	d := f.create(t)
	s := f.open(t, d)
	in := api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "same result"}
	first, e := f.results.Submit(f.ctx, f.agent, "first", in)
	if e != nil {
		t.Fatal(e)
	}
	in.Basis = basis(d, first)
	second, e := f.results.Submit(f.ctx, f.agent, "second", in)
	if e != nil {
		t.Fatal(e)
	}
	if second.Submission.ID != first.Submission.ID {
		t.Fatal("duplicate candidate")
	}
	cs, e := f.commits.List(f.ctx, "t", d.Task.ID, 0, 100)
	if e != nil || len(cs) != 2 {
		t.Fatal("duplicate result body", len(cs), e)
	}
}
func TestNoAcceptanceVerdictAndWrongInstanceRejected(t *testing.T) {
	f := setup(t, map[string]any{"flow-standard": map[string]any{"deps": map[string]any{"states": []string{"ready", "executing", "completed"}}}})
	d := f.create(t)
	s := f.open(t, d)
	issued, e := f.security.Issue(f.ctx, f.owner, "other-instance", security.IssueInput{Tenant: "t", Subject: "agent", InstanceID: "other-machine", Incarnation: "boot", Purpose: "business", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.ex.End(f.ctx, issued.Secret, "wrong-instance", api.ExecutionRef{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Generation: s.Execution.Generation}); e == nil {
		t.Fatal("wrong instance ended execution")
	}
	if _, e = f.results.Review(f.ctx, f.owner, "no-state", api.Review{Basis: basis(d, s)}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("no acceptance state allowed verdict", e)
	}
}

type observedFailure struct {
	events   record.EventWriter
	facts    api.FactsReader
	tasks    task.Reader
	captured api.Snapshot
}
type observedDeps struct {
	Events record.EventWriter `json:"events"`
	Facts  api.FactsReader    `json:"facts"`
	Tasks  task.Reader        `json:"tasks"`
}

func (o *observedFailure) AppendEvent(ctx context.Context, event record.Event) error {
	t, e := o.tasks.GetTask(ctx, event.Tenant, event.Resource)
	if e != nil {
		return e
	}
	o.captured, e = o.facts.ReadFacts(ctx, t)
	if e != nil {
		return e
	}
	if e = o.events.AppendEvent(ctx, event); e != nil {
		return e
	}
	return errors.New("injected post-review outbox failure")
}
func init() {
	pluginkit.Register("test/observed-outbox-failure", func(_ struct{}, d observedDeps) (*observedFailure, error) {
		return &observedFailure{events: d.Events, facts: d.Facts, tasks: d.Tasks}, nil
	})
}
func TestPostReviewFailureRollsBackEverySubmissionFact(t *testing.T) {
	f := setup(t, map[string]any{"observed": map[string]any{"use": "test/observed-outbox-failure", "deps": map[string]any{"events": "sql", "facts": "workflow-query", "tasks": "sql"}}, "result-commands": map[string]any{"deps": map[string]any{"events": "observed"}}})
	d := f.create(t)
	s := f.open(t, d)
	revision := s.Position.Revision
	_, e := f.results.Submit(f.ctx, f.agent, "post-review-failure", api.Submit{Basis: basis(d, s), ExecutionID: s.Execution.ID, Generation: s.Execution.Generation, Body: "rollback everything"})
	if e == nil {
		t.Fatal("failure accepted")
	}
	observer, _ := build.GetByID[*observedFailure](f.graph, "observed")
	captured := observer.captured
	if captured.Submission == nil || captured.Review == nil || captured.Position.State != "accepting" {
		t.Fatal("failure not injected after review creation")
	}
	if _, e = f.commits.Get(f.ctx, "t", d.Task.ID, captured.Submission.CommitID); !errors.Is(e, fault.ErrNotFound) {
		t.Fatal("commit survived", e)
	}
	if _, e = f.deliveries.GetSubmission(f.ctx, "t", captured.Submission.ID); !errors.Is(e, fault.ErrNotFound) {
		t.Fatal("submission survived", e)
	}
	if _, e = f.reviews.GetReview(f.ctx, "t", d.Task.ID, captured.Position.RoundID, captured.Review.EntryID, captured.Submission.ID); !errors.Is(e, fault.ErrNotFound) {
		t.Fatal("review survived", e)
	}
	receipts, _ := build.GetByID[record.Receipts](f.graph, "sql")
	if _, e = receipts.FindReceipt(f.ctx, record.RequestKey{Tenant: "t", Subject: "agent", Operation: "result.submit", Key: "post-review-failure"}); !errors.Is(e, fault.ErrNotFound) {
		t.Fatal("receipt survived", e)
	}
	s = f.read(t, d.Task.ID)
	if s.Position.Revision != revision || s.Position.State != "executing" {
		t.Fatal("position survived rollback")
	}
}
func TestPreparationDefinitionRetainsOldTaskBehavior(t *testing.T) {
	f := setup(t, nil)
	tasks, _ := build.GetByID[task.Store](f.graph, "sql")
	positions, _ := build.GetByID[flow.PositionStore](f.graph, "sql")
	defs, _ := build.GetByID[flow.DefinitionStore](f.graph, "sql")
	ref := flow.DefinitionRef{ID: "preparation", Version: "1", Digest: fmt.Sprintf("%x", sha256.Sum256([]byte(`{"Key":"ready","Version":"1"}`)))}
	e := f.tx.Do(f.ctx, func(ctx context.Context) error {
		if e := tasks.CreateTask(ctx, task.Task{Tenant: "t", ID: "old-task", Owner: "owner", Title: "old", Goal: "goal", Criteria: "criteria", Version: 1, BasisVersion: 1}); e != nil {
			return e
		}
		if e := defs.EnsureDefinition(ctx, ref); e != nil {
			return e
		}
		return positions.CreatePosition(ctx, flow.Position{Tenant: "t", TaskID: "old-task", RoundID: "old-round", EntryID: "old-entry", State: "ready", Flow: ref, Complete: true})
	})
	if e != nil {
		t.Fatal(e)
	}
	old, e := f.query.Read(f.ctx, f.owner, "old-task")
	if e != nil {
		t.Fatal(e)
	}
	if old.Position.Flow != ref || old.Position.State != "ready" || old.Position.RoundID != "old-round" || old.Position.Status != "active" {
		t.Fatal("old task silently upgraded")
	}
	if _, e = f.ex.Open(f.ctx, f.agent, "old-claim", api.Open{Basis: api.Basis{TaskID: "old-task", RoundID: "old-round", BasisVersion: 1}}); e == nil {
		t.Fatal("preparation-only task became executable")
	}
	current := f.create(t)
	if current.Position.Flow.ID != "standard" || current.Position.State != "executing" {
		t.Fatal("new task did not use new default")
	}
}
