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

`deploy/agent/` 用已有的 AgentKit 运行时（版本随 go.mod）搭一个常驻执行 Agent，入口是 `cmd/taskloom-agent`：AgentKit 原生 runner 加上本仓库的平台插件 `platform/taskloom`（[harness/agentkit](../harness/agentkit/platform.go)）。只开放 Taskloom MCP 工具与 todo/finish，没有 shell、文件或网络工具，所有写入仍由 Taskloom 鉴权。模型走 OpenAI 兼容网关（默认 deepseek-v4.1-flash）。

`platform/taskloom` 是 Agent 层的适配，不是 Taskloom 服务端插件，也不新增 Taskloom 接口：它用该 Agent 自己的业务凭据只读 `GET /v1/changes`、`/v1/inbox`、`/v1/tasks/{id}` 与 `/workflow`，决定何时唤醒、steer 或停止 AgentKit 会话：

- **按任务分会话**：每个任务是一个 channel（会话 ID 含任务 ID），历史、steer 与停止互不影响；`runner.maxConcurrentTurns` 控制并行任务数。
- **唤醒**：任务首次出现在 inbox（下一步等这个 Agent）时唤醒一次，之后只在其他人写入该任务时再唤醒，消息里带上这些新 commit 摘要；Agent 自己的写入从不唤醒自己（避免自回复）。等别人期间（问负责人、待验收）他人写入不唤醒，回到 inbox 时一并带上。阻塞任务（等待必需子任务）不唤醒。
- **运行中新消息**：任务会话正在运行时的唤醒由 AgentKit runner 折叠进当前 turn（steer）。
- **停止**：会话运行中任务被取消、处理人被换成别人或 Agent 失去读取权限时发送 `/stop`。Taskloom 此前已让该执行失去写资格；停止只为节省工作，不证明外部工具已停止。等别人或待验收不停止。
- **断线与重启**：Taskloom 不可达时按 pollSeconds 起指数退避（上限 maxBackoffSeconds），不调用模型，恢复后用原游标补拉；游标被拒（换库/恢复备份）时从 inbox 重新同步。进程重启不保存任何状态：inbox 中每个任务唤醒一次，Agent 用同一凭据 read_task 继续原执行。凭据被拒按最大退避记错误日志，需运维换凭据后重启。

```sh
# 1. 让两个容器在同一网络（taskloom 重建时用 --network taskloom-net 启动即可）
docker network create taskloom-net
docker network connect taskloom-net taskloom
# 2. 为 Agent 签发业务凭据（一张凭据即一个运行实例，30 天），写入 git 忽略的 deploy/agent/taskloom.env，不打印
sh deploy/agent/issue-credential.sh
# 3. 复制 deploy/agent/.env.example 为 deploy/agent/.env，自行填入模型 Key
# 4. 在仓库根目录构建并启动
docker build --build-arg GOPROXY="$(go env GOPROXY)" --build-arg GOSUMDB="$(go env GOSUMDB)" \
  -f deploy/agent/Dockerfile -t taskloom-agent:local .
docker run -d --name taskloom-agent --restart unless-stopped --network taskloom-net \
  --env-file deploy/agent/.env --env-file deploy/agent/taskloom.env taskloom-agent:local
docker logs -f taskloom-agent
```

入口脚本在容器内按环境变量生成 `~/.agentkit/mcp.json`，并由 `TASKLOOM_MCP_URL` 去掉 `/mcp` 得到适配插件的 API 根 `TASKLOOM_API_URL`（可单独设置）。AgentKit 的 `${env:X}` 在变量存在时保留为运行期引用 `env:X`，普通字段不会再解析它；需要直接取值的字段（如 baseUrl）用 `${var:X}`；凭据只经 `authorizationEnv` 指定的环境变量读取，不进配置。`taskloom-agent -check` 只构建配置图并退出。
