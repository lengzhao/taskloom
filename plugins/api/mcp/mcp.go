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
	"io"
	"net/http"

	"github.com/lengzhao/pluginkit"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/httpapi"
	work "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/cap/workspace"
)

// Local interfaces keep the adapter to what an executing Agent may call: no
// task editing, assignment, review, answering or stop proofs.
type taskReader interface {
	Get(ctx context.Context, credential, taskID string, after int64) (workspace.Detail, error)
}
type commenter interface {
	Comment(ctx context.Context, credential, key string, in workspace.Comment) (commit.Commit, error)
}
type runner interface {
	Open(ctx context.Context, token, key string, in work.Open) (work.Snapshot, error)
	End(ctx context.Context, token, key string, in work.ExecutionRef) (work.Snapshot, error)
}
type submitter interface {
	Submit(ctx context.Context, token, key string, in work.Submit) (work.Snapshot, error)
}
type asker interface {
	Ask(ctx context.Context, token, key string, in work.Ask) (work.Snapshot, error)
}

// Deps has seven fields, one per Agent-facing capability plus the shared
// request envelope. Like api/workflow it only translates a protocol; splitting
// it would scatter one tool table across adapters without separating any rule.
type Deps struct {
	Tasks      taskReader              `json:"tasks"`
	Comments   commenter               `json:"comments"`
	Workflow   work.Queries            `json:"workflow"`
	Executions runner                  `json:"executions"`
	Results    submitter               `json:"results"`
	Questions  asker                   `json:"questions"`
	Security   httpapi.RequestSecurity `json:"security"`
}
type Module struct{ d Deps }

func init() {
	pluginkit.Register("api/mcp", func(_ struct{}, d Deps) (*Module, error) {
		if d.Tasks == nil || d.Comments == nil || d.Workflow == nil || d.Executions == nil || d.Results == nil || d.Questions == nil || d.Security == nil {
			return nil, fault.ErrInput
		}
		return &Module{d}, nil
	})
}

func (m *Module) Routes() []httpapi.Route {
	return []httpapi.Route{{Method: "POST", Path: "/mcp", Operation: "mcp.call", Handler: http.HandlerFunc(m.serve)}}
}

const serverVersion = "0.2.0"

var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26"}

const instructions = `Taskloom 任务协作。你是执行者，人是负责人。推荐流程：
1. inbox 查看等你处理的任务；read_task 读取目标、验收标准、下一步等谁、不能操作的原因和最近时间线。
2. start_work 认领执行。执行不会超时，但负责人看“最近进展时间”判断你是否卡住：长时间工作请每 10～15 分钟用 post_message 汇报一次进度。
3. 用 post_message 汇报进度或回复人的引导；遇到需要人决定的问题，用 ask_owner 提问（未回答前任务不能完成）。
4. 完成后用 submit_result 明确提交结果（普通留言不算交付），再用 finish_run 报告主运行结束。
写操作需要 read_task 返回的 basis；遇到版本冲突先重新 read_task 再决定。`

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
	token, ok := m.d.Security.Credential(w, r)
	if !ok {
		return
	}
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
	writeRPC(w, m.handle(r.Context(), call{token: token, session: session}, msg))
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
