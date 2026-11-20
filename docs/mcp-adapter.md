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
| read_task | workspace.Get + workflow.Read | 目标、标准、状态、等谁、can/blocked 原因、待答问题、当前结果、最近进展时间、可读的父任务与子任务、最近 20 条时间线及 basis |
| start_work | ExecutionCommands.Open | 认领执行 |
| post_message | workspace.Comment | 留言：进度、回复人的引导；记录最近进展，不算交付 |
| ask_owner | QuestionCommands.Ask | 提问或请求协助：默认问负责人，`to` 可指定已加入任务的参与者；未答前任务不能完成 |
| answer_question | QuestionCommands.Answer | 回答别人向自己提的问题（open_questions 中 asked_of 为自己） |
| submit_result | ResultCommands.Submit | 明确提交结果 |

分配、验收、取消、退回与重开不对模型开放：插件以局部窄接口注入，只拿到上表能力，这些都属于人。

## 约束

- **basis**：read_task 和每次写操作返回不透明 basis（任务、轮次、依据版本、执行 ID），写操作须原样带回；不会替模型取最新版本写入，过期视图得到冲突和“先 read_task”的提示。
- **幂等**：同一会话内相同工具与参数复用同一幂等键，客户端重试不会重复写入；因此同一会话内完全相同的留言只记录一次。
- **进展而非保活**：执行没有租约或超时。认领、本人留言、提问、提交结果都会更新执行的最近进展时间，负责人据此判断是否介入；instructions 提示模型长时间工作时每 10～15 分钟汇报一次。Agent 重启后用同一业务凭据（同实例）read_task 即可继续原执行，不新建执行。沉默或断线不改变执行。
- **推送**：不订阅变化流；模型通过 inbox/read_task 按需读取，人的留言与回答在下次读取时可见。实时 steer 仍归 Agent 层。

验证见 `plugins/api/mcp/mcp_test.go`：以真实默认配置图和 HTTP 路由运行，覆盖初始化与会话、认领、损坏/过期 basis、留言幂等与进展记录、提问与回答、Agent 回答被问的问题、提交，以及通知、未知方法、批量、坏凭据和外部 Origin。

## 示例：AgentKit 常驻执行 Agent

`deploy/agent/` 用已有的 AgentKit 运行时（v0.4.45）搭一个常驻执行 Agent：`platform/timer` 每 60 秒唤醒一次，先调 inbox 再处理分配给它的任务；只开放 Taskloom MCP 工具与 todo/finish，没有 shell、文件或网络工具，所有写入仍由 Taskloom 鉴权。模型走 OpenAI 兼容网关（默认 deepseek-v4.1-flash）。

```sh
# 1. 让两个容器在同一网络（taskloom 重建时用 --network taskloom-net 启动即可）
docker network create taskloom-net
docker network connect taskloom-net taskloom
# 2. 为 Agent 签发业务凭据（一张凭据即一个运行实例，30 天），写入 git 忽略的 deploy/agent/taskloom.env，不打印
sh deploy/agent/issue-credential.sh
# 3. 复制 deploy/agent/.env.example 为 deploy/agent/.env，自行填入模型 Key
# 4. 构建并启动
docker build --build-arg GOPROXY="$(go env GOPROXY)" --build-arg GOSUMDB="$(go env GOSUMDB)" \
  -f deploy/agent/Dockerfile -t taskloom-agent:local deploy/agent
docker run -d --name taskloom-agent --restart unless-stopped --network taskloom-net \
  --env-file deploy/agent/.env --env-file deploy/agent/taskloom.env taskloom-agent:local
docker logs -f taskloom-agent
```

入口脚本在容器内按环境变量生成 `~/.agentkit/mcp.json`（AgentKit 的 `${env:X}` 在变量存在时保留为运行期引用 `env:X`，scopedEnv 与 baseUrl 等普通字段不会再解析它；需要直接取值的字段用 `${var:X}`）。验收通过任务立即完成；任务被转交、退回或取消时，Agent 的执行立即失效，下次读写时得知并停止处理。

已知限制：AgentKit v0.4.45 的 timer 在 fresh 模式下各次唤醒的投递 ID 为 `taskloom:<启动时间>:run-N`，路由只识别 `:t:`/`:u:` 段，同一进程的所有唤醒会并入同一会话，历史随唤醒增长；示例配置以约 40k token 触发摘要压缩来限界，根治需在 AgentKit 中修正 timer 的会话 ID。空闲时 inbox 为空，每次唤醒只需 inbox→finish 两步。
