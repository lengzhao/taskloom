package workflow_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"github.com/lengzhao/taskloom/cap/unitofwork"
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/plugins/security"
	"github.com/lengzhao/taskloom/runtime/config"
)

type problem struct{ Code, Message, Field string }

// view is the part of the task view these tests read.
type view struct {
	Task struct {
		ID, Title, Owner string
		BasisVersion     int64
	}
	Position struct {
		Status, State, Waiting, RoundID string
		WaitingOnSubtasks               bool
	}
	NextActor, Assignee string
	Execution           *struct {
		ID, RoundID string
		Writable    bool
	}
	Result    *struct{ CommitID string }
	Review    *struct{ Outcome string }
	Questions []struct{ ID, Addressee string }
	Parent    *struct {
		TaskID   string
		Required bool
	}
	CanOpen bool
}

// fixture serves the real default graph over HTTP. In tenant "t": owner (an
// administrator), bob (a member) and agent (with a business credential). In
// tenant "other": a foreign administrator and Agent that reuse the same
// subject IDs, so isolation cannot rest on subject names.
type fixture struct {
	t                          *testing.T
	srv                        *httptest.Server
	owner, bob, agent          string
	foreignOwner, foreignAgent string
	seq                        int
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.yaml")
	raw, _ := json.Marshal(map[string]any{
		"sql":   map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}},
		"blobs": map[string]any{"config": map[string]any{"dir": filepath.Join(dir, "files")}},
	})
	if e := os.WriteFile(overlay, raw, 0600); e != nil {
		t.Fatal(e)
	}
	_, graph, e := config.BuildFiles(ctx, "../../../base.yaml", overlay)
	if e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"sql", "blobs"} {
		r, _ := build.GetByID[lifecycle.Resource](graph, id)
		if e = r.Open(ctx); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { r.Close(ctx) })
	}
	mux, _ := build.GetByID[http.Handler](graph, "http")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sec, _ := build.GetByID[*security.Service](graph, "security")
	f := &fixture{t: t, srv: srv}
	if e = sec.Bootstrap(ctx, security.AdminInput{Tenant: "t", Subject: "owner", Operator: "test", Reason: "fixture", Password: "AdminPassword123!"}); e != nil {
		t.Fatal(e)
	}
	f.owner = login(t, sec, "t", "owner", "AdminPassword123!")
	if e = sec.CreateUser(ctx, f.owner, "user-bob", capsecurity.UserInput{Tenant: "t", Username: "bob", Password: "BobPassword123!"}); e != nil {
		t.Fatal(e)
	}
	f.bob = login(t, sec, "t", "bob", "BobPassword123!")
	f.agent = agent(t, sec, f.owner, "t")

	// A second tenant with its own administrator and Agent, same subject IDs.
	principals, _ := build.GetByID[identity.Principals](graph, "sql")
	tx, _ := build.GetByID[unitofwork.Manager](graph, "sql")
	if e = tx.Do(ctx, func(ctx context.Context) error {
		return principals.CreatePrincipal(ctx, identity.Principal{Tenant: "other", ID: "owner", Kind: "human"})
	}); e != nil {
		t.Fatal(e)
	}
	if e = sec.Recover(ctx, security.AdminInput{Tenant: "other", Subject: "owner", Operator: "test", Reason: "isolation", Password: "ForeignPassword123!"}); e != nil {
		t.Fatal(e)
	}
	f.foreignOwner = login(t, sec, "other", "owner", "ForeignPassword123!")
	f.foreignAgent = agent(t, sec, f.foreignOwner, "other")
	return f
}

func login(t *testing.T, sec *security.Service, tenant, user, password string) string {
	t.Helper()
	issued, e := sec.Login(context.Background(), capsecurity.LoginInput{Tenant: tenant, Username: user, Password: password})
	if e != nil {
		t.Fatal(e)
	}
	return issued.Secret
}

