# MCP 接入

`api/mcp` 是随主服务启动的协议适配插件，在 `POST /v1/mcp` 提供 MCP（Streamable HTTP，只返回 JSON，不开 SSE 流），让 Claude Code、Codex、Cursor 等 Agent 只配 URL 和业务凭据即可接入。它与 `api/workflow` 一样只做协议转换：授权、版本、执行写资格和幂等全部由同一套命令按该 Agent 的业务凭据校验，不是 Agent 专用业务接口。插件无会话状态、不启动后台工作，由 HTTP owner 挂载。

## 接入

Agent 的业务凭据由 `taskloom-admin -mode enroll-agent` 签发。以 Claude Code 为例：

```sh
claude mcp add --transport http taskloom http://localhost:8899/v1/mcp \
  --header "Authorization: Bearer <Agent 业务凭据的 Secret>"
```

请求先经 HTTP 传输层对所有路由统一的检查：带 Origin 时必须等于 `http.config.origin`（防止网页跨站调用），无 Authorization 的写入须带 `X-Taskloom-Request`；Agent 用 Bearer 凭据调用。`initialize` 返回 `Mcp-Session-Id`，只用于派生幂等键；缺失时按凭据派生。批量 JSON-RPC 不支持。

## 工具

| 工具 | 对应命令 | 说明 |
| --- | --- | --- |
| inbox | workflow.Inbox | 等我处理的任务；不含阻塞任务（Agent 只看到现在能做的事，阻塞任务用 read_task 查看，其中 `waiting_on_subtasks` 为 true） |
| read_task | workspace.Get + workflow.Read + file.Queries.List | 你是谁（you）、目标、标准、状态、等谁、can/blocked 原因、待答问题（for_you 标出问你的）、已在任务中的其他参与者（participants）、当前结果、最近进展时间、可读的父任务与子任务、最近 20 条时间线（带文件名与链接）及 basis |
| start_work | ExecutionCommands.Open | 认领执行 |
| post_message | workspace.Comment | 留言：进度、回复人的引导，可带 files（逗号分隔 file_id）与 links（每行一个 URL，可空格加标题）；记录最近进展，不算交付 |
| list_members | workspace.People | 空间内有效成员（subject、kind），可按 ID 片段与 kind 过滤，按 subject 分页（next/after，每页 50）；供 ask_owner 的 `to` 选人 |
| ask_owner | QuestionCommands.Ask | 提问或请求协助：默认问负责人，`to` 可填任一有效成员（不在任务中时自动加为参与者）；未答前任务不能完成 |
| answer_question | QuestionCommands.Answer | 回答别人向自己提的问题（open_questions 中 for_you 为 true） |
| close_question | QuestionCommands.Close | 撤回自己提的、已不需要回答的问题（须写理由），撤回后不再阻止完成 |
| submit_result | ResultCommands.Submit | 明确提交结果；result、files、links 至少一项 |
| create_subtask | workspace.Create | 把自己正在执行的任务拆出子任务：title 必填，可带 goal、criteria、required（true/false）、owner（有效用户，默认本任务负责人）、acceptance（manual/none，默认 none）、assignee（同事务分配的处理人，可分配范围由部署配置决定；分配给 Agent 后仍须认领）；自己成为子任务参与者，必需子任务不使自己的执行失效，见[任务拆分](core-contracts.md#任务拆分) |
| upload_file | file.Commands.Upload | 上传 text 或 content_base64（MCP 请求上限 1MiB，约 700KB 内容），返回 file_id；只上传不进入时间线，更大的文件用 HTTP `POST /v1/tasks/{id}/files` |
| read_file | file.Queries.Open | 读取文件：UTF-8 文本直接返回，其他 base64；超过 512KB 只返回元数据与下载路径 |

分配、验收、取消与重开不对模型开放：插件以局部窄接口注入，只拿到上表能力，这些都属于人。

## 约束

- **basis**：read_task 和每次写操作返回不透明 basis（任务、轮次、依据版本、执行 ID），写操作须原样带回；不会替模型取最新版本写入，过期视图得到冲突和“先 read_task”的提示。
- **幂等**：幂等键由会话、JSON-RPC 请求 id、工具与参数派生：客户端原样重发同一请求不会重复写入；新的调用即使正文相同（如重复的进度汇报）也是新写入。缺少会话时按凭据派生，客户端重启后请求 id 从头计数，正文也相同的调用会被当作重发。
- **进展而非保活**：执行没有租约或超时。认领、本人留言、提问、提交结果都会更新执行的最近进展时间，负责人据此判断是否介入；instructions 提示模型长时间工作时每 10～15 分钟汇报一次。Agent 重启后用同一业务凭据（同实例）read_task 即可继续原执行，不新建执行。沉默或断线不改变执行。
- **推送**：不订阅变化流；模型通过 inbox/read_task 按需读取，人的留言与回答在下次读取时可见。实时 steer 仍归 Agent 层。

验证见 `plugins/api/mcp/mcp_test.go`：以真实默认配置图和 HTTP 路由运行，覆盖初始化与会话、成员目录、认领、损坏/过期 basis、留言幂等与进展记录、提问与回答、Agent 回答被问的问题、提交、文件上传/交付/读取，以及通知、未知方法、批量、坏凭据和外部 Origin。

## 示例：AgentKit 常驻执行 Agent

常驻执行 Agent 由 AgentKit 提供（v0.4.46 起）：平台插件 `platform/taskloom`（AgentKit 仓库 `runtime/platform/taskloom`）与 preset `presets/taskloom.yaml`，用 AgentKit 自带的 `cmd/agent` 运行，本仓库不再包含 Agent 代码。该 preset 只开放 Taskloom MCP 工具与 todo/finish，没有 shell、文件或网络工具，所有写入仍由 Taskloom 鉴权；提示词与模型在 preset 中，可用自己的 overlay 覆盖。

`platform/taskloom` 是 Agent 层的适配，不是 Taskloom 服务端插件，也不新增 Taskloom 接口：它用该 Agent 自己的业务凭据只读 `GET /v1/me`、`/v1/changes`、`/v1/inbox` 与 `/v1/tasks/{id}?after=`，决定何时唤醒或 steer 会话；唤醒、退避与重启行为以 AgentKit 文档为准。这几个接口的字段与语义、以及 MCP 工具名（preset 提示词引用 `read_task`、`finish` 等）是跨仓库契约，变更时须同步 AgentKit。适配插件不主动停止 turn：取消或改派后旧执行的写入被拒，由 Agent 读任务后结束；给 Agent 开放外部工具时须重新评估。

```sh
# 1. 为 Agent 签发业务凭据（一张凭据即一个运行实例，30 天），不打印；
#    写入 git 忽略的 deploy/agent/taskloom.env 与 deploy/agent/mcp.json
#    Taskloom 不在本机 8899 端口时先设 TASKLOOM_API_URL=http://<host>:<port>/v1
sh deploy/agent/issue-credential.sh
# 2. 把 mcp.json 中的 taskloom 条目放进 ~/.agentkit/mcp.json（已有该文件时合并 mcpServers，不要覆盖）
# 3. 在 AgentKit 仓库（v0.4.46 起）运行；模型 Key 按 preset 说明设置（默认 DEEPSEEK_API_KEY）
set -a; . /path/to/taskloom/deploy/agent/taskloom.env; set +a
go run ./cmd/agent -config presets/taskloom.yaml
```

适配插件只从 `TASKLOOM_AUTHORIZATION`（可由 `authorizationEnv` 改名）读取凭据，不进配置；API 根取 `TASKLOOM_API_URL`，默认 `http://127.0.0.1:8899/v1`。
