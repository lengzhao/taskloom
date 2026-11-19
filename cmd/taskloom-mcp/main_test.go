package main

import (
	"bytes"
	"context"
	"encoding/json"
	capsecurity "github.com/lengzhao/taskloom/cap/security"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lengzhao/pluginkit/build"
	"github.com/lengzhao/taskloom/cap/httpapi"
	"github.com/lengzhao/taskloom/cap/identity"
	"github.com/lengzhao/taskloom/cap/lifecycle"
	"github.com/lengzhao/taskloom/domain/security"
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/runtime/config"
)

// fixture runs the real default graph behind an httptest server, with an
// owner and an agent holding its business credential.
type fixture struct {
	t            *testing.T
	ctx          context.Context
	srv          *httptest.Server
	owner, agent string
	mcp          *server
	seq          int
}

func setup(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	overlay := filepath.Join(dir, "config.yaml")
	raw, _ := json.Marshal(map[string]any{"sql": map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}}})
	if e := os.WriteFile(overlay, raw, 0600); e != nil {
		t.Fatal(e)
	}
	_, graph, e := config.BuildFiles(ctx, "../../base.yaml", overlay)
	if e != nil {
		t.Fatal(e)
	}
	storage, _ := build.GetByID[lifecycle.Resource](graph, "sql")
	if e = storage.Open(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { storage.Close(ctx) })
	mux := http.NewServeMux()
	for _, id := range []string{"workspace-api", "workflow-api"} {
		m, ok := build.GetByID[httpapi.Module](graph, id)
		if !ok {
			t.Fatal(id)
		}
		for _, r := range m.Routes() {
			mux.Handle(r.Method+" /v1"+r.Path, r.Handler)
		}
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sec, _ := build.GetByID[*security.Service](graph, "security")
	issued, e := sec.Bootstrap(ctx, security.BootstrapInput{Tenant: "t", Subject: "owner", Operator: "test", Reason: "fixture", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{t: t, ctx: ctx, srv: srv, owner: issued.Secret}
	if e = sec.Create(ctx, f.owner, "agent", identity.Principal{Tenant: "t", ID: "agent", Kind: "agent", OwnerID: "owner"}); e != nil {
		t.Fatal(e)
	}
	agent, e := sec.Issue(ctx, f.owner, "business", capsecurity.IssueInput{Tenant: "t", Subject: "agent", InstanceID: "machine", Incarnation: "boot", Purpose: "business", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	f.agent = agent.Secret
	f.mcp = newServer(newClient(srv.URL+"/v1", f.agent), 100*time.Millisecond, &bytes.Buffer{})
	t.Cleanup(f.mcp.Close)
	return f
}

// owner calls the HTTP API directly as the human owner.
func (f *fixture) ownerCall(method, path string, body, out any) {
	f.t.Helper()
	f.seq++
	c := newClient(f.srv.URL+"/v1", f.owner)
	if e := c.do(f.ctx, method, path, body, "owner-"+string(rune('a'+f.seq)), out); e != nil {
		f.t.Fatal(method, path, e)
	}
}

// rpc sends one JSON-RPC request through the MCP handler.
func (f *fixture) rpc(method string, params any) map[string]any {
	f.t.Helper()
	f.seq++
	line, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": f.seq, "method": method, "params": params})
	resp := f.mcp.handle(f.ctx, line)
	raw, _ := json.Marshal(resp)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	if out["error"] != nil {
		f.t.Fatal(method, out["error"])
	}
	return out["result"].(map[string]any)
}

// tool returns the text and error flag of a tools/call.
func (f *fixture) tool(name string, args map[string]string) (string, bool) {
	f.t.Helper()
	r := f.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ := r["isError"].(bool)
	return text, isErr
}

func (f *fixture) ok(name string, args map[string]string) map[string]any {
	f.t.Helper()
	text, isErr := f.tool(name, args)
	if isErr {
		f.t.Fatalf("%s failed: %s", name, text)
	}
	var out map[string]any
	if json.Unmarshal([]byte(text), &out) != nil {
		return map[string]any{"text": text}
	}
	return out
}

func TestAgentWorksATaskThroughMCP(t *testing.T) {
	f := setup(t)
	init := f.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test"}})
	if init["protocolVersion"] != "2025-06-18" || !strings.Contains(init["instructions"].(string), "submit_result") {
		t.Fatal("initialize", init)
	}
	if n := len(f.rpc("tools/list", nil)["tools"].([]any)); n != 7 {
		t.Fatal("tools", n)
	}

	var created struct{ Task struct{ ID string } }
	f.ownerCall("POST", "/tasks", map[string]string{"Title": "导出报表", "Goal": "导出订单", "Criteria": "包含全部订单"}, &created)
	id := created.Task.ID
	var w snapshotDTO
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &w)
	f.ownerCall("POST", "/tasks/"+id+"/execution/assign", map[string]any{"RoundID": w.Position.RoundID, "BasisVersion": 1, "ExpectedRevision": w.Position.Revision, "Subject": "agent"}, nil)

	if text, _ := f.tool("inbox", nil); !strings.Contains(text, id) {
		t.Fatal("inbox misses assigned task", text)
	}
	task := f.ok("read_task", map[string]string{"task_id": id})
	if task["goal"] != "导出订单" || !task["can"].(map[string]any)["start_work"].(bool) {
		t.Fatal("read_task", task)
	}
	stale := task["basis"].(string)
	started := f.ok("start_work", map[string]string{"task_id": id, "basis": stale})

	// A second claim from the old view is refused with guidance, not retried silently.
	if text, isErr := f.tool("start_work", map[string]string{"task_id": id, "basis": stale + "x"}); !isErr || !strings.Contains(text, "basis") {
		t.Fatal("corrupt basis accepted", text)
	}
	if text, isErr := f.tool("start_work", map[string]string{"task_id": id, "basis": started["basis"].(string)}); !isErr || !strings.Contains(text, "read_task") {
		t.Fatal("duplicate claim not explained", text)
	}

	// Renewal keeps the lease moving without any model action.
	var before snapshotDTO
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &before)
	time.Sleep(1200 * time.Millisecond)
	var after snapshotDTO
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &after)
	if !after.Execution.LeaseUntil.After(before.Execution.LeaseUntil) {
		t.Fatal("lease not renewed", before.Execution.LeaseUntil, after.Execution.LeaseUntil)
	}

	first := f.ok("post_message", map[string]string{"task_id": id, "text": "开始拉取数据"})["text"]
	if again := f.ok("post_message", map[string]string{"task_id": id, "text": "开始拉取数据"})["text"]; again != first {
		t.Fatal("retried message was not idempotent", first, again)
	}

	f.ok("ask_owner", map[string]string{"task_id": id, "basis": started["basis"].(string), "question": "CSV 还是 Excel？"})
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &w)
	if len(w.Questions) != 1 || w.NextActor != "owner" {
		t.Fatal("question not waiting on owner", w.NextActor)
	}
	f.ownerCall("POST", "/tasks/"+id+"/questions/"+w.Questions[0].ID+"/answer", map[string]string{"Body": "用 CSV"}, nil)
	task = f.ok("read_task", map[string]string{"task_id": id})
	if !strings.Contains(pretty(task["timeline"]), "用 CSV") || len(task["open_questions"].([]any)) != 0 {
		t.Fatal("answer not visible to agent", task["timeline"])
	}

	submitted := f.ok("submit_result", map[string]string{"task_id": id, "basis": task["basis"].(string), "result": "已导出 CSV"})
	if submitted["next_actor"] != "reviewer" {
		t.Fatal("result not waiting on reviewer", submitted)
	}
	f.ok("finish_run", map[string]string{"task_id": id, "basis": submitted["basis"].(string)})
	f.mcp.mu.Lock()
	running := len(f.mcp.runs)
	f.mcp.mu.Unlock()
	if running != 0 {
		t.Fatal("renewal still running after finish_run")
	}
	task = f.ok("read_task", map[string]string{"task_id": id})
	if task["current_result"].(map[string]any)["text"] != "已导出 CSV" {
		t.Fatal("result", task["current_result"])
	}
}

