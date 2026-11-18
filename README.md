# Taskloom

面向人与 Buddy 协作的任务系统：人负责，Buddy 执行，以可追溯的任务、commit 时间线和结果证据组织工作。名称暂定，未核验商标或域名。

## 范围与架构

只建设任务系统，复用已有管理平台及 Agent 运行能力，不建设模型推理、工具执行、上下文组装或远端派发。

采用模块化单体：接口清晰的模块通过 Deps 与配置组合，共享一个 SQL 事务边界。所有流程步骤统一为 State，任务绑定版本化流程定义与策略；同任务结果/评审/阶段原子更新，跨任务影响与通知异步处理。新增流程步骤和 API 通过新实现及配置接入，不修改原消费者。权威设计见[架构](docs/architecture.md)。

## 当前状态

- 已实现：Go/PluginKit 底座；M03 GORM/SQLite 存储、共享事务、身份、幂等、审计/outbox 与管理员 CLI；M04 任务/commit 工作台；M05 本地执行、结果提交、验收、取消及安全退回/重开。
- 根 base/config 为 22 实例的 SQLite 工作台图（见 [M05 运行图](docs/m05-dependencies.md)），`examples/minimal` 保留纯 health 图；health 不表示数据库或业务就绪。
- 未实现：Harness 适配与真实 Agent 联调（下一步 M06）、WebSocket、文件/S3、任务树、提醒与历史恢复。本地测试不代表真实进程清理或生产数据库验收。
- 本地工作台：`http://127.0.0.1:8080/v1/app`，初始化与启动见 [M04](docs/m04-workspace.md)，执行与验收见 [M05](docs/m05-workflow.md)。
- 检查入口：`sh scripts/check.sh all`、`sh scripts/check.sh race`，见[开发说明](docs/development.md)。

## 文档索引

| 主题 | 文档 |
| --- | --- |
| 设计 | [架构](docs/architecture.md)、[核心契约 C01—C18](docs/core-contracts.md)、[状态模块与流程](docs/task-flow-plugins.md)、[Commit 与结果交付](docs/reply-delivery.md) |
| 边界与接口 | [插件契约](docs/plugin-contracts.md)、[cap 能力清单](docs/cap-interfaces.md)、[插件依赖审查](docs/plugin-inventory-review.md)（跨阶段规划） |
| 协议与权限 | [共用 API 与变化流](docs/shared-api-and-realtime.md)、[独立接入契约](docs/integration-contract.md)、[成员与任务权限](docs/permissions.md) |
| 配置与工程 | [两层配置](docs/configuration.md)、[开发说明](docs/development.md) |
| 阶段实现 | [M03 存储](docs/m03-storage.md)、[M04 工作台](docs/m04-workspace.md)、[M05 流程闭环](docs/m05-workflow.md)、[开发清单](todo.md) |
| 需求与参考 | [产品需求](docs/product-requirements.md)、[阶段覆盖](docs/requirements-coverage.md)、[外部参考](docs/open-source-references.md)、[Nex V2 参考](docs/nex-v2-references.md) |

分期：第一阶段薄界面与真实 Buddy 文本闭环，第二阶段文件/任务树协作，第三阶段历史恢复与治理。
