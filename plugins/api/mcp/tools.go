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

// call carries the request credential, the MCP session and the JSON-RPC
// request id used to derive idempotency keys: a client resending the same
// request reuses one key, while a new call with the same text, such as a
// repeated progress report, is a new write.
type call struct {
	token, session string
	request        json.RawMessage
}

func (c call) key(parts ...any) string {
	scope := c.session
	if scope == "" {
		sum := sha256.Sum256([]byte(c.token))
		scope = hex.EncodeToString(sum[:])
	}
	raw, _ := json.Marshal(append([]any{scope, c.request}, parts...))
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:16])
}

// basis is what a write is pinned to: the round and task basis the model read
// and its execution. It is handed to the model as an opaque token from
// read_task and returned unchanged, so a write based on a stale view fails
// with a conflict instead of silently using the latest state.
type basis struct {
	TaskID       string `json:"t"`
	RoundID      string `json:"r"`
	BasisVersion int64  `json:"b"`
	ExecutionID  string `json:"e,omitempty"`
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

// next derives the follow-up basis from what the command itself fixed, never
// from a later read that another writer may have changed. Writes never change
// the task basis version, and an execution the command did not open is kept.
func (b basis) next(r work.Ref) basis {
	n := basis{TaskID: b.TaskID, RoundID: r.RoundID, BasisVersion: b.BasisVersion, ExecutionID: b.ExecutionID}
	if r.ExecutionID != "" {
		n.ExecutionID = r.ExecutionID
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
	work.ReasonStepNotOpen:         "当前步骤还不能执行",
	work.ReasonNotYourExecution:    "当前执行不属于你的实例",
	work.ReasonExecutionFenced:     "执行已失效（取消或被替换）",
	work.ReasonRoundChanged:        "任务已进入新一轮，需要重新 read_task",
	work.ReasonBasisChanged:        "任务约定已更新，需要重新认领",
	work.ReasonNotReviewer:         "你不是验收人",
	work.ReasonChildrenPending:     "必需子任务尚未全部完成",
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
		{"list_members", "查找空间内的有效成员（用户与 Agent），用于 ask_owner 的 to。按 ID 片段搜索，按 subject 排序分页：返回 next 时，把它作为 after 取下一页。", schema(nil, map[string]string{"query": "可选：ID 中包含的文字", "kind": "可选：human 或 agent", "after": "可选：上一页返回的 next"})},
		{"start_work", "认领并开始执行任务。", schema([]string{"task_id", "basis"}, map[string]string{"task_id": descTask, "basis": descBasis})},
		{"post_message", "在任务时间线留言：汇报进度、回复人的引导。留言会更新你的最近进展时间，但不算交付结果。", schema([]string{"task_id", "text"}, map[string]string{"task_id": descTask, "text": "留言内容"})},
		{"ask_owner", "提问或请求协助（说明问题、为何卡住、可选方案和建议）。默认问任务负责人；需要别人协助时用 to 指定空间内任一有效成员（用 list_members 查找）；对方不在任务中时会自动加为参与者。问题未回答前任务不能完成；回答会出现在 read_task 的时间线中。", schema([]string{"task_id", "basis", "question"}, map[string]string{"task_id": descTask, "basis": descBasis, "question": "问题内容", "to": "可选：被提问人的 ID（list_members 返回的 subject）；留空为负责人"})},
		{"answer_question", "回答别人向你提的问题（read_task 的 open_questions 中 for_you 为 true 的那条）。", schema([]string{"task_id", "question_id", "text"}, map[string]string{"task_id": descTask, "question_id": "问题 ID（open_questions 中的 id）", "text": "回答内容"})},
		{"close_question", "撤回你提的、已不再需要回答的问题（open_questions 中 asked_by 为你的那条），须写明理由；撤回后它不再阻止任务完成。", schema([]string{"task_id", "question_id", "reason"}, map[string]string{"task_id": descTask, "question_id": "问题 ID（open_questions 中的 id）", "reason": "为什么不再需要回答"})},
		{"submit_result", "明确提交本轮的正式结果（交付物）。需要已认领执行；再次提交会替换当前结果。", schema([]string{"task_id", "basis", "result"}, map[string]string{"task_id": descTask, "basis": descBasis, "result": "结果正文"})},
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
		all, err := m.d.Workflow.Inbox(ctx, c.token)
		if err != nil {
			return "", err
		}
		// An Agent's inbox is work it can do now; blocked tasks are read when it wants them.
		items := []work.Pending{}
		for _, x := range all {
			if !x.WaitingOnSubtasks {
				items = append(items, x)
			}
		}
		if len(items) == 0 {
			return "当前没有等你处理的任务。", nil
		}
		return pretty(items), nil
	case "list_members":
		page, err := m.d.People.People(ctx, c.token, az.MemberQuery{Search: args["query"], Kind: args["kind"], After: args["after"]})
		if err != nil {
			return "", err
		}
		members := []map[string]string{}
		for _, x := range page.Items {
			members = append(members, map[string]string{"subject": x.Subject, "kind": x.Kind})
		}
		out := map[string]any{"members": members}
		if page.Next != "" {
			out["next"] = page.Next
		}
		return pretty(out), nil
	case "read_task":
		if err := required(args, "task_id"); err != nil {
			return "", err
		}
		return m.readTask(ctx, c, args["task_id"])
	case "post_message":
		if err := required(args, "task_id", "text"); err != nil {
			return "", err
		}
		ref, err := m.d.Comments.Comment(ctx, c.token, c.key(name, args), workspace.Comment{TaskID: args["task_id"], Body: args["text"]})
		if err != nil {
			return "", err
		}
		return "已留言（" + ref.CommitID + "）。", nil
	case "answer_question":
		if err := required(args, "task_id", "question_id", "text"); err != nil {
			return "", err
		}
		if _, err := m.d.Questions.Answer(ctx, c.token, c.key(name, args), work.Answer{TaskID: args["task_id"], QuestionID: args["question_id"], Body: args["text"]}); err != nil {
			return "", err
		}
		return "已回答。", nil
	case "close_question":
		if err := required(args, "task_id", "question_id", "reason"); err != nil {
			return "", err
		}
		if _, err := m.d.Questions.Close(ctx, c.token, c.key(name, args), work.CloseQuestion{TaskID: args["task_id"], QuestionID: args["question_id"], Reason: args["reason"]}); err != nil {
			return "", err
		}
		return "已撤回问题。", nil
	case "start_work", "ask_owner", "submit_result":
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
		s, err := m.d.Executions.Open(ctx, c.token, key, work.Open{TaskID: b.TaskID, BasisVersion: b.BasisVersion})
		if err != nil {
			return "", err
		}
		n := b.next(s)
		return pretty(map[string]any{"message": "已认领执行。长时间工作请定期 post_message 汇报进度。", "basis": n.token()}), nil
	case "ask_owner":
		if err = required(args, "question"); err != nil {
			return "", err
		}
		s, err := m.d.Questions.Ask(ctx, c.token, key, work.Ask{TaskID: b.TaskID, RoundID: b.RoundID, Body: args["question"], Addressee: args["to"]})
		if err != nil {
			return "", err
		}
		to := args["to"]
		if to == "" {
			to = "负责人"
		}
		return pretty(map[string]any{"message": "已向" + to + "提问；回答前任务不能完成。可继续执行，稍后用 read_task 查看回答。", "basis": b.next(s).token()}), nil
	default: // submit_result
		if err = required(args, "result"); err != nil {
			return "", err
		}
		if b.ExecutionID == "" {
			return "", invalid("还没有执行：先 start_work 认领，再用它返回的 basis 提交")
		}
		r, err := m.d.Results.Submit(ctx, c.token, key, work.Submit{TaskID: b.TaskID, ExecutionID: b.ExecutionID, Body: args["result"]})
		if err != nil {
			return "", err
		}
		s, err := m.d.Workflow.Read(ctx, c.token, b.TaskID)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"message": "结果已提交，等待负责人处理。", "next_actor": s.NextActor, "basis": b.next(r).token()}), nil
	}
}

