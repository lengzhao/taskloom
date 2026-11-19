package main

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// basis is the version a write is pinned to. It is handed to the model as an
// opaque token from read_task and returned unchanged, so a write based on a
// stale view fails with a conflict instead of silently using the latest state.
type basis struct {
	TaskID           string `json:"t"`
	RoundID          string `json:"r"`
	BasisVersion     int64  `json:"b"`
	ExpectedRevision int64  `json:"v"`
	ExecutionID      string `json:"e,omitempty"`
	Generation       int64  `json:"g,omitempty"`
}

func (b basis) token() string {
	raw, _ := json.Marshal(b)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func parseBasis(taskID, token string) (basis, error) {
	var b basis
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil || json.Unmarshal(raw, &b) != nil || b.TaskID == "" {
		return b, errInvalid("basis 无效：请使用 read_task 返回的 basis 原样传入")
	}
	if b.TaskID != taskID {
		return b, errInvalid("basis 属于另一个任务：请对该任务重新 read_task")
	}
	return b, nil
}

// Request bodies match the server DTOs exactly (it rejects unknown fields).
type flowBasis struct {
	TaskID, RoundID                string
	BasisVersion, ExpectedRevision int64
}
type executionRef struct {
	TaskID, ExecutionID string
	Generation          int64
}

func (b basis) flow() flowBasis {
	return flowBasis{b.TaskID, b.RoundID, b.BasisVersion, b.ExpectedRevision}
}
func (b basis) execution() executionRef { return executionRef{b.TaskID, b.ExecutionID, b.Generation} }

// next derives the follow-up basis from a command's snapshot. Writes never
// change the task basis version, so it is carried over.
func (b basis) next(s snapshotDTO) basis {
	n := basis{TaskID: b.TaskID, RoundID: s.Position.RoundID, BasisVersion: b.BasisVersion, ExpectedRevision: s.Position.Revision}
	if s.Execution != nil {
		n.ExecutionID, n.Generation = s.Execution.ID, s.Execution.Generation
	}
	return n
}

type invalidInput string

func (e invalidInput) Error() string { return string(e) }
func errInvalid(msg string) error    { return invalidInput(msg) }

// explain turns an error into guidance the model can act on.
func explain(err error) string {
	var api *apiError
	var bad invalidInput
	switch {
	case errors.As(err, &bad):
		return string(bad)
	case errors.As(err, &api):
		hint := map[int]string{
			400: "输入无效：检查参数（正文不能为空）。",
			401: "凭据无效或已过期：请联系管理员重新签发 Agent 业务凭据。",
			403: "无权执行：该操作不属于你，或任务未分配给你。用 read_task 查看 blocked 原因。",
			404: "任务或记录不存在，或你无权查看。",
			409: "版本冲突或当前状态不允许：任务已变化。请先 read_task 获取最新 basis 和 blocked 原因再决定。",
		}[api.Status]
		if hint == "" {
			hint = "服务暂时不可用，可稍后重试。"
		}
		return fmt.Sprintf("%s（服务端：%s）", hint, api.Message)
	default:
		return "调用 Taskloom 失败：" + err.Error()
	}
}

var reasonText = map[string]string{
	"task_not_active":                "任务已结束或已取消",
	"business_credential_required":   "需要 Agent 业务凭据",
	"not_assigned":                   "任务未分配给你",
	"execution_in_progress":          "本轮已有执行在进行",
	"previous_execution_not_stopped": "上一次执行尚未确认安全停止",
	"step_not_open":                  "当前步骤还不能执行",
	"not_your_execution":             "当前执行不属于你的实例",
	"execution_not_active":           "执行已结束",
	"execution_fenced":               "执行已失效（取消或被替换）",
	"round_changed":                  "任务已进入新一轮，需要重新 read_task",
	"basis_changed":                  "任务约定已更新，需要重新认领",
	"lease_expired":                  "执行保活已过期",
	"not_reviewer":                   "你不是验收人",
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

func schema(required []string, props map[string]string) map[string]any {
	p := map[string]any{}
	for name, desc := range props {
		p[name] = map[string]any{"type": "string", "description": desc}
	}
	return map[string]any{"type": "object", "properties": p, "required": required, "additionalProperties": false}
}

const (
	descTask  = "任务 ID"
	descBasis = "read_task（或上一次写操作）返回的 basis，原样传入"
)

func toolList() []tool {
	return []tool{
		{"inbox", "列出当前等你处理的任务（下一步在等你这个执行 Agent）。", schema(nil, nil)},
		{"read_task", "读取任务：目标、验收标准、状态、下一步等谁、你能做哪些操作及不能做的原因、待回答问题、当前结果和最近时间线，并返回写操作所需的 basis。", schema([]string{"task_id"}, map[string]string{"task_id": descTask})},
		{"start_work", "认领并开始执行任务。成功后适配器会在执行期间自动续期。", schema([]string{"task_id", "basis"}, map[string]string{"task_id": descTask, "basis": descBasis})},
		{"post_message", "在任务时间线留言：汇报进度、回复人的引导。留言不算交付结果。", schema([]string{"task_id", "text"}, map[string]string{"task_id": descTask, "text": "留言内容"})},
		{"ask_owner", "向任务负责人提问（说明问题、为何卡住、可选方案和建议）。问题未回答前任务不能完成；回答会出现在 read_task 的时间线中。", schema([]string{"task_id", "basis", "question"}, map[string]string{"task_id": descTask, "basis": descBasis, "question": "问题内容"})},
		{"submit_result", "明确提交本轮的正式结果（交付物）。需要已认领执行；再次提交会替换当前结果。", schema([]string{"task_id", "basis", "result"}, map[string]string{"task_id": descTask, "basis": descBasis, "result": "结果正文"})},
		{"finish_run", "报告本次主运行已结束并停止自动续期。不代表任务完成，也不证明外部工作已停止。", schema([]string{"task_id", "basis"}, map[string]string{"task_id": descTask, "basis": descBasis})},
	}
}

// key makes retries of the same call in this session reuse one Idempotency-Key.
func (s *server) key(parts ...any) string {
	raw, _ := json.Marshal(append([]any{s.session}, parts...))
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:16])
}

