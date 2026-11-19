package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/fault"
	"github.com/lengzhao/taskloom/cap/identity"
	work "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/cap/workspace"
)

// call carries the request credential and the MCP session used to derive
// idempotency keys, so a client retrying the same call reuses one key.
type call struct{ token, session string }

func (c call) key(parts ...any) string {
	scope := c.session
	if scope == "" {
		sum := sha256.Sum256([]byte(c.token))
		scope = hex.EncodeToString(sum[:])
	}
	raw, _ := json.Marshal(append([]any{scope}, parts...))
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:16])
}

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
		return b, invalid("basis 无效：请使用 read_task 返回的 basis 原样传入")
	}
	if b.TaskID != taskID {
		return b, invalid("basis 属于另一个任务：请对该任务重新 read_task")
	}
	return b, nil
}

func (b basis) flow() work.Basis {
	return work.Basis{TaskID: b.TaskID, RoundID: b.RoundID, BasisVersion: b.BasisVersion, ExpectedRevision: b.ExpectedRevision}
}
func (b basis) execution() work.ExecutionRef {
	return work.ExecutionRef{TaskID: b.TaskID, ExecutionID: b.ExecutionID, Generation: b.Generation}
}

// next derives the follow-up basis from a command's snapshot. Writes never
// change the task basis version, so it is carried over.
func (b basis) next(s work.Snapshot) basis {
	n := basis{TaskID: b.TaskID, RoundID: s.Position.RoundID, BasisVersion: b.BasisVersion, ExpectedRevision: s.Position.Revision}
	if s.Execution != nil {
		n.ExecutionID, n.Generation = s.Execution.ID, s.Execution.Generation
	}
	return n
}

type invalid string

func (e invalid) Error() string { return string(e) }

// explain turns an error into guidance the model can act on.
func explain(err error) string {
	var bad invalid
	switch {
	case errors.As(err, &bad):
		return string(bad)
	case errors.Is(err, identity.ErrUnauthenticated):
		return "凭据无效或已过期：请联系管理员重新签发 Agent 业务凭据。"
	case errors.Is(err, az.ErrForbidden):
		return "无权执行：该操作不属于你，或任务未分配给你。用 read_task 查看 blocked 原因。"
	case errors.Is(err, fault.ErrNotFound):
		return "任务或记录不存在，或你无权查看。"
	case errors.Is(err, fault.ErrConflict):
		return "版本冲突或当前状态不允许：任务已变化。请先 read_task 获取最新 basis 和 blocked 原因再决定。"
	case errors.Is(err, fault.ErrInput):
		return "输入无效：检查参数（正文不能为空）。"
	default:
		return "服务暂时不可用，可稍后重试。"
	}
}

var reasonText = map[string]string{
	work.ReasonTaskNotActive:       "任务已结束或已取消",
	work.ReasonCredential:          "需要 Agent 业务凭据",
	work.ReasonNotAssigned:         "任务未分配给你",
	work.ReasonExecutionInProgress: "本轮已有执行在进行",
	work.ReasonPreviousNotStopped:  "上一次执行尚未确认安全停止",
	work.ReasonStepNotOpen:         "当前步骤还不能执行",
	work.ReasonNotYourExecution:    "当前执行不属于你的实例",
	work.ReasonExecutionNotActive:  "执行已结束",
	work.ReasonExecutionFenced:     "执行已失效（取消或被替换）",
	work.ReasonRoundChanged:        "任务已进入新一轮，需要重新 read_task",
	work.ReasonBasisChanged:        "任务约定已更新，需要重新认领",
	work.ReasonNotReviewer:         "你不是验收人",
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
	if required == nil {
		required = []string{}
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
		{"start_work", "认领并开始执行任务。", schema([]string{"task_id", "basis"}, map[string]string{"task_id": descTask, "basis": descBasis})},
		{"post_message", "在任务时间线留言：汇报进度、回复人的引导。留言会更新你的最近进展时间，但不算交付结果。", schema([]string{"task_id", "text"}, map[string]string{"task_id": descTask, "text": "留言内容"})},
		{"ask_owner", "向任务负责人提问（说明问题、为何卡住、可选方案和建议）。问题未回答前任务不能完成；回答会出现在 read_task 的时间线中。", schema([]string{"task_id", "basis", "question"}, map[string]string{"task_id": descTask, "basis": descBasis, "question": "问题内容"})},
		{"submit_result", "明确提交本轮的正式结果（交付物）。需要已认领执行；再次提交会替换当前结果。", schema([]string{"task_id", "basis", "result"}, map[string]string{"task_id": descTask, "basis": descBasis, "result": "结果正文"})},
		{"finish_run", "报告本次主运行已结束。不代表任务完成，也不证明外部工作已停止。", schema([]string{"task_id", "basis"}, map[string]string{"task_id": descTask, "basis": descBasis})},
	}
}

