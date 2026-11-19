package mcp_test

import (
	"bytes"
	"context"
	"encoding/json"
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
	_ "github.com/lengzhao/taskloom/plugins"
	"github.com/lengzhao/taskloom/plugins/security"
	"github.com/lengzhao/taskloom/runtime/config"
)

// fixture serves the real default graph's API modules, with an owner and an
// agent holding its business credential.
type fixture struct {
	t            *testing.T
	srv          *httptest.Server
	owner, agent string
	session      string
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
	_, graph, e := config.BuildFiles(ctx, "../../../base.yaml", overlay)
	if e != nil {
		t.Fatal(e)
	}
	storage, _ := build.GetByID[lifecycle.Resource](graph, "sql")
	if e = storage.Open(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { storage.Close(ctx) })
	// The real transport: routes, prefix and the shared request envelope.
	mux, _ := build.GetByID[http.Handler](graph, "http")
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	sec, _ := build.GetByID[*security.Service](graph, "security")
	if e := sec.Bootstrap(ctx, security.AdminInput{Tenant: "t", Subject: "owner", Operator: "test", Reason: "fixture", Password: "AdminPassword123!"}); e != nil {
		t.Fatal(e)
	}
	issued, e := sec.Login(ctx, capsecurity.LoginInput{Tenant: "t", Username: "owner", Password: "AdminPassword123!"})
	if e != nil {
		t.Fatal(e)
	}
	f := &fixture{t: t, srv: srv, owner: issued.Secret}
	if e = sec.Create(ctx, f.owner, "agent", identity.Principal{Tenant: "t", ID: "agent", Kind: "agent", OwnerID: "owner"}); e != nil {
		t.Fatal(e)
	}
	agent, e := sec.Issue(ctx, f.owner, "business", capsecurity.IssueInput{Tenant: "t", Subject: "agent", ExpiresAt: time.Now().Add(time.Hour)})
	if e != nil {
		t.Fatal(e)
	}
	f.agent = agent.Secret
	return f
}

func (f *fixture) post(path, token string, body any, header map[string]string) *http.Response {
	f.t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest("POST", f.srv.URL+"/v1"+path, bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	for k, v := range header {
		req.Header.Set(k, v)
	}
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		f.t.Fatal(e)
	}
	return resp
}

// ownerCall uses the shared HTTP API as the human owner.
func (f *fixture) ownerCall(method, path string, body, out any) {
	f.t.Helper()
	f.seq++
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, f.srv.URL+"/v1"+path, bytes.NewReader(raw))
	req.Header.Set("Authorization", "Bearer "+f.owner)
	req.Header.Set("Idempotency-Key", "owner-"+string(rune('a'+f.seq)))
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		f.t.Fatal(e)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		f.t.Fatal(method, path, resp.StatusCode, string(b))
	}
	if out != nil {
		_ = json.Unmarshal(b, out)
	}
}

// rpc posts one JSON-RPC request to /v1/mcp as the agent.
func (f *fixture) rpc(method string, params any) map[string]any {
	f.t.Helper()
	f.seq++
	resp := f.post("/mcp", f.agent, map[string]any{"jsonrpc": "2.0", "id": f.seq, "method": method, "params": params}, map[string]string{"Mcp-Session-Id": f.session, "Accept": "application/json, text/event-stream"})
	defer resp.Body.Close()
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		f.session = s
	}
	var out map[string]any
	if e := json.NewDecoder(resp.Body).Decode(&out); e != nil {
		f.t.Fatal(method, resp.StatusCode, e)
	}
	if out["error"] != nil {
		f.t.Fatal(method, out["error"])
	}
	return out["result"].(map[string]any)
}

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

type snapshot struct {
	Position struct {
		RoundID string
	}
	NextActor string
	Execution *struct {
		Status     string
		ProgressAt time.Time
	}
	Questions []struct{ ID string }
}