func required(args map[string]string, names ...string) error {
	for _, n := range names {
		if strings.TrimSpace(args[n]) == "" {
			return errInvalid("缺少参数 " + n)
		}
	}
	return nil
}

func pretty(v any) string {
	raw, _ := json.MarshalIndent(v, "", "  ")
	return string(raw)
}

func (s *server) call(ctx context.Context, name string, rawArgs json.RawMessage) (string, error) {
	args := map[string]string{}
	if len(rawArgs) > 0 && string(rawArgs) != "null" {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", errInvalid("参数必须是字符串字段的对象")
		}
	}
	path := func() string { return "/tasks/" + url.PathEscape(args["task_id"]) }
	switch name {
	case "inbox":
		var items []pendingDTO
		if err := s.api.do(ctx, "GET", "/inbox", nil, "", &items); err != nil {
			return "", err
		}
		if len(items) == 0 {
			return "当前没有等你处理的任务。", nil
		}
		return pretty(items), nil
	case "read_task":
		if err := required(args, "task_id"); err != nil {
			return "", err
		}
		return s.readTask(ctx, args["task_id"])
	case "post_message":
		if err := required(args, "task_id", "text"); err != nil {
			return "", err
		}
		var c commitDTO
		if err := s.api.do(ctx, "POST", path()+"/commits", map[string]string{"Body": args["text"], "RefID": ""}, s.key(name, args), &c); err != nil {
			return "", err
		}
		return "已留言（" + c.ID + "）。", nil
	}
	if err := required(args, "task_id", "basis"); err != nil {
		return "", err
	}
	b, err := parseBasis(args["task_id"], args["basis"])
	if err != nil {
		return "", err
	}
	var snap snapshotDTO
	switch name {
	case "start_work":
		if err = s.api.do(ctx, "POST", path()+"/execution/open", b.flow(), s.key(name, args), &snap); err != nil {
			return "", err
		}
		n := b.next(snap)
		s.startRenewal(n.execution())
		return pretty(map[string]any{"message": "已认领执行，执行期间自动续期。", "execution_generation": n.Generation, "basis": n.token()}), nil
	case "ask_owner":
		if err = required(args, "question"); err != nil {
			return "", err
		}
		body := map[string]string{"RoundID": b.RoundID, "Body": args["question"]}
		if err = s.api.do(ctx, "POST", path()+"/questions", body, s.key(name, args), &snap); err != nil {
			return "", err
		}
		return pretty(map[string]any{"message": "已向负责人提问；回答前任务不能完成。可继续执行，稍后用 read_task 查看回答。", "basis": b.next(snap).token()}), nil
	case "submit_result":
		if err = required(args, "result"); err != nil {
			return "", err
		}
		if b.ExecutionID == "" {
			return "", errInvalid("还没有执行：先 start_work 认领，再用它返回的 basis 提交")
		}
		body := struct {
			flowBasis
			ExecutionID, Body string
			Generation        int64
		}{b.flow(), b.ExecutionID, args["result"], b.Generation}
		if err = s.api.do(ctx, "POST", path()+"/results", body, s.key(name, args), &snap); err != nil {
			return "", err
		}
		return pretty(map[string]any{"message": "结果已提交。完成后请 finish_run。", "next_actor": snap.NextActor, "basis": b.next(snap).token()}), nil
	case "finish_run":
		if b.ExecutionID == "" {
			return "", errInvalid("basis 中没有执行：请先 read_task")
		}
		if err = s.api.do(ctx, "POST", path()+"/execution/end", b.execution(), s.key(name, args), &snap); err != nil {
			return "", err
		}
		s.stopRenewal(b.ExecutionID)
		return pretty(map[string]any{"message": "已报告主运行结束，自动续期已停止。", "basis": b.next(snap).token()}), nil
	}
	return "", errInvalid("未知工具 " + name)
}

