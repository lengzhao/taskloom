package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/record"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"github.com/lengzhao/taskloom/cap/task"
	api "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/runtime/config"
	"net/http"
	"os"
	"testing"
	"time"
)

type collisionAPI struct{}

func (collisionAPI) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "POST", Path: "/tasks/{id}/review", Operation: "test.collision", Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})}}
}
func init() {
	pluginkit.Register("test/collision-api", func() (*collisionAPI, error) { return &collisionAPI{}, nil })
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
	in := api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "same result"}
	first, e := f.results.Submit(f.ctx, f.agent, "first", in)
	if e != nil {
		t.Fatal(e)
	}
	second, e := f.results.Submit(f.ctx, f.agent, "second", in)
	if e != nil {
		t.Fatal(e)
	}
	if second.Result.CommitID != first.Result.CommitID {
		t.Fatal("duplicate candidate")
	}
	cs, e := f.commits.List(f.ctx, "t", d.Task.ID, 0, 100)
	if e != nil || len(cs) != 2 {
		t.Fatal("duplicate result body", len(cs), e)
	}
}
func TestNoAcceptanceVerdictAndWrongInstanceRejected(t *testing.T) {
	f := setup(t, map[string]any{"flow": map[string]any{"config": map[string]any{"acceptance": "none"}}})
	d := f.create(t)
	s := f.open(t, d)
	issued, e := f.security.Issue(f.ctx, f.owner, "other-instance", capsecurity.IssueInput{Tenant: "t", Subject: "agent", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.results.Submit(f.ctx, issued.Secret, "wrong-instance", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "x"}); e == nil {
		t.Fatal("wrong instance submitted into another instance's execution")
	}
	if _, e = f.results.Review(f.ctx, f.owner, "no-state", api.Review{TaskID: d.Task.ID}); !errors.Is(e, fault.ErrConflict) {
		t.Fatal("no acceptance state allowed verdict", e)
	}
}

// factsReader is the query's in-transaction read, used to observe facts
// before an injected failure rolls them back.
type factsReader interface {
	ReadFacts(ctx context.Context, value task.Task) (api.Snapshot, error)
}
type observedFailure struct {
	events   record.EventWriter
	facts    factsReader
	tasks    task.Reader
	captured api.Snapshot
}
type observedDeps struct {
	Events record.EventWriter `json:"events"`
	Facts  factsReader        `json:"facts"`
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
func TestPostReviewFailureRollsBackEveryResultFact(t *testing.T) {
	f := setup(t, map[string]any{"observed": map[string]any{"use": "test/observed-outbox-failure", "deps": map[string]any{"events": "sql", "facts": "workflow-query", "tasks": "sql"}}, "result-commands": map[string]any{"deps": map[string]any{"events": "observed"}}})
	d := f.create(t)
	s := f.open(t, d)
	position := s.Position
	_, e := f.results.Submit(f.ctx, f.agent, "post-review-failure", api.Submit{TaskID: d.Task.ID, ExecutionID: s.Execution.ID, Body: "rollback everything"})
	if e == nil {
		t.Fatal("failure accepted")
	}
	observer, _ := build.GetByID[*observedFailure](f.graph, "observed")
	captured := observer.captured
	if captured.Result == nil || captured.Review == nil || captured.Position.State != "accepting" {
		t.Fatal("failure not injected after review creation")
	}
	if _, e = f.commits.Get(f.ctx, "t", d.Task.ID, captured.Result.CommitID); !errors.Is(e, fault.ErrNotFound) {
		t.Fatal("commit survived", e)
	}
	if cur, e := f.deliveries.CurrentResult(f.ctx, "t", d.Task.ID, captured.Position.RoundID); e == nil && cur.CommitID == captured.Result.CommitID {
		t.Fatal("result registration survived")
	}
	if _, e = f.reviews.LatestReview(f.ctx, "t", d.Task.ID, captured.Result.CommitID); !errors.Is(e, fault.ErrNotFound) {
		t.Fatal("review survived", e)
	}
	receipts, _ := build.GetByID[record.Receipts](f.graph, "sql")
	if _, e = receipts.FindReceipt(f.ctx, record.RequestKey{Tenant: "t", Subject: "agent", Operation: "result.submit", Key: "post-review-failure"}); !errors.Is(e, fault.ErrNotFound) {
		t.Fatal("receipt survived", e)
	}
	s = f.read(t, d.Task.ID)
	if s.Position != position {
		t.Fatal("position survived rollback")
	}
}