// agent creates the tenant's Agent "agent", owned by "owner", and issues its business credential.
func agent(t *testing.T, sec *security.Service, admin, tenant string) string {
	t.Helper()
	ctx := context.Background()
	if e := sec.Create(ctx, admin, "agent-"+tenant, identity.Principal{Tenant: tenant, ID: "agent", Kind: "agent", OwnerID: "owner"}); e != nil {
		t.Fatal(e)
	}
	issued, e := sec.Issue(ctx, admin, "issue-"+tenant, capsecurity.IssueInput{Tenant: tenant, Subject: "agent", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	return issued.Secret
}

// call sends one request with a fresh idempotency key and returns status and body.
func (f *fixture) call(token, method, path string, body any) (int, []byte) {
	f.t.Helper()
	f.seq++
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, f.srv.URL+"/v1"+path, r)
	req.Header.Set("Authorization", "Bearer "+token)
	if method != "GET" {
		req.Header.Set("Idempotency-Key", fmt.Sprintf("k%d", f.seq))
	}
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		f.t.Fatal(e)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// ok requires 200 and decodes the task view.
func (f *fixture) ok(token, method, path string, body any) view {
	f.t.Helper()
	status, raw := f.call(token, method, path, body)
	var v view
	if status != 200 || json.Unmarshal(raw, &v) != nil {
		f.t.Fatalf("%s %s: %d %s", method, path, status, raw)
	}
	return v
}

// fail requires the status and the stable error code.
func (f *fixture) fail(token, method, path string, body any, status int, code string) []byte {
	f.t.Helper()
	got, raw := f.call(token, method, path, body)
	var p problem
	if json.Unmarshal(raw, &p) != nil || got != status || p.Code != code || p.Message == "" {
		f.t.Fatalf("%s %s: got %d %s, want %d %s", method, path, got, raw, status, code)
	}
	return raw
}

func (f *fixture) create(title string, extra map[string]any) view {
	f.t.Helper()
	body := map[string]any{"Title": title, "Goal": "goal of " + title}
	for k, v := range extra {
		body[k] = v
	}
	return f.ok(f.owner, "POST", "/tasks", body)
}

// claim assigns the task to the agent and lets it start work.
func (f *fixture) claim(v view) view {
	f.t.Helper()
	f.ok(f.owner, "POST", "/tasks/"+v.Task.ID+"/assign", map[string]any{"Subject": "agent"})
	got := f.ok(f.agent, "POST", "/tasks/"+v.Task.ID+"/start", map[string]any{"BasisVersion": v.Task.BasisVersion})
	if got.Execution == nil || !got.Execution.Writable {
		f.t.Fatalf("claim %+v", got)
	}
	return got
}

// Cancelling fences the running execution; restart pins the round the caller
// saw and opens a new one that can be claimed again.
func TestCancelRestartAndNewRound(t *testing.T) {
	f := setup(t)
	v := f.claim(f.create("取消重开", nil))
	id, old, round1 := v.Task.ID, v.Execution.ID, v.Position.RoundID

	// Only the owner or an administrator cancels.
	f.fail(f.bob, "POST", "/tasks/"+id+"/cancel", map[string]any{}, 403, "forbidden")
	f.fail(f.agent, "POST", "/tasks/"+id+"/cancel", map[string]any{}, 403, "forbidden")
	// Restart is only for cancelled tasks.
	f.fail(f.owner, "POST", "/tasks/"+id+"/restart", map[string]any{"RoundID": round1}, 409, "not_cancelled")

	v = f.ok(f.owner, "POST", "/tasks/"+id+"/cancel", map[string]any{})
	if v.Position.Status != "cancelled" || v.Position.Waiting != "任务已取消" || v.NextActor != "" || v.Execution == nil || v.Execution.Writable {
		t.Fatalf("cancelled view %+v", v)
	}
	// The old execution can no longer deliver; a second cancel conflicts.
	f.fail(f.agent, "POST", "/tasks/"+id+"/results", map[string]any{"ExecutionID": old, "Body": "late"}, 409, "task_not_active")
	f.fail(f.owner, "POST", "/tasks/"+id+"/cancel", map[string]any{}, 409, "task_not_active")
	f.fail(f.agent, "POST", "/tasks/"+id+"/start", map[string]any{"BasisVersion": v.Task.BasisVersion}, 409, "task_not_active")
	// Only the owner or an administrator restarts.
	f.fail(f.bob, "POST", "/tasks/"+id+"/restart", map[string]any{"RoundID": round1}, 403, "forbidden")

	v = f.ok(f.owner, "POST", "/tasks/"+id+"/restart", map[string]any{"RoundID": round1})
	round2 := v.Position.RoundID
	if v.Position.Status != "active" || round2 == "" || round2 == round1 {
		t.Fatalf("restart %+v", v.Position)
	}
	// The execution of the old round stays fenced in the new round.
	f.fail(f.agent, "POST", "/tasks/"+id+"/results", map[string]any{"ExecutionID": old, "Body": "late"}, 409, "execution_fenced")

	// A restart that pins an earlier round conflicts and changes nothing.
	f.ok(f.owner, "POST", "/tasks/"+id+"/cancel", map[string]any{})
	f.fail(f.owner, "POST", "/tasks/"+id+"/restart", map[string]any{"RoundID": round1}, 409, "round_changed")
	if now := f.ok(f.owner, "GET", "/tasks/"+id, nil); now.Position.Status != "cancelled" || now.Position.RoundID != round2 {
		t.Fatalf("stale restart changed the task %+v", now.Position)
	}
	v = f.ok(f.owner, "POST", "/tasks/"+id+"/restart", map[string]any{"RoundID": round2})
	round3 := v.Position.RoundID
	if v.Position.Status != "active" || round3 == round2 || round3 == round1 {
		t.Fatalf("second restart %+v", v.Position)
	}

	// The assignment survives; the agent claims the new round and delivers.
	if v.Assignee != "agent" {
		t.Fatalf("assignee after restart %+v", v)
	}
	v = f.ok(f.agent, "POST", "/tasks/"+id+"/start", map[string]any{"BasisVersion": v.Task.BasisVersion})
	if v.Execution == nil || v.Execution.ID == old || v.Execution.RoundID != round3 || !v.Execution.Writable {
		t.Fatalf("claim in new round %+v", v.Execution)
	}
	v = f.ok(f.agent, "POST", "/tasks/"+id+"/results", map[string]any{"ExecutionID": v.Execution.ID, "Body": "done"})
	if v.Position.State != "accepting" || v.Position.RoundID != round3 || v.NextActor != "reviewer" {
		t.Fatalf("delivery in new round %+v", v)
	}
}

// Handing over ownership pins the owner the caller saw; only the owner or an
// administrator hands over, and only to an active person.
func TestOwnerHandover(t *testing.T) {
	f := setup(t)
	v := f.create("移交", nil)
	id := v.Task.ID
	path := "/tasks/" + id + "/owner"

	f.fail(f.bob, "POST", path, map[string]any{"Owner": "bob", "SeenOwner": "owner"}, 403, "forbidden")
	f.fail(f.agent, "POST", path, map[string]any{"Owner": "bob", "SeenOwner": "owner"}, 403, "forbidden")
	f.fail(f.owner, "POST", path, map[string]any{"Owner": "agent", "SeenOwner": "owner"}, 400, "invalid_input")
	f.fail(f.owner, "POST", path, map[string]any{"Owner": "bob"}, 400, "invalid_input")

	v = f.ok(f.owner, "POST", path, map[string]any{"Owner": "bob", "SeenOwner": "owner", "Reason": "换人负责"})
	if v.Task.Owner != "bob" {
		t.Fatalf("handover %+v", v.Task)
	}
	// A handover from a stale read of the owner conflicts.
	f.fail(f.owner, "POST", path, map[string]any{"Owner": "owner", "SeenOwner": "owner"}, 409, "owner_changed")
	// The new owner now reviews and manages the task.
	if got := f.ok(f.bob, "GET", "/tasks/"+id, nil); got.Task.Owner != "bob" {
		t.Fatalf("new owner read %+v", got.Task)
	}
	f.ok(f.bob, "POST", path, map[string]any{"Owner": "owner", "SeenOwner": "bob"})
}

// Placing a task in the work breakdown pins the relation the caller saw; the
// child must be newer than its parent and the caller must plan both. A
// required subtask then holds the parent, and an open question still names
// whom the parent waits on.
func TestParentPlacement(t *testing.T) {
	f := setup(t)
	parent := f.create("父任务", nil)
	child := f.create("子任务", nil)
	path := "/tasks/" + child.Task.ID + "/parent"

	f.fail(f.bob, "POST", path, map[string]any{"ParentID": parent.Task.ID, "Required": true}, 403, "forbidden")
	f.fail(f.agent, "POST", path, map[string]any{"ParentID": parent.Task.ID, "Required": true}, 403, "forbidden")
	// The parent cannot sit under its newer child.
	f.fail(f.owner, "POST", "/tasks/"+parent.Task.ID+"/parent", map[string]any{"ParentID": child.Task.ID}, 409, "parent_newer")

	v := f.ok(f.owner, "POST", path, map[string]any{"ParentID": parent.Task.ID, "Required": true})
	if v.Parent == nil || v.Parent.TaskID != parent.Task.ID || !v.Parent.Required {
		t.Fatalf("placed child %+v", v.Parent)
	}
	// Placing again from a read that showed a root task conflicts.
	f.fail(f.owner, "POST", path, map[string]any{"ParentID": parent.Task.ID, "Required": false}, 409, "parent_changed")

	// The required subtask holds the parent; nobody on the parent is waited on.
	p := f.ok(f.owner, "GET", "/tasks/"+parent.Task.ID, nil)
	if !p.Position.WaitingOnSubtasks || p.Position.Waiting != "等待必需子任务完成" || p.NextActor != "" {
		t.Fatalf("parent waits on subtask %+v %q", p.Position, p.NextActor)
	}
	// The agent works on the parent and asks the owner; Waiting still names the
	// subtask while NextActor names the addressee (docs/api.md NextActor).
	p = f.claim(p)
	f.fail(f.agent, "POST", "/tasks/"+parent.Task.ID+"/results", map[string]any{"ExecutionID": p.Execution.ID, "Body": "early"}, 409, "required_children_pending")
	p = f.ok(f.agent, "POST", "/tasks/"+parent.Task.ID+"/questions", map[string]any{"RoundID": p.Position.RoundID, "Body": "范围包括哪些？"})
	if len(p.Questions) != 1 || p.Questions[0].Addressee != "owner" || p.NextActor != "addressee" || !p.Position.WaitingOnSubtasks || p.Position.Waiting != "等待必需子任务完成" {
		t.Fatalf("subtask and question %+v %q %+v", p.Position, p.NextActor, p.Questions)
	}
	p = f.ok(f.owner, "POST", "/tasks/"+parent.Task.ID+"/questions/"+p.Questions[0].ID+"/answer", map[string]any{"Body": "只含本周"})
	if len(p.Questions) != 0 || p.NextActor != "" || !p.Position.WaitingOnSubtasks {
		t.Fatalf("after answer %+v %q", p.Position, p.NextActor)
	}
	// Making the subtask optional releases the parent to its own executor.
	v = f.ok(f.owner, "POST", path, map[string]any{"ParentID": parent.Task.ID, "Required": false, "SeenParentID": parent.Task.ID, "SeenRequired": true})
	if v.Parent == nil || v.Parent.Required {
		t.Fatalf("optional child %+v", v.Parent)
	}
	if p = f.ok(f.owner, "GET", "/tasks/"+parent.Task.ID, nil); p.Position.WaitingOnSubtasks {
		t.Fatalf("parent still waits %+v", p.Position)
	}
}

// A member or Agent of another tenant, even under the same subject IDs, can
// neither read nor work a task here, and its errors reveal nothing of it.
func TestForeignTenantCannotReachTask(t *testing.T) {
	f := setup(t)
	secret := "机密标题-xyz"
	v := f.claim(f.create(secret, map[string]any{"Criteria": "机密标准-xyz"}))
	id, exec := v.Task.ID, v.Execution.ID
	v = f.ok(f.agent, "POST", "/tasks/"+id+"/results", map[string]any{"ExecutionID": exec, "Body": "机密结果-xyz"})
	if v.Result == nil {
		t.Fatalf("result %+v", v)
	}
	commitID, round := v.Result.CommitID, v.Position.RoundID

	denied := func(token, method, path string, body any) {
		t.Helper()
		status, raw := f.call(token, method, path, body)
		t.Logf("%s %s -> %d", method, path, status)
		if status != 403 && status != 404 {
			t.Fatalf("%s %s: %d %s", method, path, status, raw)
		}
		for _, leak := range []string{"xyz", id, exec, commitID, round} {
			if strings.Contains(string(raw), leak) {
				t.Fatalf("%s %s leaks %q: %s", method, path, leak, raw)
			}
		}
	}
	for _, token := range []string{f.foreignOwner, f.foreignAgent} {
		denied(token, "GET", "/tasks/"+id, nil)
		denied(token, "POST", "/tasks/"+id+"/assign", map[string]any{"Subject": "agent"})
		denied(token, "POST", "/tasks/"+id+"/start", map[string]any{"BasisVersion": v.Task.BasisVersion})
		denied(token, "POST", "/tasks/"+id+"/results", map[string]any{"ExecutionID": exec, "Body": "foreign"})
		denied(token, "POST", "/tasks/"+id+"/review", map[string]any{"CommitID": commitID, "Outcome": "approved", "Reason": "ok"})
		denied(token, "POST", "/tasks/"+id+"/questions", map[string]any{"RoundID": round, "Body": "?"})
		denied(token, "POST", "/tasks/"+id+"/cancel", map[string]any{})
		denied(token, "POST", "/tasks/"+id+"/owner", map[string]any{"Owner": "owner", "SeenOwner": "owner"})
		// A foreign task answers exactly like a missing one, so existence does not leak.
		hidden, hiddenRaw := f.call(token, "GET", "/tasks/"+id, nil)
		missing, missingRaw := f.call(token, "GET", "/tasks/00000000000000000000000000000000", nil)
		if hidden != missing || string(hiddenRaw) != string(missingRaw) {
			t.Fatalf("foreign %d %s differs from missing %d %s", hidden, hiddenRaw, missing, missingRaw)
		}
		// The list neither shows nor hints at the task.
		status, raw := f.call(token, "GET", "/tasks?id="+id, nil)
		if status != 200 || strings.TrimSpace(string(raw)) != "[]" {
			t.Fatalf("foreign list %d %s", status, raw)
		}
		status, raw = f.call(token, "GET", "/tasks", nil)
		if status != 200 || strings.Contains(string(raw), "xyz") || strings.Contains(string(raw), id) {
			t.Fatalf("foreign full list %d %s", status, raw)
		}
	}
	// Nothing changed here: the result still waits for this owner's review.
	now := f.ok(f.owner, "GET", "/tasks/"+id, nil)
	if now.Position.Status != "active" || now.Position.State != "accepting" || now.Result == nil || now.Result.CommitID != commitID || now.Execution == nil || now.Execution.ID != exec || len(now.Questions) != 0 || now.Task.Owner != "owner" {
		t.Fatalf("foreign calls changed the task %+v", now)
	}
	f.ok(f.owner, "POST", "/tasks/"+id+"/review", map[string]any{"CommitID": commitID, "Outcome": "approved", "Reason": "符合"})
}