func required(args map[string]string, names ...string) error {
	for _, n := range names {
		if strings.TrimSpace(args[n]) == "" {
			return invalid("缺少参数 " + n)
		}
	}
	return nil
}

func pretty(v any) string {
	raw, _ := json.MarshalIndent(v, "", "  ")
	return string(raw)
}

func (m *Module) call(ctx context.Context, c call, name string, rawArgs json.RawMessage) (string, error) {
	args := map[string]string{}
	if len(rawArgs) > 0 && string(rawArgs) != "null" {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", invalid("参数必须是字符串字段的对象")
		}
	}
	switch name {
	case "inbox":
		items, err := m.d.Workflow.Inbox(ctx, c.token)
		if err != nil {
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
		return m.readTask(ctx, c, args["task_id"])
	case "post_message":
		if err := required(args, "task_id", "text"); err != nil {
			return "", err
		}
		cm, err := m.d.Comments.Comment(ctx, c.token, c.key(name, args), workspace.Comment{TaskID: args["task_id"], Body: args["text"]})
		if err != nil {
			return "", err
		}
		return "已留言（" + cm.ID + "）。", nil
	case "start_work", "ask_owner", "submit_result", "finish_run":
	default:
		return "", invalid("未知工具 " + name)
	}
	if err := required(args, "task_id", "basis"); err != nil {
		return "", err
	}
	b, err := parseBasis(args["task_id"], args["basis"])
	if err != nil {
		return "", err
	}
	key := c.key(name, args)
	switch name {
	case "start_work":
		s, err := m.d.Executions.Open(ctx, c.token, key, work.Open{Basis: b.flow()})
		if err != nil {
			return "", err
		}
		n := b.next(s)
		return pretty(map[string]any{"message": "已认领执行。长时间工作请定期 post_message 汇报进度。", "execution_generation": n.Generation, "basis": n.token()}), nil
	case "ask_owner":
		if err = required(args, "question"); err != nil {
			return "", err
		}
		s, err := m.d.Questions.Ask(ctx, c.token, key, work.Ask{TaskID: b.TaskID, RoundID: b.RoundID, Body: args["question"]})
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"message": "已向负责人提问；回答前任务不能完成。可继续执行，稍后用 read_task 查看回答。", "basis": b.next(s).token()}), nil
	case "submit_result":
		if err = required(args, "result"); err != nil {
			return "", err
		}
		if b.ExecutionID == "" {
			return "", invalid("还没有执行：先 start_work 认领，再用它返回的 basis 提交")
		}
		s, err := m.d.Results.Submit(ctx, c.token, key, work.Submit{Basis: b.flow(), ExecutionID: b.ExecutionID, Generation: b.Generation, Body: args["result"]})
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"message": "结果已提交。完成后请 finish_run。", "next_actor": s.NextActor, "basis": b.next(s).token()}), nil
	default: // finish_run
		if b.ExecutionID == "" {
			return "", invalid("basis 中没有执行：请先 read_task")
		}
		s, err := m.d.Executions.End(ctx, c.token, key, b.execution())
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"message": "已报告主运行结束。", "basis": b.next(s).token()}), nil
	}
}

const timelineLimit = 20

func (m *Module) readTask(ctx context.Context, c call, taskID string) (string, error) {
	d, err := m.d.Tasks.Get(ctx, c.token, taskID, 0)
	if err != nil {
		return "", err
	}
	commits := d.Commits
	for page := d.Commits; len(page) == 100; {
		next, err := m.d.Tasks.Get(ctx, c.token, taskID, page[len(page)-1].Sequence)
		if err != nil {
			return "", err
		}
		page = next.Commits
		commits = append(commits, page...)
	}
	w, err := m.d.Workflow.Read(ctx, c.token, taskID)
	if err != nil {
		return "", err
	}
	byID := map[string]commit.Commit{}
	for _, cm := range commits {
		byID[cm.ID] = cm
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
	for _, cm := range commits {
		entry := map[string]any{"kind": cm.Kind, "author": cm.Author, "at": cm.CreatedAt.Format(time.RFC3339), "text": cm.Body}
		if cm.Kind == "answer" {
			entry["answers"] = byID[cm.RefID].Body
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
		"can":            map[string]bool{"start_work": w.CanOpen, "submit_result": w.CanSubmit, "finish_run": w.CanEnd},
		"blocked":        blocked,
		"open_questions": questions,
		"timeline":       timeline,
		"basis":          b.token(),
	}
	if e := w.Execution; e != nil {
		x := map[string]any{"status": e.Status, "generation": e.Generation, "agent": e.Subject}
		if !e.ProgressAt.IsZero() {
			x["last_progress_at"] = e.ProgressAt.Format(time.RFC3339)
		}
		out["execution"] = x
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
