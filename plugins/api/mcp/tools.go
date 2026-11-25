package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/commit"
	"github.com/lengzhao/taskloom/cap/fault"
	capfile "github.com/lengzhao/taskloom/cap/file"
	"github.com/lengzhao/taskloom/cap/flow"
	"github.com/lengzhao/taskloom/cap/identity"
	work "github.com/lengzhao/taskloom/cap/workflow"
	"github.com/lengzhao/taskloom/cap/workspace"
	"github.com/lengzhao/taskloom/domain/attachment"
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

// explain turns an error into guidance the model can act on: the stable code
// first, so the model can branch on it, then what to do.
func explain(err error) string {
	var bad invalid
	if errors.As(err, &bad) {
		return string(bad)
	}
	var coded *fault.Error
	code := ""
	if errors.As(err, &coded) {
		code = coded.Code
	}
	hint := ""
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		code, hint = "unauthenticated", "凭据无效或已过期：请联系管理员重新签发 Agent 业务凭据。"
	case errors.Is(err, az.ErrForbidden):
		hint = "无权执行：该操作不属于你，或任务未分配给你。用 read_task 查看 blocked 原因。"
	case errors.Is(err, fault.ErrNotFound):
		hint = "任务或记录不存在，或你无权查看。"
	case errors.Is(err, fault.ErrConflict):
		hint = "任务已变化，当前状态不允许：先 read_task 获取最新 basis 和 blocked 原因再决定。"
	case errors.Is(err, fault.ErrTooLarge):
		hint = "内容超过大小上限。"
	case errors.Is(err, fault.ErrInput):
		hint = "输入无效，检查参数后重试。"
	default:
		return "[internal] 服务暂时不可用，可稍后重试。"
	}
	if code == "" {
		return hint
	}
	text := "[" + code + "] "
	if m := fault.Message(code); m != "" {
		text += m + "。"
	}
	if coded != nil && coded.Field != "" {
		text += "字段：" + coded.Field + "。"
	}
	return text + hint
}

type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// prop describes one tool argument; typ is a JSON Schema type.
func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

var (
	filesProp = map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": attachment.MaxFiles,
		"description": "可选：要附上的 file_id 列表（upload_file 返回或 read_task 的 files 中列出）"}
	linksProp = map[string]any{"type": "array", "maxItems": attachment.MaxLinks, "description": "可选：要附上的链接；只保存链接，不保存页面内容",
		"items": map[string]any{"type": "object", "required": []string{"url"}, "additionalProperties": false, "properties": map[string]any{
			"url": prop("string", "http 或 https 地址"), "title": prop("string", "可选：链接标题")}}}
)

func schema(required []string, props map[string]any) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	if required == nil {
		required = []string{}
	}
	return map[string]any{"type": "object", "properties": props, "required": required, "additionalProperties": false}
}

const (
	descTask  = "任务 ID"
	descBasis = "read_task（或上一次写操作）返回的 basis，原样传入"
)

