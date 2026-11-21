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
	"strings"
	"time"
	"unicode/utf8"

	az "github.com/lengzhao/taskloom/cap/authorization"
	"github.com/lengzhao/taskloom/cap/commit"
	capfile "github.com/lengzhao/taskloom/cap/file"
	"github.com/lengzhao/taskloom/cap/fault"
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
	case errors.Is(err, fault.ErrTooLarge):
		return "文件超过大小上限。"
	case errors.Is(err, fault.ErrInput):
		return "输入无效：检查参数（正文、文件、链接不能全为空；file_id 须来自本任务；链接须为 http(s)；文件名不能为空；文件不能超过上限；子任务的 owner 须为空间内有效用户）。"
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
	descFiles = "可选：要附上的 file_id，多个用逗号分隔（upload_file 返回或 read_task 的 files 中列出）"
	descLinks = "可选：要附上的链接，每行一个：URL，后面可空格加标题；只保存链接，不保存页面内容"
)

func toolList() []tool {
	return []tool{
		{"inbox", "列出当前等你处理的任务（下一步在等你这个执行 Agent）。", schema(nil, nil)},
		{"read_task", "读取任务：目标、验收标准、状态、下一步等谁、你能做哪些操作及不能做的原因、待回答问题、当前结果和最近时间线，并返回写操作所需的 basis。", schema([]string{"task_id"}, map[string]string{"task_id": descTask})},
		{"list_members", "查找空间内的有效成员（用户与 Agent），用于 ask_owner 的 to。按 ID 片段搜索，按 subject 排序分页：返回 next 时，把它作为 after 取下一页。", schema(nil, map[string]string{"query": "可选：ID 中包含的文字", "kind": "可选：human 或 agent", "after": "可选：上一页返回的 next"})},
		{"start_work", "认领并开始执行任务。", schema([]string{"task_id", "basis"}, map[string]string{"task_id": descTask, "basis": descBasis})},
		{"post_message", "在任务时间线留言：汇报进度、回复人的引导，可附文件和链接。留言会更新你的最近进展时间，但不算交付结果。text、files、links 至少一项。", schema([]string{"task_id"}, map[string]string{"task_id": descTask, "text": "留言内容", "files": descFiles, "links": descLinks})},
		{"ask_owner", "提问或请求协助（说明问题、为何卡住、可选方案和建议）。默认问任务负责人；需要别人协助时用 to 指定空间内任一有效成员（用 list_members 查找）；对方不在任务中时会自动加为参与者。问题未回答前任务不能完成；回答会出现在 read_task 的时间线中。", schema([]string{"task_id", "basis", "question"}, map[string]string{"task_id": descTask, "basis": descBasis, "question": "问题内容", "to": "可选：被提问人的 ID（list_members 返回的 subject）；留空为负责人"})},
		{"answer_question", "回答别人向你提的问题（read_task 的 open_questions 中 for_you 为 true 的那条）。", schema([]string{"task_id", "question_id", "text"}, map[string]string{"task_id": descTask, "question_id": "问题 ID（open_questions 中的 id）", "text": "回答内容"})},
		{"close_question", "撤回你提的、已不再需要回答的问题（open_questions 中 asked_by 为你的那条），须写明理由；撤回后它不再阻止任务完成。", schema([]string{"task_id", "question_id", "reason"}, map[string]string{"task_id": descTask, "question_id": "问题 ID（open_questions 中的 id）", "reason": "为什么不再需要回答"})},
		{"submit_result", "明确提交本轮的正式结果（交付物），可带文件和链接。需要已认领执行；再次提交会替换当前结果。result、files、links 至少一项。", schema([]string{"task_id", "basis"}, map[string]string{"task_id": descTask, "basis": descBasis, "result": "结果正文", "files": descFiles, "links": descLinks})},
		{"create_subtask", "把你正在执行的任务拆出一个子任务，交给别人并行处理。需要你持有该任务当前有效的执行（已 start_work）。required 为 true 时，本任务要等该子任务完成才能提交结果；你的执行仍然有效，可以继续做你自己的部分。子任务负责人默认是本任务负责人，也可以用 owner 指定空间内任一有效用户（list_members 且 kind 为 human）；由子任务负责人决定交给谁处理。你会成为子任务的参与者，可用 read_task 查看进度。", schema([]string{"task_id", "title"}, map[string]string{"task_id": "你正在执行的任务 ID（子任务的父任务）", "title": "子任务标题", "goal": "子任务目标", "criteria": "子任务验收标准", "required": "可选：true 表示本任务必须等它完成才能提交；默认 false", "owner": "可选：子任务负责人的 ID（用户，不能是 Agent）；留空为本任务负责人"})},
		{"upload_file", "把一个文件上传到任务，返回 file_id，供 submit_result 或 post_message 的 files 引用；只上传不会出现在时间线中。text 与 content_base64 二选一。经本工具上传的内容上限约 700KB，更大的文件用 HTTP POST /v1/tasks/{id}/files 上传（同一凭据）。", schema([]string{"task_id", "name"}, map[string]string{"task_id": descTask, "name": "文件名，如 report.md", "text": "文本文件的内容（UTF-8）", "content_base64": "二进制文件内容的 base64", "media_type": "可选：如 text/markdown、image/png；留空按文件名推断"})},
		{"read_file", "读取任务中的一个文件（read_task 的 files 中列出）。UTF-8 文本直接返回内容，其他文件返回 base64；超过 512KB 时只返回元数据和 HTTP 下载路径。", schema([]string{"task_id", "file_id"}, map[string]string{"task_id": descTask, "file_id": "文件 ID"})},
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
		all, err := m.d.Workflow.Inbox(ctx, c.token, nil)
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
		if err := required(args, "task_id"); err != nil {
			return "", err
		}
		fileIDs, links, err := attachments(args)
		if err != nil {
			return "", err
		}
		ref, err := m.d.Comments.Comment(ctx, c.token, c.key(name, args), workspace.Comment{TaskID: args["task_id"], Body: args["text"], Files: fileIDs, Links: links})
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
	case "create_subtask":
		if err := required(args, "task_id", "title"); err != nil {
			return "", err
		}
		req := false
		switch strings.TrimSpace(args["required"]) {
		case "", "false":
		case "true":
			req = true
		default:
			return "", invalid("required 只能是 true 或 false")
		}
		ref, err := m.d.Comments.Create(ctx, c.token, c.key(name, args), workspace.Create{Title: args["title"], Goal: args["goal"], Criteria: args["criteria"], ParentID: args["task_id"], Owner: args["owner"], Required: req})
		if err != nil {
			return "", err
		}
		msg := "已创建子任务；由子任务负责人安排处理人。"
		if req {
			msg = "已创建必需子任务；它完成前本任务不能提交结果，你的执行仍然有效，可继续做你的部分。"
		}
		return pretty(map[string]any{"message": msg, "subtask_id": ref.TaskID}), nil
	case "upload_file":
		return m.uploadFile(ctx, c, args)
	case "read_file":
		if err := required(args, "task_id", "file_id"); err != nil {
			return "", err
		}
		return m.readFile(ctx, c, args["task_id"], args["file_id"])
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
		fileIDs, links, err := attachments(args)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(args["result"]) == "" && len(fileIDs) == 0 && len(links) == 0 {
			return "", invalid("缺少参数 result（或 files、links）")
		}
		if b.ExecutionID == "" {
			return "", invalid("还没有执行：先 start_work 认领，再用它返回的 basis 提交")
		}
		r, err := m.d.Results.Submit(ctx, c.token, key, work.Submit{TaskID: b.TaskID, ExecutionID: b.ExecutionID, Body: args["result"], Files: fileIDs, Links: links})
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

// attachments parses the comma-separated files and line-separated links
// arguments; the commands validate them again.
func attachments(args map[string]string) ([]string, []commit.Link, error) {
	var ids []string
	for _, id := range strings.Split(args["files"], ",") {
		if id = strings.TrimSpace(id); id != "" {
			ids = append(ids, id)
		}
	}
	var links []commit.Link
	for _, line := range strings.Split(args["links"], "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		url, title, _ := strings.Cut(line, " ")
		links = append(links, commit.Link{URL: url, Title: strings.TrimSpace(title)})
	}
	if attachment.Valid(ids, links) != nil {
		return nil, nil, invalid("files 或 links 无效：file_id 用逗号分隔（最多 20 个），链接每行一个且须以 http:// 或 https:// 开头（最多 20 个）")
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

func (m *Module) uploadFile(ctx context.Context, c call, args map[string]string) (string, error) {
	if err := required(args, "task_id", "name"); err != nil {
		return "", err
	}
	text, encoded := args["text"], args["content_base64"]
	if (text == "") == (encoded == "") {
		return "", invalid("text 与 content_base64 须且只能给一个")
	}
	content := []byte(text)
	mediaType := args["media_type"]
	if encoded != "" {
		raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
		if err != nil {
			return "", invalid("content_base64 不是有效的 base64")
		}
		content = raw
	}
	if mediaType == "" {
		mediaType = mime.TypeByExtension(path.Ext(args["name"]))
		if mediaType == "" && encoded == "" {
			mediaType = "text/plain; charset=utf-8"
		}
	}
	f, err := m.d.Files.Upload(ctx, c.token, c.key("upload_file", args), capfile.Upload{TaskID: args["task_id"], Name: args["name"], MediaType: mediaType}, bytes.NewReader(content))
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
