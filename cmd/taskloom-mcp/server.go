package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

const serverVersion = "0.1.0"

var protocolVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05"}

const instructions = `Taskloom 任务协作。你是执行者，人是负责人。推荐流程：
1. inbox 查看等你处理的任务；read_task 读取目标、验收标准、下一步等谁、不能操作的原因和最近时间线。
2. start_work 认领执行（适配器会在执行期间自动续期）。
3. 用 post_message 汇报进度或回复人的引导；遇到需要人决定的问题，用 ask_owner 提问（未回答前任务不能完成）。
4. 完成后用 submit_result 明确提交结果（普通留言不算交付），再用 finish_run 报告主运行结束。
写操作需要 read_task 返回的 basis；遇到版本冲突（409）先重新 read_task 再决定。`

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

// server owns the stdio loop and every background renewal it starts.
type server struct {
	api     *client
	session string
	renew   time.Duration
	log     io.Writer

	mu      sync.Mutex
	ctx     context.Context
	cancel  context.CancelFunc
	runs    map[string]context.CancelFunc // execution ID -> renewal stop
	workers sync.WaitGroup
}

func newServer(api *client, renew time.Duration, log io.Writer) *server {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	ctx, cancel := context.WithCancel(context.Background())
	return &server{api: api, session: hex.EncodeToString(b), renew: renew, log: log, ctx: ctx, cancel: cancel, runs: map[string]context.CancelFunc{}}
}

// Close stops all renewals. It does not end runs: ending the adapter is not
// evidence that the agent's work stopped, so leases simply expire to unknown.
func (s *server) Close() {
	s.cancel()
	s.workers.Wait()
}

func (s *server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	reader := bufio.NewReaderSize(in, 1<<20)
	enc := json.NewEncoder(out)
	lines := make(chan []byte)
	readErr := make(chan error, 1)
	go func() {
		defer close(lines)
		for {
			line, err := reader.ReadBytes('\n')
			if len(line) > 0 {
				select {
				case lines <- line:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					readErr <- err
				}
				return
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case line, ok := <-lines:
			if !ok {
				select {
				case err := <-readErr:
					return err
				default:
					return nil
				}
			}
			if resp := s.handle(ctx, line); resp != nil {
				if err := enc.Encode(resp); err != nil {
					return err
				}
			}
		}
	}
}

func (s *server) handle(ctx context.Context, line []byte) *rpcResponse {
	var m rpcMessage
	if err := json.Unmarshal(line, &m); err != nil {
		return &rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "parse error"}}
	}
	if len(m.ID) == 0 { // notification
		return nil
	}
	reply := func(result any) *rpcResponse { return &rpcResponse{JSONRPC: "2.0", ID: m.ID, Result: result} }
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &p)
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
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: m.ID, Error: &rpcError{-32602, "invalid params"}}
		}
		text, err := s.call(ctx, p.Name, p.Arguments)
		if err != nil {
			return reply(map[string]any{"content": []map[string]string{{"type": "text", "text": explain(err)}}, "isError": true})
		}
		return reply(map[string]any{"content": []map[string]string{{"type": "text", "text": text}}})
	default:
		return &rpcResponse{JSONRPC: "2.0", ID: m.ID, Error: &rpcError{-32601, "method not found: " + m.Method}}
	}
}

// startRenewal keeps one execution's lease alive until stopRenewal, a
// definite refusal from the server, or Close. It only proves this adapter is
// alive; stop proofs remain with the agent runtime.
func (s *server) startRenewal(ref executionRef) {
	if s.renew <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.runs[ref.ExecutionID]; ok {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.runs[ref.ExecutionID] = cancel
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer s.stopRenewal(ref.ExecutionID)
		t := time.NewTicker(s.renew)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				key := s.key("renew", ref, time.Now().UnixNano())
				err := s.api.do(ctx, "POST", "/tasks/"+ref.TaskID+"/execution/renew", ref, key, nil)
				var api *apiError
				if errors.As(err, &api) && api.Status >= 400 && api.Status < 500 {
					fmt.Fprintf(s.log, "taskloom-mcp: stop renewing %s: %v\n", ref.ExecutionID, err)
					return
				}
				if err != nil {
					fmt.Fprintf(s.log, "taskloom-mcp: renew %s failed, will retry: %v\n", ref.ExecutionID, err)
				}
			}
		}
	}()
}

func (s *server) renewing(executionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.runs[executionID]
	return ok
}

func (s *server) stopRenewal(executionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cancel, ok := s.runs[executionID]; ok {
		cancel()
		delete(s.runs, executionID)
	}
}
