# Taskloom

面向人与 Buddy 协作的任务系统：人负责，Buddy 执行，以可追溯的任务、commit 时间线和结果证据组织工作。名称暂定，未核验商标或域名。

## 范围与架构

只建设任务系统，默认采用本地身份模块，复用已有管理平台及 Agent 运行能力，不建设模型推理、工具执行、上下文组装或远端派发。

采用模块化单体：接口清晰的模块通过 Deps 与配置组合，一个 SQL 事务边界。所有流程步骤统一为状态插件，默认状态链由配置组合，任务绑定版本化流程定义与策略；同任务结果/评审/阶段原子更新，跨任务影响与通知异步处理。PluginKit 负责模块装配，新增流程步骤和 API 可通过新实现及配置接入，不修改原消费者逻辑。权威设计见 [M01 架构](docs/architecture.md)。

## 当前实现

已有 Go/PluginKit 底座，以及 M03 的 GORM 存储、共享事务、身份/授权、幂等、审计/outbox 和管理员 CLI；M04 已提供任务/commit 工作台；M05 已提供执行、结果提交、验收、取消与安全重开闭环。根 base/config 为 SQLite 工作台图，examples/minimal 保留纯 health。health 不表示数据库或任务系统就绪。`sh scripts/check.sh all`、`sh scripts/check.sh race` 为实际检查入口，见[开发说明](docs/development.md)。

## 资料入口

- [核心业务约束与 C01—C18](docs/core-contracts.md)
- [状态模块与流程控制](docs/task-flow-plugins.md)与[回复交付](docs/reply-delivery.md)
- [模块/插件边界](docs/plugin-contracts.md)与[功能模块](docs/task-system-modules.md)
- [共用 API 与变化流](docs/shared-api-and-realtime.md)与[M02 独立接入契约](docs/integration-contract.md)
- [产品需求](docs/product-requirements.md)与[阶段覆盖](docs/requirements-coverage.md)
- [两层配置](docs/configuration.md)、[M03 实现与验证边界](docs/m03-storage.md)及[开发清单](todo.md)
- [外部参考](docs/open-source-references.md)与[Nex V2 参考](docs/nex-v2-references.md)

第一阶段先交付薄界面与真实 Buddy 文本闭环，第二阶段文件/任务树协作，第三阶段历史恢复与治理。M02 已明确独立接入契约，后续由 Harness 插件适配；M03 已落实存储与安全基础，M05 已提供本地闭环，下一步 M06 真实运行时接入验收。D01/D02 为历史方案；M01 已按“插件即接口清晰、Deps 配置组合的模块”修订，不再限制业务模块只能代码装配。

具体插件清单、各接口/Deps、依赖图及尚未闭合的扩展点见[插件依赖审查](docs/plugin-inventory-review.md)；这是设计基线，非已实现业务图。

M04 本地工作台入口：`http://127.0.0.1:8080/v1/app`，初始化、显式授权及启动见[M04 使用说明](docs/m04-workspace.md)。新任务默认使用标准流程，不含 Harness；执行与验收操作见 [M05](docs/m05-workflow.md)。

权限已简化为[租户成员与任务关系](docs/permissions.md)：有效成员可创建任务，负责人或管理员管理，参与者读取/评论；无 Scope/Grant 或逐动作授权配置。

当前公共接口按真实消费者收敛，见[cap 能力清单](docs/cap-interfaces.md)：接口拆分不新增插件，根默认图为 22 个实例，不保留旧 Repository/Service 兼容接口。

M05 已提供本地执行/结果/验收/取消闭环及安全退回/重开，实际接口、配置、证明边界与验证见[M05 说明](docs/m05-workflow.md)。本地测试不代表真实 Agent/进程清理或生产数据库验收，Harness 未在本轮实现。
