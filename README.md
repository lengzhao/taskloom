# Taskloom

面向人与 Agent（Buddy）协作的任务系统：人负责，人或 Agent 处理，以可追溯的任务、commit 时间线和结果证据组织工作。名称暂定，未核验商标或域名。

## 范围

建设任务系统，以及作为独立模块组的 skill 交付控制面（见 [Skill 交付控制面](docs/skills.md)）；复用已有管理平台及 Agent 运行能力，不建设模型推理、工具执行、上下文组装或远端派发。采用模块化单体：接口清晰的模块通过 Deps 与两层配置组合，共享一个 SQL 事务边界；只存能核实的事实，流程步骤由事实推导。

## 当前状态

- 已实现（本地 SQLite）：存储、事务、幂等与审计；人与 Agent 身份、密码登录与凭据；任务与 commit 时间线；分配、认领、提交、验收（通过/退回）、取消与重开；提问与回答；空间与项目（项目成员可见项目内任务）；负责人移交；变化补拉与工作台新待处理提醒；MCP 接入；内嵌工作台；AgentKit 适配插件（按任务唤醒/steer，已真实联调）；skill 发布、审核、检索与交付判定（[覆盖示例](examples/skills/skills.config.yaml)装配；embedding 可选，语义检索未接真实 embedding 服务验证）。
- 已实现单父任务树与必需子任务；Agent 可新建顶层任务（负责人默认为所属人，见 [C25](docs/core-contracts.md#契约验收案例)），执行中的 Agent 可拆分本任务；创建时可指定验收方式（Agent 建的任务默认 none）并同事务分配处理人（Agent 可分配范围由部署配置限定），见 [C23](docs/core-contracts.md#契约验收案例)。任务可按层级（由父子关系推导）和空间级受管标签筛选，工作台有汇总子树进度的"目标"页，见[层级与标签](docs/core-contracts.md#层级与标签)。尚未正式使用，不考虑兼容与迁移。现有功能已覆盖主要需求，暂无排期；新能力须先有真实读取方（不变量或消费者）再立项。本地测试不代表生产可用，详见[开发说明](docs/development.md#验证范围与已知限制)。

## 运行

- 本地：先用 `taskloom-admin` 初始化管理员并设置密码，再 `go run ./cmd/taskloom`，打开 `http://127.0.0.1:8080/v1/app`，步骤见[开发说明](docs/development.md#初始化与-agent-登记)。healthz 只表示进程存活。
- Docker：`docker build -t taskloom:local .`，再 `docker run -d --name taskloom -p 8899:8899 -v taskloom-data:/data taskloom:local`，访问 `http://localhost:8899/v1/app`。端口由 `TASKLOOM_PORT`（默认 8899）决定，Origin 默认 `http://localhost:端口`，可用 `TASKLOOM_ORIGIN` 覆盖；非 localhost 访问须走 HTTPS。首次启动前用同一数据卷执行 `docker run --rm -i -v taskloom-data:/data taskloom:local taskloom-admin -base /app/base.yaml -config /app/config.yaml -mode bootstrap -password-stdin ...`，从 stdin 输入管理员密码；不会生成管理员凭据文件。需要内部 Go 模块代理时传 `--build-arg GOPROXY="$(go env GOPROXY)" --build-arg GOSUMDB="$(go env GOSUMDB)"`（也支持 `GOPRIVATE`）；离线构建可先 `go mod vendor`。
- Agent：服务内置 MCP 端点 `POST /v1/mcp`，Agent 只需配置 URL 和凭据，见 [MCP 接入](docs/mcp-adapter.md)；常驻执行 Agent 由 AgentKit 的 `platform/taskloom` 与 `presets/taskloom.yaml` 提供，凭据签发见 [MCP 接入](docs/mcp-adapter.md#示例agentkit-常驻执行-agent)。
- 检查：`sh scripts/check.sh all`、`sh scripts/check.sh race`。

## 文档

| 主题 | 文档 |
| --- | --- |
| 设计 | [架构](docs/architecture.md)、[核心契约](docs/core-contracts.md)、[任务流程](docs/task-flow.md) |
| 接口 | [API 与权限](docs/api.md)、[MCP 接入](docs/mcp-adapter.md) |
| 工程 | [两层配置](docs/configuration.md)、[开发说明](docs/development.md) |

分期：第一阶段真实 Agent 文本闭环，第二阶段文件与任务树协作（均已完成）；原第三阶段的历史恢复、治理与迁移经重审不做。
