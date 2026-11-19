# MCP 接入

`api/mcp` 是随主服务启动的协议适配插件，在 `POST /v1/mcp` 提供 MCP（Streamable HTTP，只返回 JSON，不开 SSE 流），让 Claude Code、Codex、Cursor 等 Agent 只配 URL 和业务凭据即可接入。它与 `api/workflow` 一样只做协议转换：授权、版本、执行代次和幂等全部由同一套命令按该 Agent 的业务凭据校验，不是 Agent 专用业务接口。插件无会话状态、不启动后台工作，由 HTTP owner 挂载。

## 接入

Agent 的 business 用途凭据由 `taskloom-admin -mode enroll-agent -purpose business` 签发；runtime 凭据不得交给模型。以 Claude Code 为例：

```sh
claude mcp add --transport http taskloom http://localhost:8899/v1/mcp \
  --header "Authorization: Bearer <Agent 业务凭据的 Secret>"
```

请求经共用的 RequestSecurity：带 Origin 时必须等于配置的 origin（防止网页跨站调用），POST 须带 Authorization。`initialize` 返回 `Mcp-Session-Id`，只用于派生幂等键；缺失时按凭据派生。批量 JSON-RPC 不支持。

## 工具

| 工具 | 对应命令 | 说明 |
| --- | --- | --- |
| inbox | workflow.Inbox | 等我处理的任务 |
| read_task | workspace.Get + workflow.Read | 目标、标准、状态、等谁、can/blocked 原因、待答问题、当前结果、最近进展时间、最近 20 条时间线及 basis |
| start_work | ExecutionCommands.Open | 认领执行 |
| post_message | workspace.Comment | 留言：进度、回复人的引导；记录最近进展，不算交付 |
| ask_owner | QuestionCommands.Ask | 向负责人提问，未答前任务不能完成 |
| submit_result | ResultCommands.Submit | 明确提交结果 |
| finish_run | ExecutionCommands.End | 报告主运行结束 |

分配、验收、回答、取消、退回、重开、隔离与停止证明不对模型开放：插件以局部窄接口注入，只拿到上表能力。前者属于人，后者需要 runtime 凭据和真实进程证据。

## 约束

- **basis**：read_task 和每次写操作返回不透明 basis（任务、轮次、依据版本、流程修订、执行与代次），写操作须原样带回；不会替模型取最新版本写入，过期视图得到冲突和“先 read_task”的提示。
- **幂等**：同一会话内相同工具与参数复用同一幂等键，客户端重试不会重复写入；因此同一会话内完全相同的留言只记录一次。
- **进展而非保活**：执行没有租约或超时。认领、本人留言、提问、提交结果都会更新执行的最近进展时间，负责人据此判断是否介入；instructions 提示模型长时间工作时每 10～15 分钟汇报一次。Agent 重启后用同一业务凭据（同实例）read_task 即可继续原执行，不新建执行。沉默或断线不改变执行，也不是停止证据。
- **推送**：不订阅变化流；模型通过 inbox/read_task 按需读取，人的留言与回答在下次读取时可见。实时 steer 仍归 Agent 层。

验证见 `plugins/api/mcp/mcp_test.go`：以真实默认配置图和 HTTP 路由运行，覆盖初始化与会话、认领、损坏/过期 basis、留言幂等与进展记录、提问与回答、提交与结束，以及通知、未知方法、批量、坏凭据和外部 Origin。
