package mcp_test

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
	raw, _ := json.Marshal(map[string]any{"sql": map[string]any{"config": map[string]any{"dsn": filepath.Join(dir, "db")}}, "blobs": map[string]any{"config": map[string]any{"dir": filepath.Join(dir, "files")}}})
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
	blobs, _ := build.GetByID[lifecycle.Resource](graph, "blobs")
	if e = blobs.Open(ctx); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { blobs.Close(ctx) })
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
	return f.rpcID(f.seq, method, params)
}

// rpcID sends a request with a given JSON-RPC id, as a client resending it does.
func (f *fixture) rpcID(id int, method string, params any) map[string]any {
	f.t.Helper()
	resp := f.post("/mcp", f.agent, map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}, map[string]string{"Mcp-Session-Id": f.session, "Accept": "application/json, text/event-stream"})
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

func (f *fixture) tool(name string, args any) (string, bool) {
	f.t.Helper()
	r := f.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	text := r["content"].([]any)[0].(map[string]any)["text"].(string)
	isErr, _ := r["isError"].(bool)
	return text, isErr
}

func (f *fixture) ok(name string, args any) map[string]any {
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
	if n := len(f.rpc("tools/list", nil)["tools"].([]any)); n != 14 {
		t.Fatal("tools", n)
	}

	var created struct{ Task struct{ ID string } }
	f.ownerCall("POST", "/tasks", map[string]string{"Title": "导出报表", "Goal": "导出订单", "Criteria": "包含全部订单"}, &created)
	id := created.Task.ID
	var w snapshot
	f.ownerCall("GET", "/tasks/"+id, nil, &w)
	f.ownerCall("POST", "/tasks/"+id+"/assign", map[string]any{"Subject": "agent"}, nil)

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

	// A message is progress the owner can see. Resending the same request is
	// idempotent; a new call with the same text is a new report.
	var before snapshot
	f.ownerCall("GET", "/tasks/"+id, nil, &before)
	time.Sleep(5 * time.Millisecond)
	msg := map[string]any{"name": "post_message", "arguments": map[string]string{"task_id": id, "text": "开始拉取数据"}}
	f.seq++
	text := func(r map[string]any) string { return r["content"].([]any)[0].(map[string]any)["text"].(string) }
	first := text(f.rpcID(f.seq, "tools/call", msg))
	if again := text(f.rpcID(f.seq, "tools/call", msg)); again != first {
		t.Fatal("resent message was not idempotent", first, again)
	}
	if next := f.ok("post_message", map[string]string{"task_id": id, "text": "开始拉取数据"})["text"]; next == first {
		t.Fatal("a repeated progress report was swallowed", next)
	}
	var after snapshot
	f.ownerCall("GET", "/tasks/"+id, nil, &after)
	if !after.Execution.ProgressAt.After(before.Execution.ProgressAt) {
		t.Fatal("message not recorded as progress")
	}
	if task = f.ok("read_task", map[string]string{"task_id": id}); task["execution"].(map[string]any)["last_progress_at"] == nil {
		t.Fatal("agent cannot see its last progress", task["execution"])
	}
	// The directory gives the agent whom it may ask; paging returns next only when more follow.
	people, _ := json.Marshal(f.ok("list_people", map[string]string{"kind": "human"}))
	if !strings.Contains(string(people), `"subject":"owner"`) || strings.Contains(string(people), `"agent"`) || strings.Contains(string(people), "next") {
		t.Fatal("list_people", string(people))
	}
	// The agent learns who it is and who is already on the task; it never lists itself.
	if task["you"] != "agent" || len(task["participants"].([]any)) != 0 {
		t.Fatal("viewer or askable participants", task["you"], task["participants"])
	}

	asked := f.ok("ask", map[string]string{"task_id": id, "basis": started["basis"].(string), "question": "CSV 还是 Excel？"})
	f.ownerCall("GET", "/tasks/"+id, nil, &w)
	if len(w.Questions) != 1 || w.NextActor != "addressee" {
		t.Fatal("question not waiting on the owner it was asked of", w.NextActor)
	}
	if q := f.ok("read_task", map[string]string{"task_id": id})["open_questions"].([]any)[0].(map[string]any); q["for_you"] != false {
		t.Fatal("a question to the owner is marked for the agent", q)
	}
	f.ownerCall("POST", "/tasks/"+id+"/questions/"+w.Questions[0].ID+"/answer", map[string]string{"Body": "用 CSV"}, nil)
	task = f.ok("read_task", map[string]string{"task_id": id})
	timeline, _ := json.Marshal(task["timeline"])
	if !strings.Contains(string(timeline), "用 CSV") || len(task["open_questions"].([]any)) != 0 {
		t.Fatal("answer not visible to agent", string(timeline))
	}

	// The basis returned by ask still carries the execution it was asked from.
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
	f.ownerCall("GET", "/tasks/"+id, nil, &w)
	f.ownerCall("POST", "/tasks/"+id+"/assign", map[string]any{"Subject": "agent"}, nil)
	f.ownerCall("GET", "/tasks/"+id, nil, &w)
	f.ownerCall("POST", "/tasks/"+id+"/questions", map[string]string{"RoundID": w.Position.RoundID, "Body": "预计什么时候完成？", "Addressee": "agent"}, nil)

	if text, _ := f.tool("inbox", nil); !strings.Contains(text, "addressee") {
		t.Fatal("agent inbox misses the question", text)
	}
	task := f.ok("read_task", map[string]string{"task_id": id})
	q := task["open_questions"].([]any)[0].(map[string]any)
	if q["asked_of"] != "agent" || q["for_you"] != true || q["id"] == nil {
		t.Fatal("open question", q)
	}
	f.ok("answer_question", map[string]string{"task_id": id, "question_id": q["id"].(string), "text": "今天下班前"})
	task = f.ok("read_task", map[string]string{"task_id": id})
	if len(task["open_questions"].([]any)) != 0 || task["next_actor"] != "assignee" {
		t.Fatal("answer did not close the question", task["open_questions"], task["next_actor"])
	}
}