func toolList() []tool {
	str := func(desc string) map[string]any { return prop("string", desc) }
	task, basis := str(descTask), str(descBasis)
	return []tool{
		{"inbox", "列出当前等你处理的任务（下一步在等你），含当前步骤和等待原因（如验收未通过等待重新提交）。任务较多时分页：返回 next_cursor 时，把它作为 cursor 取下一页。", schema(nil, map[string]any{"cursor": str("可选：上一页返回的 next_cursor")})},
		{"read_task", "读取任务：目标、验收标准、状态、下一步等谁、你能做哪些操作及不能做的原因、待回答问题、当前结果和最近时间线，并返回写操作所需的 basis。时间线只含最近 " + strconv.Itoa(timelineLimit) + " 条；返回 earlier_before 时，把它作为 before 读更早的记录。", schema([]string{"task_id"}, map[string]any{"task_id": task, "before": str("可选：上次返回的 earlier_before，读取更早的时间线")})},
		{"list_people", "查找空间内的有效成员（用户与 Agent），用于 ask 的 to 或 create_subtask 的 owner/assignee。按 ID 片段搜索，按 subject 排序分页：返回 next 时，把它作为 after 取下一页。", schema(nil, map[string]any{"query": str("可选：ID 中包含的文字"), "kind": map[string]any{"type": "string", "enum": []string{"human", "agent"}, "description": "可选：只列用户或只列 Agent"}, "after": str("可选：上一页返回的 next")})},
		{"start_work", "认领并开始执行任务。", schema([]string{"task_id", "basis"}, map[string]any{"task_id": task, "basis": basis})},
		{"post_message", "在任务时间线留言：汇报进度、回复人的引导，可附文件和链接。留言会更新你的最近进展时间，但不算交付结果。text、files、links 至少一项。", schema([]string{"task_id"}, map[string]any{"task_id": task, "text": str("留言内容"), "files": filesProp, "links": linksProp})},
		{"ask", "提问或请求协助（说明问题、为何卡住、可选方案和建议）。默认问任务负责人；需要别人协助时用 to 指定空间内任一有效成员（用 list_people 查找）；对方不在任务中时会自动加为参与者。问题未回答前任务不能完成；回答会出现在 read_task 的时间线中。", schema([]string{"task_id", "basis", "question"}, map[string]any{"task_id": task, "basis": basis, "question": str("问题内容"), "to": str("可选：被提问人的 ID（list_people 返回的 subject）；留空为负责人")})},
		{"answer_question", "回答别人向你提的问题（read_task 的 open_questions 中 for_you 为 true 的那条）。", schema([]string{"task_id", "question_id", "text"}, map[string]any{"task_id": task, "question_id": str("问题 ID（open_questions 中的 id）"), "text": str("回答内容")})},
		{"close_question", "撤回你提的、已不再需要回答的问题（open_questions 中 asked_by 为你的那条），须写明理由；撤回后它不再阻止任务完成。", schema([]string{"task_id", "question_id", "reason"}, map[string]any{"task_id": task, "question_id": str("问题 ID（open_questions 中的 id）"), "reason": str("为什么不再需要回答")})},
		{"submit_result", "明确提交本轮的正式结果（交付物），可带文件和链接。需要已认领执行；再次提交会替换当前结果。result、files、links 至少一项。", schema([]string{"task_id", "basis"}, map[string]any{"task_id": task, "basis": basis, "result": str("结果正文"), "files": filesProp, "links": linksProp})},
		{"create_subtask", "把你正在执行的任务拆出一个子任务，交给别人并行处理。需要你持有该任务当前有效的执行（已 start_work）。required 为 true 时，本任务要等该子任务完成才能提交结果；你的执行仍然有效，可以继续做你自己的部分。子任务负责人默认是本任务负责人，也可以用 owner 指定空间内任一有效用户（list_people 且 kind 为 human）。子任务默认不需要负责人验收（提交有效结果即完成），需要时 acceptance 填 manual。可用 assignee 同时指定处理人（可以是你自己），能指定谁由部署配置决定；指定 Agent 后它仍须 start_work 认领，指定人则对方立即开始；不指定则由子任务负责人安排。指定处理人时 goal 必填。你会成为子任务的参与者，可用 read_task 查看进度。", schema([]string{"task_id", "title"}, map[string]any{"task_id": str("你正在执行的任务 ID（子任务的父任务）"), "title": str("子任务标题"), "goal": str("子任务目标"), "criteria": str("可选：子任务验收标准"), "required": prop("boolean", "可选：true 表示本任务必须等它完成才能提交；默认 false"), "owner": str("可选：子任务负责人的 ID（用户，不能是 Agent）；留空为本任务负责人"), "acceptance": map[string]any{"type": "string", "enum": []string{"manual", "none"}, "description": "可选：manual 表示子任务结果须负责人验收；默认 none"}, "assignee": str("可选：子任务处理人的 ID（用户或 Agent，可以是你自己）")})},
		{"reassign", "把任务改派给别人（用户或 Agent），例如任务不适合你或需要别人接手。你是当前处理人，或当前这次分配是你做的时可用；改派后原执行立即失效，接手的 Agent 须 start_work 认领，接手的人立即开始。", schema([]string{"task_id", "to"}, map[string]any{"task_id": task, "to": str("新处理人的 ID（list_people 返回的 subject）")})},
		{"upload_file", "把一个文件上传到任务，返回 file_id，供 submit_result 或 post_message 的 files 引用；只上传不会出现在时间线中。text 与 content_base64 二选一。经本工具上传的内容上限约 700KB，更大的文件用 HTTP POST /v1/tasks/{id}/files 上传（同一凭据）。", schema([]string{"task_id", "name"}, map[string]any{"task_id": task, "name": str("文件名，如 report.md"), "text": str("文本文件的内容（UTF-8）"), "content_base64": str("二进制文件内容的 base64"), "media_type": str("可选：如 text/markdown、image/png；留空按文件名推断")})},
		{"read_file", "读取任务中的一个文件（read_task 的 files 中列出）。UTF-8 文本直接返回内容，其他文件返回 base64；超过 512KB 时只返回元数据和 HTTP 下载路径。", schema([]string{"task_id", "file_id"}, map[string]any{"task_id": task, "file_id": str("文件 ID")})},
	}
}