// A new adapter session (agent restart) finds its own run with a lapsed lease:
// read_task renews it once and takes over renewal instead of leaving it stuck.
func TestReadTaskResumesOwnLapsedRun(t *testing.T) {
	f := setup(t)
	var created struct{ Task struct{ ID string } }
	f.ownerCall("POST", "/tasks", map[string]string{"Title": "t", "Goal": "g", "Criteria": "c"}, &created)
	id := created.Task.ID
	var w snapshotDTO
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &w)
	f.ownerCall("POST", "/tasks/"+id+"/execution/assign", map[string]any{"RoundID": w.Position.RoundID, "BasisVersion": 1, "ExpectedRevision": w.Position.Revision, "Subject": "agent"}, nil)
	first := newServer(newClient(f.srv.URL+"/v1", f.agent), 0, &bytes.Buffer{}) // no renewal
	defer first.Close()
	f.mcp, first = first, f.mcp
	task := f.ok("read_task", map[string]string{"task_id": id})
	f.ok("start_work", map[string]string{"task_id": id, "basis": task["basis"].(string)})
	f.mcp = first // "restart": a fresh session that never started this run

	// Let the server see the lease as expired by reading it as unknown later is
	// slow; instead check the takeover itself: renewal starts and the note shows.
	task = f.ok("read_task", map[string]string{"task_id": id})
	exec := task["execution"].(map[string]any)
	if exec["note"] == nil || !f.mcp.renewing(func() string { f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &w); return w.Execution.ID }()) {
		t.Fatal("own run not resumed", exec)
	}
	if !task["can"].(map[string]any)["submit_result"].(bool) {
		t.Fatal("resumed run cannot submit", task["blocked"])
	}
	if task2 := f.ok("read_task", map[string]string{"task_id": id}); task2["execution"].(map[string]any)["note"] != nil {
		t.Fatal("resumed twice")
	}
}

func TestServeSpeaksLineDelimitedJSONRPC(t *testing.T) {
	s := newServer(newClient("http://127.0.0.1:1/v1", "x"), 0, &bytes.Buffer{})
	defer s.Close()
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}
{"jsonrpc":"2.0","method":"notifications/initialized"}
{"jsonrpc":"2.0","id":2,"method":"nope"}
not json
`)
	var out bytes.Buffer
	if e := s.Serve(context.Background(), in, &out); e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(strings.TrimSpace(out.String()), "\n")
	if len(lines) != 3 || !strings.Contains(lines[0], `"protocolVersion":"2025-11-25"`) || !strings.Contains(lines[1], "-32601") || !strings.Contains(lines[2], "-32700") {
		t.Fatal(out.String())
	}
}
