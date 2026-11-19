# 插件接口与组合契约

流程步骤统一为 State，不另设 delivery.Check、review.Coordinator 或 ReviewPolicy/Evaluator 引擎。下表为目标接口边界；当前 Go 接口以 [cap 清单](cap-interfaces.md) 为准。

| 插件能力 | 职责与接口 | Deps 方向 |
| --- | --- | --- |
| task | 目标、依据、负责人、验收人策略；Reader/Editor | 自己的 SQL 仓储 |
| commits | 不可变内容及引用；Reader/Appender | 自己的仓储；文件阶段接材料引用 |
| decisions | 结构化阻塞问题、回答与解决；Reader/Editor | 自己的仓储；正文引用 CommitID |
| execution | 分配/代次/进展/停止；Reader/Editor | 自己的仓储 |
| delivery | 提交登记、按轮次的当前候选；Reader/Register/Withdraw | 自己的仓储，不管理评审 |
| flow | 顺序推进/轮次/控制、CheckOpen、Reader | 状态列表及任务/执行安全能力，不直接读评审表 |
| State | Describe/CheckEnter/Enter/CheckComplete | 所需事实/本步骤记录，不依赖 flow/外部命令 |
| state/accepting | 人工验收规则、记录与判定；可提供 RecordVerdict | task/delivery/execution/本地可信身份及验收仓储 |
| state/fact-review | 自定义证据审核，具体规则由此插件实现 | task/delivery/本地证据，按需有自己的记录 |
| 命令模块 | 命名业务动作、授权、幂等和事务 | 对应事实/步骤动作接口及 flow |
| HTTP Module | 命名 API 协议适配 | 命令/查询接口 |
| event.Handler / Channel | 持久事件消费、独立通知渠道 | 命名业务能力/投递记录 |

命令模块不是每个状态都必须配套一份。纯本地状态无需额外命令；需要人工输入的状态可提供命名事务内能力，由薄命令/API 适配。数据结构/辅助函数不用独立插件。禁止万能 Execute(action,map)、全局服务查找、具体实现断言和 context 隐藏服务。

所有依赖通过 typed Deps 注入接口。跨插件共享事务，内部子操作保留主体/CommandID，无权单独提交。Enter 的幂等同步在当前事务完成，远端副作用走 outbox。状态可重复访问新证据，因此它负责让旧结论失效，不把此逻辑散到交付模块。

新增审核步骤：实现 State、声明 Deps、注册、配置进 states；不修改控制器/交付模块。需要新数据输入时同时提供自己的记录、API 和迁移，但不因此建设通用审核平台。Go 注册/重建不是热加载。

固定权限、依据、版本、执行隔离及停止保护不能删除；纯业务审核以步骤表达。SubmitResult 成功登记不等于通过后续步骤：审核 wait/reject 保留结果，技术失败整体回滚。完整规则见[状态契约](task-flow-plugins.md)。

E01：新增本地事实审核状态，原交付模块不改；审核未过保留交付并等待。
E02：新增需人工/异步输入的状态及其记录入口，原 flow 不改；新候选不能复用旧结论。
E03：新增 API 模块，认证/生命周期和冲突检测统一。
E04：新增通知渠道，原业务逻辑不改、逐渠道重试。

接口/Deps 清单、结构检查与未闭合边界见[依赖审查](plugin-inventory-review.md)。清单数量不作为拆分目标，超过 5 个依赖按职责和事务边界评审。
