# MCP 适配器

`cmd/taskloom-mcp` 是共用 HTTP 业务 API 之上的薄协议适配器（MCP stdio），让 Claude Code、Codex、Cursor 等 Agent 以工具方式接入。它不是 Agent 专用业务接口：不含业务规则、不新增服务端插件或配置节点，授权、版本、执行代次和幂等全部由服务端按该 Agent 的业务凭据校验。它属于 M06 接入的第一种形态，不替代 Harness 对保活与停止证明的职责。

## 运行

```sh
TASKLOOM_URL=http://localhost:8899/v1 \
TASKLOOM_CREDENTIAL_FILE=/path/to/worker-1-business.json \
go run ./cmd/taskloom-mcp
```

- `TASKLOOM_URL`（或 `-url`）：API 地址，含 `/v1`，默认 `http://localhost:8899/v1`。
- `TASKLOOM_TOKEN` 或 `TASKLOOM_CREDENTIAL_FILE`（或 `-credential-file`）：Agent 的 business 用途凭据，由 `taskloom-admin -mode enroll-agent -purpose business` 签发。runtime 凭据不得交给适配器。
- `-renew`：执行期间自动续期间隔，默认 20s，0 关闭。

Docker 镜像内也带有该程序，可直接经容器接入，例如 Claude Code：

```sh
claude mcp add taskloom -- docker exec -i taskloom taskloom-mcp -credential-file /data/worker-1-business.json
```

## 工具

| 工具 | 对应 API | 说明 |
| --- | --- | --- |
| inbox | GET /inbox | 等我处理的任务 |
| read_task | GET /tasks/{id}、/workflow | 目标、标准、状态、等谁、can/blocked 原因、待答问题、当前结果、最近 20 条时间线及 basis |
| start_work | POST /execution/open | 认领执行并开始自动续期 |
| post_message | POST /commits | 留言：进度、回复人的引导；不算交付 |
| ask_owner | POST /questions | 向负责人提问，未答前任务不能完成 |
| submit_result | POST /results | 明确提交结果 |
| finish_run | POST /execution/end | 报告主运行结束并停止续期 |

分配、验收、取消、退回、重开、隔离与停止证明不对模型开放：前者属于人，后者需要 runtime 凭据和真实进程证据。

## 约束

- **basis**：read_task 和每次写操作返回不透明 basis（任务、轮次、依据版本、流程修订、执行与代次），写操作须原样带回；适配器不会替模型取最新版本写入，过期视图得到 409 和“先 read_task”的提示。
- **幂等**：同一会话内相同工具与参数复用同一 Idempotency-Key，模型重试不会重复写入；因此同一会话内发送完全相同的留言只记录一次。
- **自动续期**：只说明适配器进程存活，不证明模型或子进程仍在工作，也不是停止证据。服务端拒绝（4xx）即停止续期；适配器退出时不自动报告结束，租约到期后显示 unknown，由人或 Harness 按停止规则处理。Agent 重启后新会话的 read_task 若发现当前执行仍属于自己（同实例、同代次、仍可写），会立即续期一次并接管后台续期，不新建执行；被取消、替换或换实例的执行不会被接管。
- **推送**：首版不订阅变化流；模型通过 inbox/read_task 按需读取，人的留言与回答在下次读取时可见。实时 steer 仍归 Agent 层。

验证见 `cmd/taskloom-mcp/main_test.go`：以真实默认配置图和 HTTP 路由运行，覆盖认领、过期 basis、自动续期、留言幂等、提问与回答、提交与结束。
