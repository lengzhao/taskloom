# 任务流程：由事实推导步骤

流程不再是可组合的状态机。任务系统只存储它能直接核实的事实，"现在在哪一步、在等什么"每次读取时从这些事实计算，不落库，因此不会与事实漂移。实现见 `plugins/flow`（`task/flow`），接口见 `cap/flow`。

## 存什么

| 存储字段 | 语义 |
| --- | --- |
| Status | active / cancelled / completed，只由流程控制器写入 |
| RoundID | 一轮工作；Return、Reopen 新建轮次，正常推进不变 |
| Acceptance | `manual` 或 `none`，创建任务时按部署配置写入，此后固定；部署配置变更不暗改旧任务 |
| Revision | Status 或 RoundID 变化时递增，作为流程命令的 ExpectedRevision |

缺少 Acceptance 的旧位置记录被明确拒绝（冲突），不猜测其验收方式。

## 推导什么

`State` 与 `Waiting` 按以下顺序由事实计算，第一个不满足的条件决定步骤和等待原因：

| 条件 | State | Waiting |
| --- | --- | --- |
| 目标或验收标准为空 | ready | 请补充任务目标 / 验收标准 |
| 本轮没有当前交付 | executing | 等待有效结果提交 |
| 当前交付的依据版本 ≠ 任务依据版本 | executing | 结果依据已失效 |
| manual 且当前交付没有负责人结论 | accepting | 等待负责人验收 |
| manual 且结论为退回 | accepting | 验收未通过，等待重新提交 |
| 本轮有未回答问题 | 保持上一行步骤 | 等待被提问人回答问题 |

全部满足即达成完成条件。终态 completed 的 State 为 completed；cancelled 保留推导出的步骤，Waiting 为"任务已取消"。

"待验收"不存记录：有当前交付、需要验收且无结论即为 pending，查询时合成。负责人的结论（approved/rejected）绑定 SubmissionID、CommitID 与 BasisVersion 存储，新交付或依据变化自动使旧结论不再适用；每次结论另存历史。

## 控制器

| 方法 | 语义 |
| --- | --- |
| Initialize | 同事务创建位置：新 RoundID、active、当前配置的 Acceptance |
| Read | 读存储字段并推导步骤 |
| Refresh | 每个改变事实的命令在同事务末尾调用；达成完成条件即写 completed |
| CheckOpen | 分配和认领的前提：任务 active 且依据完整（非 ready） |
| CheckEdit | 修改依据的前提：active、不在验收中、本轮无有效认领 |
| Cancel | active → cancelled |
| Return / Reopen | active / 终态 → 新轮 active，然后 Refresh |

取消、退回、重开和转交由命令模块同事务失效执行写资格（Writable），控制器不观察外部进程。完成不等待执行者停下。

## 扩展方式

需要新的完成条件时，在推导函数中加一行有真实消费者的事实检查，而不是预建状态插件接口。若将来确有多个互斥流程，再按真实需求引入，届时同样遵守"只存事实、步骤推导"的原则。