// An Agent uploads a file, delivers it with a link, and the owner downloads
// the same bytes; the file is visible in read_task and read_file.
func TestAgentDeliversFilesThroughMCP(t *testing.T) {
	f := setup(t)
	f.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	var created struct{ Task struct{ ID string } }
	f.ownerCall("POST", "/tasks", map[string]string{"Title": "写报告", "Goal": "报告", "Criteria": "有文件"}, &created)
	id := created.Task.ID
	f.ownerCall("POST", "/tasks/"+id+"/assign", map[string]any{"Subject": "agent"}, nil)
	basis := f.ok("read_task", map[string]string{"task_id": id})["basis"].(string)
	basis = f.ok("start_work", map[string]string{"task_id": id, "basis": basis})["basis"].(string)

	if text, isErr := f.tool("upload_file", map[string]string{"task_id": id, "name": "a.md"}); !isErr {
		t.Fatal("upload without content", text)
	}
	up := f.ok("upload_file", map[string]string{"task_id": id, "name": "report.md", "text": "# 报告\n完成"})
	fileID := up["file_id"].(string)
	bin := f.ok("upload_file", map[string]string{"task_id": id, "name": "x.bin", "content_base64": "AAEC/w=="})
	if text, isErr := f.tool("submit_result", map[string]any{"task_id": id, "basis": basis, "files": []string{"0123456789abcdef0123456789abcdef"}}); !isErr || !strings.Contains(text, "不存在") {
		t.Fatal("unknown file accepted", text)
	}
	f.ok("submit_result", map[string]any{"task_id": id, "basis": basis, "files": []string{fileID, bin["file_id"].(string)}, "links": []map[string]string{{"url": "https://example.com/doc", "title": "设计文档"}}})

	task := f.ok("read_task", map[string]string{"task_id": id})
	result := task["current_result"].(map[string]any)
	files := result["files"].([]any)
	if len(files) != 2 || files[0].(map[string]any)["name"] != "report.md" || result["links"] == nil {
		t.Fatal("result attachments", result)
	}
	read := f.ok("read_file", map[string]string{"task_id": id, "file_id": fileID})
	if read["text"] != "# 报告\n完成" || !strings.HasPrefix(read["media_type"].(string), "text/") {
		t.Fatal("read_file", read)
	}
	if f.ok("read_file", map[string]string{"task_id": id, "file_id": bin["file_id"].(string)})["content_base64"] != "AAEC/w==" {
		t.Fatal("binary read_file")
	}

	req, _ := http.NewRequest("GET", f.srv.URL+"/v1/tasks/"+id+"/files/"+fileID, nil)
	req.Header.Set("Authorization", "Bearer "+f.owner)
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "# 报告\n完成" || !strings.HasPrefix(resp.Header.Get("Content-Disposition"), "attachment") || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "sandbox") {
		t.Fatal("download", resp.StatusCode, string(body), resp.Header)
	}
}