const timelineLimit = 20

// others lists the task's participants besides the caller: who is already
// involved. Anyone in list_members may still be asked.
func others(w work.Snapshot) []string {
	out := []string{}
	for _, s := range w.Participants {
		if s != w.Viewer {
			out = append(out, s)
		}
	}
	return out
}

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
	b := basis{TaskID: d.Task.ID, RoundID: w.Position.RoundID, BasisVersion: d.Task.BasisVersion}
	if w.Execution != nil {
		b.ExecutionID = w.Execution.ID
	}
	blocked := map[string]string{}
	for action, code := range w.Blocked {
		blocked[action] = code + "：" + reasonText[code]
	}
	questions := []map[string]any{}
	for _, q := range w.Questions {
		questions = append(questions, map[string]any{"id": q.ID, "question": byID[q.CommitID].Body, "asked_by": byID[q.CommitID].Author, "asked_of": q.Addressee, "for_you": q.Addressee == w.Viewer, "asked_at": byID[q.CommitID].CreatedAt.Format(time.RFC3339)})
	}
	if len(commits) > timelineLimit {
		commits = commits[len(commits)-timelineLimit:]
	}
	timeline := []map[string]any{}
	for _, cm := range commits {
		entry := map[string]any{"kind": cm.Kind, "author": cm.Author, "at": cm.CreatedAt.Format(time.RFC3339), "text": cm.Body}
		if cm.Kind == "answer" || cm.Kind == work.KindQuestionClosed {
			entry["answers"] = byID[cm.RefID].Body
		}
		timeline = append(timeline, entry)
	}
	out := map[string]any{
		"you":                 w.Viewer,
		"task_id":             d.Task.ID,
		"title":               d.Task.Title,
		"goal":                d.Task.Goal,
		"criteria":            d.Task.Criteria,
		"owner":               d.Task.Owner,
		"status":              w.Position.Status,
		"step":                w.Position.State,
		"waiting":             w.Position.Waiting,
		"waiting_on_subtasks": w.Position.WaitingOnSubtasks,
		"next_actor":          w.NextActor,
		"assignee":            w.Assignee,
		"can":                 map[string]bool{"start_work": w.CanOpen, "submit_result": w.CanSubmit},
		"blocked":             blocked,
		"open_questions":      questions,
		"participants":        others(w),
		"timeline":            timeline,
		"basis":               b.token(),
	}
	if e := w.Execution; e != nil {
		x := map[string]any{"writable": e.Writable, "execution_id": e.ID, "agent": e.Subject}
		if !e.ProgressAt.IsZero() {
			x["last_progress_at"] = e.ProgressAt.Format(time.RFC3339)
		}
		out["execution"] = x
	}
	if w.Parent != nil {
		out["parent"] = map[string]any{"task_id": w.Parent.TaskID, "title": w.Parent.Title, "required_by_parent": w.Parent.Required, "status": w.Parent.Status}
	}
	if len(w.Children) > 0 || w.HiddenRequired {
		subtasks := []map[string]any{}
		for _, c := range w.Children {
			subtasks = append(subtasks, map[string]any{"task_id": c.TaskID, "title": c.Title, "required": c.Required, "status": c.Status, "step": c.State})
		}
		out["subtasks"] = subtasks
		if w.HiddenRequired {
			out["hidden_required_subtasks"] = true
		}
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
