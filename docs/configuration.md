# 两层配置：base 与 config

日期：2026 年 11 月 17 日。状态：配置设计及示例，加载器和业务插件尚未实现。

## 日常维护只修改 config.yaml

配置参考 Agent Harness 的两层模型，运行时仅有两层：

| 层 | 文件 | 维护范围 |
| --- | --- | --- |
| L0 | [base.yaml](../base.yaml) | 随应用发布的完整默认插件图、主要能力、全局默认值、默认流程及固定依赖，由项目开发维护 |
| L1 | [config.yaml](../config.yaml) | 部署环境、地址、凭据引用、必要参数与插件替换，由日常维护者维护；同实例 ID 覆盖 L0 |

普通部署只需关注 config.yaml 中的 S3、身份服务地址和浏览器来源，以及部署环境的凭据。base 已配置默认端口、SQL 驱动示例、凭据引用和标准任务流程。不要为部署复制整个 base，升级时保留 config 并与新 base 合并。

本仓库 config.yaml 只有示例地址，不含真实凭据，不能直接用于生产。所需环境引用：TASKLOOM_SQL_DSN、TASKLOOM_S3_CREDENTIAL、TASKLOOM_IDENTITY_CREDENTIAL。S3 凭据引用的具体结构由凭据适配器在实现时确定，示例不假设任意字符串都能作为有效凭据。

## 合并和构建

拟复用 AgentKit/PluginKit 已有配置识别与构建能力，不新建一套 DI 或任意合并语言。参考本地 Harness 的 `docs/harness-agent-config.md` 和 AgentKit 的 `config/loader.go`、`config/merge.go`；Taskloom 固定选取 base 与 config 两个输入，不照搬 Harness 的业务节点或可选第三方能力。

1. 同实例 ID，L1 未写 use 或 use 与 L0 相同：递归合并 config/deps，未覆盖字段继承。
2. 标量覆盖；列表整体替换，不按索引合并。例如 states、flows、modules 的覆盖必须给出完整目标列表。
3. L1 明确改变 use：整节点替换，必须提供新实现所需 config/deps；不能继承旧插件专有字段。
4. 顶层新 ID 可增加插件；null 删除字段/禁用实例的高级行为沿用现有 resolver，并对保留的必要依赖校验。必需能力缺失不能静默降级。常规示例不使用 null、extends 或列表增删语法。
5. 从 server 根构建最终实例图，校验类型、缺失引用和环。不可达节点不启动，不能把仅声明未挂载误认为已经启用。
6. 校验 defaultFlow 引用、状态顺序、API 冲突、凭据引用、当前版本兼容性及资源生命周期；敏感配置诊断脱敏。

部署 config 和流程片段应先人工整理成一个 L1 文件。运行时不再叠加第三层场景配置，也不提供多个文件的隐式覆盖顺序。当前示例的静态合并校验不是已经实现的加载器或 PluginKit 构建验证。

## 不同流程示例

- [标准流程片段](../examples/flows/standard.config.yaml)：待处理 → 执行中 → 待验收 → 已完成。base 已包含，普通部署无需重复写。
- [无需独立验收片段](../examples/flows/no-review.config.yaml)：待处理 → 执行中 → 已完成。将片段里的新增/覆盖节点并入 config 即可。

无需验收示例增加独立 FlowRef，并将完成检查器设为无需验收；仍检查正式交付和其他完成条件。它保留标准流程供旧任务使用，仅改变新任务的默认流程。列表 flows 覆盖时保留原流程引用，不能使已有任务找不到旧定义。后续调整既有流程行为须增加版本，不能仅重排相同版本的列表。

两个示例是独立的 L1 编辑参考，不是同时加载的额外层；部署连接字段仍保留在同一个 config 中。

## 保留插件边界，减少配置负担

- 默认交付使用 delivery/reply，正文复用会话；无额外交付表单或逐项回应开关，特殊任务才注入检查子插件，详见[交付契约](reply-delivery.md)。
- 保留领域、状态子插件、完成检查器、Hook、SQL/S3、API 模块及 WebSocket 边界；显式 deps 留在 base，不藏进万能 services 容器。
- 删除站内通知的空 channel、sender 和 sender worker：通知插件直接落库收件箱，客户端经共用 API/WebSocket 获取。未来接外部渠道再增加实际投递能力。
- 人的负责人、首版单父树、每任务最多一个有效执行、未授权拒绝、启动校验 schema 是明确规则，不再暴露只有单一合法值的开关。
- API 模块声明固定相对路径，HTTP 统一 apiPrefix，避免每个模块都配置 /v1 前缀。
- 默认 Hook 在自身实现中声明事件和影响范围，Config 为空；修改默认语义应替换清晰的 Hook 子插件，而非维护大段路由 YAML。
- 只展示默认标准流程；其他流程独立放 examples，不让主图承担所有能力演示。
- WebSocket 是 HTTP 的受管路由模块，HTTP 拥有其打开、关闭及连接清理，根资源列表只列 SQL/S3，避免重复登记和漏配。
- 保留可复用的 event/worker 及材料维护组件，消费进度、失败处理与启停 owner 仍明确；不为了少几个节点合并不同职责。

## 未展开的默认值

以下默认值是实现目标，用于补全省略字段，并非已经运行验证的性能指标。Config 仍允许覆盖有实际意义的参数；固定不变量不接受同名开关。

| 插件 | 未配置时的目标默认值 |
| --- | --- |
| storage/sql | maxOpenConns=20、maxIdleConns=5、connMaxLifetime=30m；schema 必须兼容，否则启动失败 |
| storage/s3 | pathStyle=false、requestTimeout=30s |
| identity/existing | timeout=10s |
| transport/http | listen=:8080、apiPrefix=/v1、readTimeout=30s、writeTimeout=60s |
| transport/websocket | 相对 path=/events/ws、heartbeatInterval=30s、idleTimeout=90s、maxPendingMessages=256、maxSubscriptions=20；浏览器 allowedOrigins 必须明确配置，默认不放行任意来源 |
| event/worker | batchSize=100、retryBackoff=5s、maxAttempts=8；consumerID 必填且在启用图中唯一 |
| task/hook-flow | Config 为空，事件声明及影响范围由默认实现给出 |
| api/* | Config 为空，相对路由由模块声明 |

其余仍在 base 中展开的领域配置表示默认产品组合，例如流程顺序和验收策略；日常维护只在 config 中覆盖确实需要改变的部分。

## 验证边界

静态检查覆盖 base/config 的解析和合并、引用、无环与根可达、标准/无需验收流程、已删除节点、API 路由前缀以及示例文档链接。实现后还需通过真实 loader 和 PluginKit 构建，验证类型、生命周期、事务、授权与网络行为。
