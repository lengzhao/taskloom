// Package mcp adapts the shared task commands to the Model Context Protocol
// (Streamable HTTP, JSON responses only) so an Agent connects with one URL and
// its business credential. It is a protocol adapter like api/workflow: every
// rule, authorization, version and idempotency check stays in the commands it
// calls. It keeps no per-session state and starts no background work.
package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"github.com/lengzhao/taskloom/runtime/httpx"
	"io"
	"net/http"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/fault"
	capfile "github.com/lengzhao/taskloom/cap/file"
	"github.com/lengzhao/taskloom/cap/httpapi"
	work "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/cap/workspace"
)

// Local interfaces keep the adapter to what an executing Agent may call: no
// task editing, assignment, review or task control. Creating a task is only
// splitting the task it executes; the command refuses anything else.
type commenter interface {
	Comment(ctx context.Context, credential, key string, in workspace.Comment) (workspace.Ref, error)
	Create(ctx context.Context, credential, key string, in workspace.Create) (workspace.Ref, error)
}
type claimer interface {
	Open(ctx context.Context, token, key string, in work.Open) (work.Ref, error)
	// Assign hands the task to another handler (reassign).
	Assign(ctx context.Context, token, key string, in work.Assignment) (work.Ref, error)
}
type submitter interface {
	Submit(ctx context.Context, token, key string, in work.Submit) (work.Ref, error)
}
type directory interface {
	People(ctx context.Context, credential string, q authorization.MemberQuery) (workspace.PeoplePage, error)
}
type asker interface {
	Ask(ctx context.Context, token, key string, in work.Ask) (work.Ref, error)
	Answer(ctx context.Context, token, key string, in work.Answer) (work.Ref, error)
	Close(ctx context.Context, token, key string, in work.CloseQuestion) (work.Ref, error)
}

type files interface {
	capfile.Commands
	capfile.Queries
}

// Deps has seven fields, one per Agent-facing capability. Like api/workflow it
// only translates a protocol; splitting it would scatter one tool table across
// adapters without separating any rule.
type Deps struct {
	Comments   commenter    `json:"comments"`
	Workflow   work.Queries `json:"workflow"`
	Executions claimer      `json:"executions"`
	Results    submitter    `json:"results"`
	Questions  asker        `json:"questions"`
	// People is the member directory an Agent searches for whom to ask.
	People directory `json:"people"`
	// Files uploads and reads task files an Agent attaches or receives.
	Files files `json:"files"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("api/mcp", func(_ struct{}, d Deps) (*Module, error) {
		if d.Comments == nil || d.Workflow == nil || d.Executions == nil || d.Results == nil || d.Questions == nil || d.People == nil || d.Files == nil {
			return nil, fault.ErrInput
		}
		return &Module{d}, nil
	})
}

func (m *Module) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "POST", Path: "/mcp", Operation: "mcp.call", Handler: http.HandlerFunc(m.serve)}}
}

const serverVersion = "0.4.0"

var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

const instructions = `Taskloom 任务协作。你是执行者，人是负责人。推荐流程：
1. inbox 查看等你处理的任务；read_task 读取目标、验收标准、下一步等谁、不能操作的原因和最近时间线。
2. start_work 认领执行。执行不会超时，但负责人看“最近进展时间”判断你是否卡住：长时间工作请每 10～15 分钟用 post_message 汇报一次进度。
3. inbox 里也可能是别人向你提的问题（next_actor 为 addressee）：read_task 查看 open_questions 中 for_you 为 true 的，用 answer_question 回答。
   任务不适合你或需要别人接手时，用 reassign 改派（你是当前处理人或这次分配是你做的）。
   用 post_message 汇报进度或回复人的引导；遇到需要人决定或需要别人协助的问题，用 ask 提问（默认问负责人），需要别人协助时先用 list_people 找到对方，再用 to 指定（未回答前任务不能完成）。
4. 完成后用 submit_result 明确提交结果（普通留言不算交付）。被退回时按原因修改后再提交。
   交付文件时先 upload_file 得到 file_id，再在 submit_result 或 post_message 的 files 中引用；链接放 links。时间线和结果里的文件用 read_file 读取。
被退回时执行仍然有效；任务被转交或取消后，你的执行会立即失效（写入被拒绝），此时停止在该任务上的工作。
写操作需要 read_task 返回的 basis；失败时错误以 [code] 开头，遇到冲突先重新 read_task 再决定。`

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (m *Module) serve(w http.ResponseWriter, r *http.Request) {
	token := httpx.Credential(r)
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
		return
	}
	if trimmed := bytes.TrimSpace(raw); len(trimmed) > 0 && trimmed[0] == '[' {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32600, "batch requests are not supported"}})
		return
	}
	var msg rpcMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		writeRPC(w, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}})
		return
	}
	if len(msg.ID) == 0 || msg.Method == "" { // notification or client response
		w.WriteHeader(http.StatusAccepted)
		return
	}
	session := r.Header.Get("Mcp-Session-Id")
	if msg.Method == "initialize" {
		session = newSession()
		w.Header().Set("Mcp-Session-Id", session)
	}
	writeRPC(w, m.handle(r.Context(), call{token: token, session: session, request: msg.ID}, msg))
}

func writeRPC(w http.ResponseWriter, resp rpcResponse) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func newSession() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (m *Module) handle(ctx context.Context, c call, msg rpcMessage) rpcResponse {
	reply := func(result any) rpcResponse { return rpcResponse{JSONRPC: "2.0", ID: msg.ID, Result: result} }
	switch msg.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(msg.Params, &p)
		version := protocolVersions[0]
		for _, v := range protocolVersions {
			if v == p.ProtocolVersion {
				version = v
			}
		}
		return reply(map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "taskloom", "version": serverVersion},
			"instructions":    instructions,
		})
	case "ping":
		return reply(map[string]any{})
	case "tools/list":
		return reply(map[string]any{"tools": toolList()})
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return rpcResponse{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{-32602, "invalid params"}}
		}
		text, err := m.call(ctx, c, p.Name, p.Arguments)
		if err != nil {
			return reply(map[string]any{"content": []map[string]string{{"type": "text", "text": explain(err)}}, "isError": true})
		}
		return reply(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
	default:
		return rpcResponse{JSONRPC: "2.0", ID: msg.ID, Error: &rpcError{-32601, "method not found: " + msg.Method}}
	}
}