// arguments are a tool call's JSON arguments. String fields read as text;
// required, files and links keep their JSON types.
type arguments map[string]any

func (a arguments) str(name string) string {
	s, _ := a[name].(string)
	return s
}

// check rejects a value whose JSON type the schema does not allow, so a
// model learns the shape instead of having its value silently dropped.
func (a arguments) check() error {
	for name, v := range a {
		if v == nil {
			continue
		}
		switch name {
		case "required":
			if _, ok := v.(bool); !ok {
				return invalid("参数 required 须为布尔值 true 或 false")
			}
		case "files", "links":
			if _, ok := v.([]any); !ok {
				return invalid("参数 " + name + " 须为数组")
			}
		default:
			if _, ok := v.(string); !ok {
				return invalid("参数 " + name + " 须为字符串")
			}
		}
	}
	return nil
}

func required(args arguments, names ...string) error {
	for _, n := range names {
		if strings.TrimSpace(args.str(n)) == "" {
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
	args := arguments{}
	if len(rawArgs) > 0 && string(rawArgs) != "null" {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", invalid("参数必须是 JSON 对象")
		}
	}
	if err := args.check(); err != nil {
		return "", err
	}
	switch name {
	case "inbox":
		all, next, err := m.d.Workflow.List(ctx, c.token, work.ListFilter{Mine: true, Limit: inboxPage, Cursor: args.str("cursor")})
		if err != nil {
			return "", err
		}
		// An Agent's inbox is work it can do now; blocked tasks are read when it wants them.
		items := []map[string]any{}
		for _, x := range all {
			if x.ForMe {
				items = append(items, map[string]any{"task_id": x.Task.ID, "title": x.Task.Title, "next_actor": x.NextActor, "step": x.Position.State, "waiting": x.Position.Waiting})
			}
		}
		if next != "" {
			return pretty(map[string]any{"tasks": items, "next_cursor": next}), nil
		}
		if len(items) == 0 {
			return "当前没有等你处理的任务。", nil
		}
		return pretty(items), nil
	case "list_people":
		page, err := m.d.People.People(ctx, c.token, az.MemberQuery{Search: args.str("query"), Kind: args.str("kind"), After: args.str("after")})
		if err != nil {
			return "", err
		}
		members := []map[string]string{}
		for _, x := range page.Items {
			members = append(members, map[string]string{"subject": x.Subject, "kind": x.Kind})
		}
		out := map[string]any{"people": members}
		if page.Next != "" {
			out["next"] = page.Next
		}
		return pretty(out), nil
	case "read_task":
		if err := required(args, "task_id"); err != nil {
			return "", err
		}
		var before int64
		if raw := args.str("before"); raw != "" {
			n, err := strconv.ParseInt(raw, 10, 64)
			if err != nil || n <= 0 {
				return "", invalid("参数 before 须为 read_task 返回的 earlier_before")
			}
			before = n
		}
		return m.readTask(ctx, c, args.str("task_id"), before)
	case "post_message":
		if err := required(args, "task_id"); err != nil {
			return "", err
		}
		fileIDs, links, err := attachments(args)
		if err != nil {
			return "", err
		}
		ref, err := m.d.Comments.Comment(ctx, c.token, c.key(name, args), workspace.Comment{TaskID: args.str("task_id"), Body: args.str("text"), Files: fileIDs, Links: links})
		if err != nil {
			return "", err
		}
		return "已留言（" + ref.CommitID + "）。", nil
	case "answer_question":
		if err := required(args, "task_id", "question_id", "text"); err != nil {
			return "", err
		}
		if _, err := m.d.Questions.Answer(ctx, c.token, c.key(name, args), work.Answer{TaskID: args.str("task_id"), QuestionID: args.str("question_id"), Body: args.str("text")}); err != nil {
			return "", err
		}
		return "已回答。", nil
	case "close_question":
		if err := required(args, "task_id", "question_id", "reason"); err != nil {
			return "", err
		}
		if _, err := m.d.Questions.Close(ctx, c.token, c.key(name, args), work.CloseQuestion{TaskID: args.str("task_id"), QuestionID: args.str("question_id"), Reason: args.str("reason")}); err != nil {
			return "", err
		}
		return "已撤回问题。", nil
	case "create_subtask":
		if err := required(args, "task_id", "title"); err != nil {
			return "", err
		}
		req, _ := args["required"].(bool)
		mode := flow.Acceptance(strings.TrimSpace(args.str("acceptance")))
		if mode != "" && mode != flow.AcceptanceManual && mode != flow.AcceptanceNone {
			return "", invalid("acceptance 只能是 manual 或 none")
		}
		assignee := strings.TrimSpace(args.str("assignee"))
		ref, err := m.d.Comments.Create(ctx, c.token, c.key(name, args), workspace.Create{Title: args.str("title"), Goal: args.str("goal"), Criteria: args.str("criteria"), ParentID: args.str("task_id"), Owner: args.str("owner"), Required: req, Acceptance: mode, Assignee: assignee})
		if err != nil {
			return "", err
		}
		msg := "已创建子任务；"
		if req {
			msg = "已创建必需子任务，它完成前本任务不能提交结果，你的执行仍然有效，可继续做你的部分；"
		}
		if assignee == "" {
			msg += "由子任务负责人安排处理人。"
		} else {
			msg += "已分配给 " + assignee + "；Agent（包括你自己）须用 start_work 认领后开始。"
		}
		return pretty(map[string]any{"message": msg, "subtask_id": ref.TaskID}), nil
	case "reassign":
		if err := required(args, "task_id", "to"); err != nil {
			return "", err
		}
		to := args.str("to")
		if _, err := m.d.Executions.Assign(ctx, c.token, c.key(name, args), work.Assignment{TaskID: args.str("task_id"), Subject: to}); err != nil {
			return "", err
		}
		return pretty(map[string]any{"message": "已改派给 " + to + "；你在该任务上的执行（如有）已失效，停止在该任务上的工作。"}), nil
	case "upload_file":
		return m.uploadFile(ctx, c, args)
	case "read_file":
		if err := required(args, "task_id", "file_id"); err != nil {
			return "", err
		}
		return m.readFile(ctx, c, args.str("task_id"), args.str("file_id"))
	case "start_work", "ask", "submit_result":
	default:
		return "", invalid("未知工具 " + name)
	}
	if err := required(args, "task_id", "basis"); err != nil {
		return "", err
	}
	b, err := parseBasis(args.str("task_id"), args.str("basis"))
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
	case "ask":
		if err = required(args, "question"); err != nil {
			return "", err
		}
		s, err := m.d.Questions.Ask(ctx, c.token, key, work.Ask{TaskID: b.TaskID, RoundID: b.RoundID, Body: args.str("question"), Addressee: args.str("to")})
		if err != nil {
			return "", err
		}
		to := args.str("to")
		if to == "" {
			to = "负责人"
		}
		return pretty(map[string]any{"message": "已向" + to + "提问；回答前任务不能完成。可继续执行，稍后用 read_task 查看回答。", "basis": b.next(s).token()}), nil
	default: // submit_result
		fileIDs, links, err := attachments(args)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(args.str("result")) == "" && len(fileIDs) == 0 && len(links) == 0 {
			return "", invalid("缺少参数 result（或 files、links）")
		}
		if b.ExecutionID == "" {
			return "", invalid("还没有执行：先 start_work 认领，再用它返回的 basis 提交")
		}
		r, err := m.d.Results.Submit(ctx, c.token, key, work.Submit{TaskID: b.TaskID, ExecutionID: b.ExecutionID, Body: args.str("result"), Files: fileIDs, Links: links})
		if err != nil {
			return "", err
		}
		s, err := m.d.Workflow.Read(ctx, c.token, b.TaskID, 0)
		if err != nil {
			return "", err
		}
		return pretty(map[string]any{"message": "结果已提交，等待负责人处理。", "next_actor": s.NextActor, "basis": b.next(r).token()}), nil
	}
}

