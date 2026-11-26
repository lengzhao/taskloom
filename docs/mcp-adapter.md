# MCP 接入

`api/mcp` 是随主服务启动的协议适配插件，在 `POST /v1/mcp` 提供 MCP（Streamable HTTP，只返回 JSON，不开 SSE 流），让 Claude Code、Codex、Cursor 等 Agent 只配 URL 和业务凭据即可接入。它与 `api/workflow` 一样只做协议转换：授权、版本、执行写资格和幂等全部由同一套命令按该 Agent 的业务凭据校验，不是 Agent 专用业务接口。插件无会话状态、不启动后台工作，由 HTTP owner 挂载。

## 接入

Agent 的业务凭据由 `taskloom-admin -mode enroll-agent` 或管理员在工作台签发；人也可以用管理员签发给自己的凭据接入，以本人身份工作（人工执行不绑定凭据）。以 Claude Code 为例：

```sh
claude mcp add --transport http taskloom http://localhost:8899/v1/mcp \
  --header "Authorization: Bearer <Agent 业务凭据的 Secret>"
```

请求先经 HTTP 传输层对所有路由统一的检查：带 Origin 时必须等于 `http.config.origin`（防止网页跨站调用），无 Authorization 的写入须带 `X-Taskloom-Request`；Agent 用 Bearer 凭据调用。`initialize` 返回 `Mcp-Session-Id`，只用于派生幂等键；缺失时按凭据派生。批量 JSON-RPC 不支持。

## 工具