func TestAgentWorksATaskThroughMCP(t *testing.T) {
	f := setup(t)
	init := f.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test"}})
	if init["protocolVersion"] != "2025-06-18" || !strings.Contains(init["instructions"].(string), "submit_result") || f.session == "" {
		t.Fatal("initialize", init, f.session)
	}
	if n := len(f.rpc("tools/list", nil)["tools"].([]any)); n != 7 {
		t.Fatal("tools", n)
	}

	var created struct{ Task struct{ ID string } }
	f.ownerCall("POST", "/tasks", map[string]string{"Title": "导出报表", "Goal": "导出订单", "Criteria": "包含全部订单"}, &created)
	id := created.Task.ID
	var w snapshot
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &w)
	f.ownerCall("POST", "/tasks/"+id+"/execution/assign", map[string]any{"Subject": "agent"}, nil)

	if text, _ := f.tool("inbox", nil); !strings.Contains(text, id) {
		t.Fatal("inbox misses assigned task", text)
	}
	task := f.ok("read_task", map[string]string{"task_id": id})
	if task["goal"] != "导出订单" || !task["can"].(map[string]any)["start_work"].(bool) {
		t.Fatal("read_task", task)
	}
	stale := task["basis"].(string)
	started := f.ok("start_work", map[string]string{"task_id": id, "basis": stale})
	if text, isErr := f.tool("start_work", map[string]string{"task_id": id, "basis": stale + "x"}); !isErr || !strings.Contains(text, "basis") {
		t.Fatal("corrupt basis accepted", text)
	}
	if text, isErr := f.tool("start_work", map[string]string{"task_id": id, "basis": started["basis"].(string)}); !isErr || !strings.Contains(text, "read_task") {
		t.Fatal("duplicate claim not explained", text)
	}

	// A message is progress the owner can see; retrying it in the same session is idempotent.
	var before snapshot
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &before)
	time.Sleep(5 * time.Millisecond)
	first := f.ok("post_message", map[string]string{"task_id": id, "text": "开始拉取数据"})["text"]
	if again := f.ok("post_message", map[string]string{"task_id": id, "text": "开始拉取数据"})["text"]; again != first {
		t.Fatal("retried message was not idempotent", first, again)
	}
	var after snapshot
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &after)
	if !after.Execution.ProgressAt.After(before.Execution.ProgressAt) {
		t.Fatal("message not recorded as progress")
	}
	if task = f.ok("read_task", map[string]string{"task_id": id}); task["execution"].(map[string]any)["last_progress_at"] == nil {
		t.Fatal("agent cannot see its last progress", task["execution"])
	}

	asked := f.ok("ask_owner", map[string]string{"task_id": id, "basis": started["basis"].(string), "question": "CSV 还是 Excel？"})
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &w)
	if len(w.Questions) != 1 || w.NextActor != "addressee" {
		t.Fatal("question not waiting on the owner it was asked of", w.NextActor)
	}
	f.ownerCall("POST", "/tasks/"+id+"/questions/"+w.Questions[0].ID+"/answer", map[string]string{"Body": "用 CSV"}, nil)
	task = f.ok("read_task", map[string]string{"task_id": id})
	timeline, _ := json.Marshal(task["timeline"])
	if !strings.Contains(string(timeline), "用 CSV") || len(task["open_questions"].([]any)) != 0 {
		t.Fatal("answer not visible to agent", string(timeline))
	}

	// The basis returned by ask_owner still carries the execution it was asked from.
	submitted := f.ok("submit_result", map[string]string{"task_id": id, "basis": asked["basis"].(string), "result": "已导出 CSV"})
	if submitted["next_actor"] != "reviewer" {
		t.Fatal("result not waiting on reviewer", submitted)
	}
	task = f.ok("read_task", map[string]string{"task_id": id})
	if task["current_result"].(map[string]any)["text"] != "已导出 CSV" || task["execution"].(map[string]any)["writable"] != true {
		t.Fatal("result", task["current_result"], task["execution"])
	}
}

func TestTransportEdges(t *testing.T) {
	f := setup(t)
	// Notifications are accepted without a body.
	resp := f.post("/mcp", f.agent, map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}, nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal("notification", resp.StatusCode)
	}
	// Unknown methods and batches are JSON-RPC errors; unknown versions get the latest.
	for body, want := range map[string]string{
		`{"jsonrpc":"2.0","id":1,"method":"nope"}`:   "-32601",
		`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`: "-32600",
		`not json`: "-32700",
		`{"jsonrpc":"2.0","id":2,"method":"initialize","params":{"protocolVersion":"1999-01-01"}}`: `"protocolVersion":"2025-11-25"`,
	} {
		req, _ := http.NewRequest("POST", f.srv.URL+"/v1/mcp", strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+f.agent)
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if !strings.Contains(string(raw), want) {
			t.Fatal(body, string(raw))
		}
	}
	// A bad credential is explained inside the tool result.
	f.agent = "bad"
	if text, isErr := f.tool("inbox", nil); !isErr || !strings.Contains(text, "凭据") {
		t.Fatal("bad credential", text)
	}
	// Browsers from another origin are rejected by the shared request envelope.
	req, _ := http.NewRequest("POST", f.srv.URL+"/v1/mcp", strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"ping"}`))
	req.Header.Set("Origin", "https://evil.example")
	req.Header.Set("Authorization", "Bearer x")
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatal("foreign origin", resp.StatusCode)
	}
}

// The owner may ask the assigned agent a question; it appears in the agent's
// inbox and the agent answers it through MCP.
func TestAgentAnswersAQuestionAskedOfIt(t *testing.T) {
	f := setup(t)
	var created struct{ Task struct{ ID string } }
	f.ownerCall("POST", "/tasks", map[string]string{"Title": "t", "Goal": "g", "Criteria": "c"}, &created)
	id := created.Task.ID
	var w snapshot
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &w)
	f.ownerCall("POST", "/tasks/"+id+"/execution/assign", map[string]any{"Subject": "agent"}, nil)
	f.ownerCall("GET", "/tasks/"+id+"/workflow", nil, &w)
	f.ownerCall("POST", "/tasks/"+id+"/questions", map[string]string{"RoundID": w.Position.RoundID, "Body": "预计什么时候完成？", "Addressee": "agent"}, nil)

	if text, _ := f.tool("inbox", nil); !strings.Contains(text, "addressee") {
		t.Fatal("agent inbox misses the question", text)
	}
	task := f.ok("read_task", map[string]string{"task_id": id})
	q := task["open_questions"].([]any)[0].(map[string]any)
	if q["asked_of"] != "agent" || q["id"] == nil {
		t.Fatal("open question", q)
	}
	f.ok("answer_question", map[string]string{"task_id": id, "question_id": q["id"].(string), "text": "今天下班前"})
	task = f.ok("read_task", map[string]string{"task_id": id})
	if len(task["open_questions"].([]any)) != 0 || task["next_actor"] != "assignee" {
		t.Fatal("answer did not close the question", task["open_questions"], task["next_actor"])
	}
}