const timelineLimit = 20

// inboxPage bounds one inbox call; most Agents see a single page.
const inboxPage = 50

// others lists the task's participants besides the caller: who is already
// involved. Anyone in list_people may still be asked.
func others(w work.View) []string {
	out := []string{}
	for _, s := range w.Participants {
		if s != w.Viewer {
			out = append(out, s)
		}
	}
	return out
}

func (m *Module) readTask(ctx context.Context, c call, taskID string, before int64) (string, error) {
	w, err := m.d.Workflow.Read(ctx, c.token, taskID, before)
	if err != nil {
		return "", err
	}
	d := w
	// The view carries the newest page, more than the recent entries shown.
	commits := w.Commits
	listed, err := m.d.Files.List(ctx, c.token, taskID)
	if err != nil {
		return "", err
	}
	fileByID := map[string]capfile.File{}
	for _, f := range listed {
		fileByID[f.ID] = f
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
	for action, r := range w.Blocked {
		blocked[action] = r.Code + "：" + r.Message
	}
	questions := []map[string]any{}
	for _, q := range w.Questions {
		questions = append(questions, map[string]any{"id": q.ID, "question": q.Body, "asked_by": q.Asker, "asked_of": q.Addressee, "for_you": q.Addressee == w.Viewer, "asked_at": q.AskedAt.Format(time.RFC3339)})
	}
	earlier := w.Earlier
	if len(commits) > timelineLimit {
		commits, earlier = commits[len(commits)-timelineLimit:], true
	}
	timeline := []map[string]any{}
	for _, cm := range commits {
		entry := map[string]any{"id": cm.ID, "kind": cm.Kind, "author": cm.Author, "at": cm.CreatedAt.Format(time.RFC3339), "text": cm.Body}
		if q, ok := byID[cm.RefID]; ok && (cm.Kind == "answer" || cm.Kind == work.KindQuestionClosed) {
			entry["answers"] = q.Body
		}
		addAttachments(entry, cm, fileByID)
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
	if earlier && len(commits) > 0 {
		out["earlier_before"] = strconv.FormatInt(commits[0].Sequence, 10)
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
		addAttachments(r, *w.ResultCommit, fileByID)
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

// attachments reads the files and links arguments; the commands validate
// them again.
func attachments(args arguments) ([]string, []commit.Link, error) {
	var ids []string
	files, _ := args["files"].([]any)
	for _, v := range files {
		id, ok := v.(string)
		if !ok {
			return nil, nil, invalid("files 须为 file_id 字符串数组")
		}
		ids = append(ids, strings.TrimSpace(id))
	}
	var links []commit.Link
	raw, _ := args["links"].([]any)
	for _, v := range raw {
		o, ok := v.(map[string]any)
		url, _ := o["url"].(string)
		title, _ := o["title"].(string)
		if !ok || url == "" {
			return nil, nil, invalid("links 须为 {url, title} 对象数组")
		}
		links = append(links, commit.Link{URL: strings.TrimSpace(url), Title: strings.TrimSpace(title)})
	}
	if err := attachment.Valid(ids, links); err != nil {
		return nil, nil, err
	}
	return ids, links, nil
}

func addAttachments(entry map[string]any, c commit.Commit, byID map[string]capfile.File) {
	if len(c.Files) > 0 {
		list := []map[string]any{}
		for _, id := range c.Files {
			f := byID[id]
			list = append(list, map[string]any{"file_id": id, "name": f.Name, "media_type": f.MediaType, "size": f.Size})
		}
		entry["files"] = list
	}
	if len(c.Links) > 0 {
		entry["links"] = c.Links
	}
}

// mcpReadMax bounds file content returned inline; larger files are downloaded over HTTP.
const mcpReadMax = 512 << 10

func (m *Module) uploadFile(ctx context.Context, c call, args arguments) (string, error) {
	if err := required(args, "task_id", "name"); err != nil {
		return "", err
	}
	text, encoded := args.str("text"), args.str("content_base64")
	if (text == "") == (encoded == "") {
		return "", invalid("text 与 content_base64 须且只能给一个")
	}
	content := []byte(text)
	mediaType := args.str("media_type")
	if encoded != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			return "", invalid("content_base64 不是有效的 base64")
		}
		content = raw
	}
	if mediaType == "" {
		mediaType = mime.TypeByExtension(path.Ext(args.str("name")))
		if mediaType == "" && encoded == "" {
			mediaType = "text/plain; charset=utf-8"
		}
	}
	f, err := m.d.Files.Upload(ctx, c.token, c.key("upload_file", args), capfile.Upload{TaskID: args.str("task_id"), Name: args.str("name"), MediaType: mediaType}, bytes.NewReader(content))
	if err != nil {
		return "", err
	}
	return pretty(map[string]any{"message": "已上传；在 submit_result 或 post_message 的 files 中引用它才会出现在任务中。", "file_id": f.ID, "name": f.Name, "size": f.Size}), nil
}

func (m *Module) readFile(ctx context.Context, c call, taskID, fileID string) (string, error) {
	f, body, err := m.d.Files.Open(ctx, c.token, taskID, fileID)
	if err != nil {
		return "", err
	}
	defer body.Close()
	meta := map[string]any{"file_id": f.ID, "name": f.Name, "media_type": f.MediaType, "size": f.Size, "sha256": f.Digest}
	if f.Size > mcpReadMax {
		meta["message"] = "文件过大，未返回内容；用同一凭据 HTTP GET 下载"
		meta["download"] = "/v1/tasks/" + taskID + "/files/" + fileID
		return pretty(meta), nil
	}
	raw, err := io.ReadAll(io.LimitReader(body, mcpReadMax+1))
	if err != nil {
		return "", fault.ErrUnavailable
	}
	if utf8.Valid(raw) {
		meta["text"] = string(raw)
	} else {
		meta["content_base64"] = base64.StdEncoding.EncodeToString(raw)
	}
	return pretty(meta), nil
}
