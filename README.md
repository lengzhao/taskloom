# Taskloom

面向人与 Buddy 协作的任务系统：把目标、协作过程和结果编织成可追溯的任务网络。

Taskloom 为本项目暂定工作名，尚未核验商标、域名或名称唯一性。

## 项目范围

本项目只建设任务系统，包括独立任务会话、任务树与依赖、状态、分配与交接、执行记录、版本历史、交付验收和权限审计。

软件分发与管理平台、Agent 执行层均已有能力，本项目直接复用，通过接口对接。软件包分发、安装升级、设备与实例管理，以及模型推理、工具调用和执行上下文管理，不属于本项目建设范围。Buddy 是接入任务系统的 Agent 执行主体，不是本项目需要重建的执行层。

任务系统向人和不同职责的 Buddy 提供统一业务能力，通过操作权限和作用范围控制自主程度。

## 配置

采用 `base.yaml + config.yaml` 两层配置：base 随应用维护完整默认装配与全局值；日常维护者只需修改 config，按实例 ID 覆盖。不同流程示例位于 `examples/flows/`，作为 config 编辑片段使用。

## 开发清单

开发顺序、依赖和验收条件见 [todo.md](todo.md)。当前尚无业务实现，先收敛关键契约，再打通最小闭环。

## 文档

- [产品需求草案](docs/product-requirements.md)
- [任务系统模块划分与功能参考](docs/task-system-modules.md)
- [插件能力、Config 与 Deps 草案](docs/plugin-contracts.md)
- [默认交付：Agent 结果回复](docs/reply-delivery.md)
- [任务流转：状态子插件列表与 Hook](docs/task-flow-plugins.md)
- [共用业务 API、WebSocket 与 Agent steer](docs/shared-api-and-realtime.md)
- [两层配置说明](docs/configuration.md)
- [base：完整默认装配](base.yaml)
- [config：日常部署覆盖](config.yaml)
- [项目调研：设计优点与查缺补漏](docs/open-source-references.md)
- [Nex V2 能力对照与源码入口](docs/nex-v2-references.md)

当前处于需求与设计阶段，尚无实现代码。需求文档中的建议与待决策项不代表最终设计。

## 名称

Task 表示任务，loom 表示织机，对应将任务、参与者与过程连接成完整工作网络的产品定位。