| 工具 | 对应命令 | 说明 |
| --- | --- | --- |
| inbox | workflow.List（mine，每页 50） | 等我处理的任务，每行带 step 与 waiting（如“验收未通过，等待重新提交”）；还有下一页时返回 `{tasks, next_cursor}`，用 cursor 续读；不含阻塞任务（Agent 只看到现在能做的事，阻塞任务用 read_task 查看，其中 `waiting_on_subtasks` 为 true） |
| read_task | workflow.Read（最新一页）+ file.Queries.List | 你是谁（you）、目标、标准、标签名称（tags）、状态、等谁、can/blocked 原因、待答问题（for_you 标出问你的）、已在任务中的其他参与者（participants）、当前结果、最近进展时间、可读的父任务与子任务、最近 20 条时间线（每条带 id、文件名与链接）及 basis；更早的记录存在时返回 `earlier_before`，作为 `before` 传入向前翻页 |
| start_work | ExecutionCommands.Open | 认领执行 |
| post_message | workspace.Comment | 留言：进度、回复人的引导，可带 files（file_id 字符串数组）与 links（`{url, title}` 对象数组）；记录最近进展，不算交付 |
| list_people | workspace.People | 空间内有效成员（subject、kind），可按 ID 片段与 kind 过滤，按 subject 分页（next/after，每页 50）；供 ask 的 `to` 与 create_task 的 owner/assignee 选人 |
| ask | QuestionCommands.Ask | 提问或请求协助：默认问负责人，`to` 可填任一有效成员（不在任务中时自动加为参与者）；未答前任务不能完成 |
| answer_question | QuestionCommands.Answer | 回答别人向自己提的问题（open_questions 中 for_you 为 true） |
| close_question | QuestionCommands.Close | 撤回自己提的、已不需要回答的问题（须写理由），撤回后不再阻止完成 |
| submit_result | ResultCommands.Submit | 明确提交结果；result、files、links 至少一项 |
| create_task | workspace.Create | 新建任务：title 必填；不带 parent_task_id 为顶层任务（负责人默认为你的所属人），带上则把你正在执行的该任务拆出子任务（须已 start_work，负责人默认为该任务负责人，可带 required 布尔值）；可带 goal、criteria、owner（有效用户）、acceptance（manual/none，默认 none）、assignee（同事务分配的处理人，可分配范围由部署配置决定；分配给 Agent 后仍须认领）、tags（标签名称数组，不区分大小写解析为标签 ID；未知名称提示用 list_tags，已归档或超过上限由创建命令拒绝）；自己不是负责人时成为参与者，必需子任务不使自己的执行失效，见[任务拆分](core-contracts.md#任务拆分) |
| list_tags | catalog.Tags | 空间中未归档的标签（name、description）；空时提示可用 ask 请管理员导入推荐标签；供 create_task 的 tags 选择；标签只能在创建时选，创建后由负责人或管理员修改，规则见[层级与标签](core-contracts.md#层级与标签) |
| reassign | ExecutionCommands.Assign | 改派给他人（to）：调用者须是当前处理人或当前这次分配的派送人，规则见[分配、认领与转交](task-flow.md#分配认领与转交)；原执行立即失效 |
| upload_file | file.Commands.Upload | 上传 text 或 content_base64（MCP 请求上限 1MiB，约 700KB 内容），返回 file_id；只上传不进入时间线，更大的文件用 HTTP `POST /v1/tasks/{id}/files` |
| read_file | file.Queries.Open | 读取文件：UTF-8 文本直接返回，其他 base64；超过 512KB 只返回元数据与下载路径 |

首次分配、验收、取消与重开不对模型开放：插件以局部窄接口注入，只拿到上表能力，这些都属于人；Agent 只能改派自己处理或自己派出的任务。

## 约束

- **参数类型**：工具参数按 JSON Schema 声明的类型传：文本为字符串，`required` 为布尔值，`files`、`links` 为数组；类型不符直接报错，不做字符串解析。
- **错误**：工具错误以 `[code]` 开头（与 HTTP 错误的 `Code` 同一套），随后是说明、出错字段和下一步建议，模型可按 code 决定是否 read_task 后重试。

- **basis**：read_task 和每次写操作返回不透明 basis（任务、轮次、依据版本、执行 ID），写操作须原样带回；不会替模型取最新版本写入，过期视图得到冲突和“先 read_task”的提示。
- **幂等**：幂等键由会话、JSON-RPC 请求 id、工具与参数派生：客户端原样重发同一请求不会重复写入；新的调用即使正文相同（如重复的进度汇报）也是新写入。缺少会话时按凭据派生，客户端重启后请求 id 从头计数，正文也相同的调用会被当作重发。
- **进展而非保活**：执行没有租约或超时。认领、本人留言、提问、提交结果都会更新执行的最近进展时间，负责人据此判断是否介入；instructions 提示模型长时间工作时每 10～15 分钟汇报一次。Agent 重启后用同一业务凭据（同实例）read_task 即可继续原执行，不新建执行。沉默或断线不改变执行。
- **推送**：不订阅变化流；模型通过 inbox/read_task 按需读取，人的留言与回答在下次读取时可见。实时 steer 仍归 Agent 层。

验证见 `plugins/api/mcp/mcp_test.go`：以真实默认配置图和 HTTP 路由运行，覆盖初始化与会话、成员目录、认领、损坏/过期 basis、留言幂等与进展记录、提问与回答、Agent 回答被问的问题、提交、文件上传/交付/读取，以及通知、未知方法、批量、坏凭据和外部 Origin。

## 示例：AgentKit 常驻执行 Agent

常驻执行 Agent 由 AgentKit 提供（v0.4.48 起）：平台插件 `platform/taskloom`（AgentKit 仓库 `runtime/platform/taskloom`）与 preset `presets/taskloom.yaml`，用 AgentKit 自带的 `cmd/agent` 运行，本仓库不再包含 Agent 代码。该 preset 只开放 Taskloom MCP 工具与 todo/finish，没有 shell、文件或网络工具，所有写入仍由 Taskloom 鉴权；提示词与模型在 preset 中，可用自己的 overlay 覆盖。

`platform/taskloom` 是 Agent 层的适配，不是 Taskloom 服务端插件，也不新增 Taskloom 接口：它用该 Agent 自己的业务凭据只读 `GET /v1/me`、`/v1/changes`、`/v1/tasks?mine=true` 与 `/v1/tasks/{id}`，决定何时唤醒或 steer 会话；唤醒、退避与重启行为以 AgentKit 文档为准。这几个接口的字段与语义、以及 MCP 工具名（preset 提示词引用 `read_task`、`finish` 等）是跨仓库契约，变更时须同步 AgentKit。适配插件不主动停止 turn：取消或改派后旧执行的写入被拒，由 Agent 读任务后结束；给 Agent 开放外部工具时须重新评估。

```sh
# 1. 为 Agent 签发业务凭据（一张凭据即一个运行实例，30 天），不打印；
#    写入 git 忽略的 deploy/agent/taskloom.env 与 deploy/agent/mcp.json
#    Taskloom 不在本机 8899 端口时先设 TASKLOOM_API_URL=http://<host>:<port>/v1
sh deploy/agent/issue-credential.sh
# 2. 把 mcp.json 中的 taskloom 条目放进 ~/.agentkit/mcp.json（已有该文件时合并 mcpServers，不要覆盖）
# 3. 在 AgentKit 仓库（v0.4.48 起）运行；模型 Key 按 preset 说明设置（默认 DEEPSEEK_API_KEY）
set -a; . /path/to/taskloom/deploy/agent/taskloom.env; set +a
go run ./cmd/agent -config presets/taskloom.yaml
```

适配插件只从 `TASKLOOM_AUTHORIZATION`（可由 `authorizationEnv` 改名）读取凭据，不进配置；API 根取 `TASKLOOM_API_URL`，默认 `http://127.0.0.1:8899/v1`。