const timelineLimit = 20

func (s *server) readTask(ctx context.Context, taskID string) (string, error) {
	path := "/tasks/" + url.PathEscape(taskID)
	var d detailDTO
	if err := s.api.do(ctx, "GET", path, nil, "", &d); err != nil {
		return "", err
	}
	commits := d.Commits
	for page := d.Commits; len(page) == 100; {
		var next detailDTO
		if err := s.api.do(ctx, "GET", fmt.Sprintf("%s?after=%d", path, page[len(page)-1].Sequence), nil, "", &next); err != nil {
			return "", err
		}
		page = next.Commits
		commits = append(commits, page...)
	}
	var w snapshotDTO
	if err := s.api.do(ctx, "GET", path+"/workflow", nil, "", &w); err != nil {
		return "", err
	}
	// CanRenew means the current execution is this agent's own, still active
	// and writable. After a restart nothing renews it, so its lease may have
	// lapsed: renew once now (same instance, same generation — no new
	// execution) and take over background renewal.
	resumed := false
	if w.CanRenew && w.Execution != nil && !s.renewing(w.Execution.ID) {
		ref := executionRef{d.Task.ID, w.Execution.ID, w.Execution.Generation}
		if err := s.api.do(ctx, "POST", path+"/execution/renew", ref, s.key("renew", ref, time.Now().UnixNano()), nil); err != nil {
			return "", err
		}
		s.startRenewal(ref)
		resumed = true
		// Command snapshots carry no viewer hints; read the query view again.
		w = snapshotDTO{}
		if err := s.api.do(ctx, "GET", path+"/workflow", nil, "", &w); err != nil {
			return "", err
		}
	}
	byID := map[string]commitDTO{}
	for _, c := range commits {
		byID[c.ID] = c
	}
	b := basis{TaskID: d.Task.ID, RoundID: w.Position.RoundID, BasisVersion: d.Task.BasisVersion, ExpectedRevision: w.Position.Revision}
	if w.Execution != nil {
		b.ExecutionID, b.Generation = w.Execution.ID, w.Execution.Generation
	}
	blocked := map[string]string{}
	for action, code := range w.Blocked {
		blocked[action] = code + "：" + reasonText[code]
	}
	questions := []map[string]any{}
	for _, q := range w.Questions {
		questions = append(questions, map[string]any{"question": byID[q.CommitID].Body, "asked_by": q.Asker, "asked_at": q.CreatedAt.Format(time.RFC3339)})
	}
	if len(commits) > timelineLimit {
		commits = commits[len(commits)-timelineLimit:]
	}
	timeline := []map[string]any{}
	for _, c := range commits {
		entry := map[string]any{"kind": c.Kind, "author": c.Author, "at": c.CreatedAt.Format(time.RFC3339), "text": c.Body}
		if c.Kind == "answer" {
			entry["answers"] = byID[c.RefID].Body
		}
		timeline = append(timeline, entry)
	}
	out := map[string]any{
		"task_id":        d.Task.ID,
		"title":          d.Task.Title,
		"goal":           d.Task.Goal,
		"criteria":       d.Task.Criteria,
		"owner":          d.Task.Owner,
		"status":         w.Position.Status,
		"step":           w.Position.State,
		"waiting":        w.Position.Waiting,
		"next_actor":     w.NextActor,
		"assignee":       w.Assignee,
		"can":            map[string]bool{"start_work": w.CanOpen, "submit_result": w.CanSubmit, "finish_run": w.CanRenew},
		"blocked":        blocked,
		"open_questions": questions,
		"timeline":       timeline,
		"basis":          b.token(),
	}
	if w.Execution != nil {
		e := map[string]any{"status": w.Execution.Status, "generation": w.Execution.Generation, "agent": w.Execution.Subject, "lease_until": w.Execution.LeaseUntil.Format(time.RFC3339)}
		if resumed {
			e["note"] = "这是你之前认领的执行，已恢复并接管自动续期"
		}
		out["execution"] = e
	}
	if w.ResultCommit != nil {
		r := map[string]any{"text": w.ResultCommit.Body}
		if w.Review != nil {
			r["review"] = w.Review.Outcome
			if w.Review.Reason != "" {
				r["review_reason"] = w.Review.Reason
			}
		}
		out["current_result"] = r
	}
	return pretty(out), nil
}