// An executing Agent splits its task through MCP: the subtask is created
// under it and the Agent's execution and basis stay usable.
func TestAgentSplitsATaskThroughMCP(t *testing.T) {
	f := setup(t)
	var created struct{ Task struct{ ID string } }
	f.ownerCall("POST", "/tasks", map[string]string{"Title": "报告", "Goal": "写报告", "Criteria": "完整"}, &created)
	id := created.Task.ID
	if text, isErr := f.tool("create_subtask", map[string]string{"task_id": id, "title": "资料"}); !isErr || !strings.Contains(text, "无权") {
		t.Fatal("split before taking part", text)
	}
	f.ownerCall("POST", "/tasks/"+id+"/assign", map[string]any{"Subject": "agent"}, nil)
	basis := f.ok("read_task", map[string]string{"task_id": id})["basis"].(string)
	basis = f.ok("start_work", map[string]string{"task_id": id, "basis": basis})["basis"].(string)
	if text, isErr := f.tool("create_subtask", map[string]string{"task_id": id, "title": "资料", "required": "yes"}); !isErr || !strings.Contains(text, "required") {
		t.Fatal("bad required accepted", text)
	}
	out := f.ok("create_subtask", map[string]any{"task_id": id, "title": "资料", "goal": "收集资料", "required": true})
	sub, _ := out["subtask_id"].(string)
	if sub == "" || !strings.Contains(out["message"].(string), "必需") {
		t.Fatal("create_subtask", out)
	}
	task := f.ok("read_task", map[string]string{"task_id": id})
	if !strings.Contains(fmt.Sprint(task["subtasks"]), sub) {
		t.Fatal("subtask not listed under the task", task["subtasks"])
	}
	if text, isErr := f.tool("submit_result", map[string]string{"task_id": id, "basis": basis, "result": "太早"}); !isErr || !strings.Contains(text, "read_task") {
		t.Fatal("submitted before the required subtask", text)
	}
	if _, isErr := f.tool("post_message", map[string]string{"task_id": id, "text": "继续写正文"}); isErr {
		t.Fatal("execution fenced by the Agent's own split")
	}
	f.ok("read_task", map[string]string{"task_id": sub})
	if text, isErr := f.tool("create_subtask", map[string]string{"task_id": id, "title": "x", "acceptance": "later"}); !isErr || !strings.Contains(text, "acceptance") {
		t.Fatal("bad acceptance accepted", text)
	}
	out = f.ok("create_subtask", map[string]string{"task_id": id, "title": "自己做", "goal": "g", "criteria": "c", "assignee": "agent"})
	mine, _ := out["subtask_id"].(string)
	if mine == "" || !strings.Contains(out["message"].(string), "start_work") {
		t.Fatal("self-assigned subtask", out)
	}
	f.ok("start_work", map[string]string{"task_id": mine, "basis": f.ok("read_task", map[string]string{"task_id": mine})["basis"].(string)})
}

// An Agent reads the tag vocabulary and tags the subtasks it splits by name.
func TestAgentTagsSubtasksThroughMCP(t *testing.T) {
	f := setup(t)
	if text, isErr := f.tool("list_tags", nil); isErr || !strings.Contains(text, "还没有标签") {
		t.Fatal("empty vocabulary", text)
	}
	var tag struct{ ID, Name, Description string }
	f.ownerCall("POST", "/tags", map[string]string{"Name": "Security", "Description": "安全风险"}, &tag)
	var old struct {
		ID, Name string
		Version  int64
	}
	f.ownerCall("POST", "/tags", map[string]string{"Name": "旧分类"}, &old)
	f.ownerCall("PATCH", "/tags/"+old.ID, map[string]any{"Name": old.Name, "Archived": true, "ExpectedVersion": old.Version}, nil)
	if text, isErr := f.tool("list_tags", nil); isErr || !strings.Contains(text, "Security") || strings.Contains(text, "旧分类") {
		t.Fatal("list_tags lists active tags only", text)
	}
	var created struct{ Task struct{ ID string } }
	f.ownerCall("POST", "/tasks", map[string]string{"Title": "上线", "Goal": "上线新版"}, &created)
	id := created.Task.ID
	f.ownerCall("POST", "/tasks/"+id+"/assign", map[string]any{"Subject": "agent"}, nil)
	f.ok("start_work", map[string]string{"task_id": id, "basis": f.ok("read_task", map[string]string{"task_id": id})["basis"].(string)})
	if text, isErr := f.tool("create_subtask", map[string]any{"task_id": id, "title": "x", "tags": "Security"}); !isErr || !strings.Contains(text, "tags") {
		t.Fatal("tags must be an array", text)
	}
	if text, isErr := f.tool("create_subtask", map[string]any{"task_id": id, "title": "x", "tags": []string{"nope"}}); !isErr || !strings.Contains(text, "list_tags") {
		t.Fatal("unknown tag name accepted", text)
	}
	if text, isErr := f.tool("create_subtask", map[string]any{"task_id": id, "title": "x", "tags": []string{"旧分类"}}); !isErr || !strings.Contains(text, "归档") {
		t.Fatal("archived tag accepted", text)
	}
	out := f.ok("create_subtask", map[string]any{"task_id": id, "title": "安全评估", "goal": "g", "tags": []string{"security"}})
	sub, _ := out["subtask_id"].(string)
	read := f.ok("read_task", map[string]string{"task_id": sub})
	if fmt.Sprint(read["tags"]) != "[Security]" {
		t.Fatal("read_task names the subtask's tags", read["tags"])
	}
	if fmt.Sprint(f.ok("read_task", map[string]string{"task_id": id})["tags"]) != "[]" {
		t.Fatal("subtask tags leaked to the parent")
	}
}
