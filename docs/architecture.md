# Taskloom 整体设计

Taskloom 是人与 Agent 协作的任务系统。采用模块化单体：公共能力接口经 typed Deps 和配置组合，PluginKit 注册/构建；GORM 封装在 SQL 仓储内，本地 SQLite，生产配置驱动和 DSN。已有 M03 存储与安全基础；M04 已实现本地任务/commit/准备流程与薄界面闭环。Harness 适配不属于当前工作。

## 四种事实

| 模型 | 保存什么 | 不承担什么 |
| --- | --- | --- |
| Task | 当前目标、标准、负责人、依据与策略版本 | 聊天上下文、模型运行 |
| Commit | 不可变协作内容、作者归属、引用、提交顺序 | 修改状态、授权、通用动作执行 |
| 领域事实 | 执行、问题、交付、验收、文件等结构化记录 | 根据自然语言猜测正式决定 |
| Flow position | 绑定定义、轮次、状态位置、进入标识和等待原因 | 复制各领域判断规则 |

Task 与 Flow position 分表/接口归属；M04 已将位置字段移至 flow.Position，task/facts 不写状态。正文唯一保存在 Commit，结构化事实引用 CommitID。没有 Conversation、Message 或消息修订层，也不从日志重放构建整个业务数据库。

## Commit 与命名动作

普通评论、进度、提问可以追加 commit。更正追加新 commit，RefID 指向同任务旧记录，历史内容不覆盖；更正不撤回旧结果或旧决定。界面将 commits 和受权业务事实展示为时间线。

正式修改目标、提出阻塞问题、回答/解决问题、提交结果、验收、取消和重开分别是命名用例。用例认证、授权、校验版本/依据，然后同事务更新自身领域事实、追加需要的 commit、审计、outbox 和幂等回执。客户端不能通过任意 kind 或正文触发业务动作；commit 类型由对应命令决定。

普通 commit 不增加 BasisVersion，不自动交付，不改变任务终态。结果正文也是 commit，但成为当前结果必须有显式 Submission 登记。验收绑定 SubmissionID、CommitID、Basis、PolicyRevision；更正结果必须重新提交。撤回只撤销当前候选，不恢复旧结果。

## 模块与依赖

- task/facts：目标、标准、负责人和策略；自己的仓储。
- commit/immutable：追加、精确读取、提交序分页；自己的仓储，文件阶段组合引用保留能力。
- decision/facts：阻塞问题、答案与解决事实，引用 commit；普通评论不解除阻塞。
- execution/facts：执行代次、保活、停止事实，实例/代次隔离。
- delivery/reply：结果登记及当前轮候选引用，不保存正文或评审规则。
- State：准备、执行、事实审核、人工验收、完成等步骤；需要审核记录的步骤拥有自己的仓储。
- task/flow：状态/轮次/位置唯一写入者；通过有序 states 调用步骤，保留执行安全退出等固定保护。
- 命名命令与查询：依赖上述能力及事务、身份、权限；HTTP 只协议适配，薄界面调用同一 API。

详细字段见[插件清单](plugin-inventory-review.md)与[依赖图](plugin-dependencies.mmd)。SQL 实现各领域仓储，不能把领域规则放入 SQL。插件不直接 import 其他插件实现；共享事务不代表共享领域所有权。超过五个 Deps 记录协调职责及拆分理由，不把不同接口打包规避计数。

## 流程组合

默认目标链 ready → executing → accepting → completed；额外事实审核以新 State 插入，省略 accepting 表示无需人工验收。任务绑定不可变 FlowRef，不能改同版本配置偷偷改变旧任务行为。M04 只实现创建与准备位置，M05 接入执行和后续步骤，不能把尚未实现的状态配置成假成功。

State 的 Describe、CheckEnter、Enter、CheckComplete 保持不变。检查只读；Enter 在本地事务幂等准备/同步记录，远端请求只写 outbox。当前证据变化重查所有已到达步骤，未来步骤不提前 Enter，前序失效保留位置和原因。Enter 幂等依据为 EntryID 与真实证据组合，不使用自己写入后必变的 TaskVersion。

正常推进保留 RoundID，退回/重开新轮；执行 Generation 单独隔离。认领在同事务通过 CheckAction(OpenExecution) 重查。任务完成必须满足有效候选、当前依据、全部相关步骤及安全退出；普通 commit 或 Hook 不能绕过。取消请求、断线、在线、执行成功均不等同所有外部工作停止。

## 一致性、权限与变化流

同 SQL UnitOfWork 提交领域事实、commit、审计、outbox 和回执；嵌套失败使整个事务 rollback-only。幂等命名空间 tenant/subject/operation/key，重放仍校验当前权限。实际作者由认证产生，Agent 不继承 owner 权限；历史、引用、搜索、附件和变化流逐次检查当前任务范围。

Commit 的 Sequence 与 outbox 游标分别用于内容分页与变化恢复，不互相冒充；均保证提交顺序。用户变化流不直接暴露内部 outbox。先 StartCursor、再快照、再补拉；支持重复、游标过期明确重同步。新任务可通过动态权限范围发现。

SQL/S3 不共享事务，文件版本固定与删除补偿按 M07 实现。定义、材料及记录保留遵守历史引用；不承诺外部链接内容快照或任意工具副作用恰好一次。

## 交付顺序

M03 已验证存储/安全基础。M04：任务创建/读取/正式依据变更、不可变 commits、准备初始化、授权 API/变化流和薄界面；不接 Harness。M05：执行、正式结果、验收/取消/退回/重开与新 State 扩展验证。M06：第一阶段真实 Agent 适配与恢复联调。文件、树依赖、提醒及历史恢复按后续清单推进。

核心约束见[核心契约](core-contracts.md)，内容与交付见[Commit](commits.md)和[结果交付](reply-delivery.md)。文档及依赖静态检查不能代替运行验证。

M04 实际子集与验证见[工作台说明](m04-workspace.md)，含 HTTP 变化补拉；WS、结构化阻塞问答及执行后续步骤尚未实现。

当前权限基线见[成员与任务关系](permissions.md)：移除通用 Scope/Grant 和逐动作授权配置，以有效租户成员、固定角色、负责人及参与关系判断业务资格；保持租户隔离、即时撤销和幂等重放前重新鉴权。不兼容旧权限数据库格式。

当前 Go cap 的消费者边界见[能力清单](cap-interfaces.md)。共享命令生命周期由 command.Handler 提供，状态检查使用明确的 allow/wait/reject；通用 Revisions 和旧 Repository/Service 已移除。
