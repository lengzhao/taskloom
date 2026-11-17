# 默认交付：Agent 结果回复

日期：2026 年 11 月 17 日。状态：已确认产品方向下的契约草案，尚未实现。

## 1. 架构结论与边界

默认交付物是 Agent 明确作为工作结果提交的一条回复，可以包含文本、文件版本引用和链接。结果正文只保存在任务会话中，交付插件维护稳定的结果登记及依据，不再维护第二套正文、文件上传流程或必填的逐项标准回应表。

- 会话插件拥有消息及不可变内容修订；编辑消息形成新修订，不覆盖原内容。
- 材料插件拥有文件内容、可用性和版本；回复引用具体文件版本，不只引用可变文件名。
- 交付插件拥有“该回复修订是一次正式交付”的记录，关联任务、执行及目标/标准依据，并维护当前结果及撤回事实。
- 验收插件的结论绑定交付 ID、回复修订和标准版本。
- 状态插件读取交付及验收事实，flow 仍是任务状态唯一写入者。

不将所有 Agent 发言视为结果：交流、问题、进度、工具过程保持普通消息语义。提交动作明确表达结果意图，不根据最后一条消息、自然语言“做完了”或模型 turn 结束推断完成。

保留轻量交付记录是为了稳定引用、撤回、审计和验收。它不是第二份内容存储；将“最新消息”直接视为结果会使进度、编辑和旧执行回报污染当前交付。

## 2. 默认接口

保留 `delivery.Reader.GetSubmission/ListSubmissions/GetCurrent`；写接口收敛为 `delivery.Writer.SubmitReply/Withdraw`。移除原 Submit 中独立正文、独立交付附件列表、结构化产物类型和必填的逐项回应字段。

```go
type Writer interface {
    SubmitReply(context.Context, SubmitReplyRequest) (Submission, error)
    Withdraw(context.Context, WithdrawRequest) (Submission, error)
}

type SubmitReplyRequest struct {
    TaskID         TaskID
    ExecutionID    ExecutionID
    ExpectedVersion Version
    Basis          TaskBasis // 目标与验收标准等依据版本
    Reply          ReplyInput
    IdempotencyKey string
    Reason         string
}

type ReplyInput struct {
    New      *ReplyContent // 新回复：文本、文件版本引用、链接
    Existing *ReplyRef     // 已有回复：MessageID + Revision
    // New 与 Existing 必须且只能提供一种。
}
```

DTO 为设计片段，沿用公共身份、授权、事务和版本约定。常规使用只需提交 New，一次 HTTP 请求创建回复并登记结果；已有消息登记用于避免先发回复的客户端重复发送内容，不作为默认必需的两步流程。

`Submission` 仅保存稳定 ID、任务及执行依据、回复 ID/修订、提交者、提交时间、交付版本、撤回信息和可选检查证据。正文和文件内容由回复及其材料版本解析；不能把引用解析后的内容缓存当成另一份权威正文。

内容可以只有文件或链接，不强制附加长文本；默认至少有非空文本、可用文件版本或合法链接之一。格式合法不等于目标已达成，任务是否完成仍由状态检查及配置的验收规则决定。

## 3. `delivery/reply` 插件

**能力：** `delivery.Reader`、`delivery.Writer`。现有的 delivery.Service 仅组合这两个小接口。

**Config：** 无。

**Deps：**

| 字段 | 能力 | 用途 |
| --- | --- | --- |
| store | delivery.Repository | 结果引用、交付版本和撤回记录 |
| tasks | task.Reader | 读取任务及依据版本 |
| executions | execution.Reader | 检查提交回复的执行归属、代次与有效性 |
| replies | conversation.ResultReplies | 创建/读取精确消息修订，固定证据引用 |
| validators | []delivery.ReplyValidator，可选 | 特殊任务的额外检查，默认空列表 |
| authz | authorization.Authorizer | 正式提交/撤回权限 |
| confirmation | confirmation.Service | 仅命中逐次确认策略时使用 |
| tx | persistence.UnitOfWork | 跨消息和交付的原子事务 |
| requests | idempotency.Repository | 显式注入本操作的幂等存储 |
| changes | revision.ChangeRecorder | 正式结果的变更记录 |
| audit | audit.Recorder | 操作依据与主体记录 |
| events | event.Writer | 同事务持久事件 |

移除 requireCriteriaResponse Config；交付插件不再直接依赖 artifact.Reader，回复与材料引用合法性由会话能力复用既有材料插件检查。不新增结果正文、结果上传、自动总结或专用 Agent API 插件。

### 会话侧的小能力

`conversation.ResultReplies` 提供：

- `GetRevision`：读取指定消息不可变修订及原作者、原执行依据、文件版本和链接；当前权限仍须校验。
- `AppendResultReply`：在当前事务按可信调用主体及执行依据创建结果回复，复用消息内容、大小和附件检查；只保存消息，不自动创建交付或修改状态。
- `PinRevision`：同事务固定正式交付引用的修订及材料版本保留关系，防止一般编辑/清理使验收依据消失。

会话侧通过额外小依赖 `artifactRefs: artifact.ReferenceKeeper` 调用 RetainVersions，按交付引用登记材料版本保留；材料清理必须检查这些引用。该能力由现有 artifact/default 实现，不新增存储或材料插件。

它由 conversation/default 具体类型实现，经 Deps 注入；不是新的可配置会话插件。普通 PostMessage 不自动登记交付，客户端自填 result 标签也不能绕过交付命令。会话不反向依赖 delivery，消除循环。

读取交付时，delivery 通过 replies.GetRevision 解析内容。GetCurrent 返回交付元信息及回复引用，客户端可通过同一通用消息修订 API 读取；不专门为 Agent 打包上下文。消息作者和结果提交者分别保留，不能由请求伪造原作者。

### 提交事务

1. 验证请求幂等键、任务版本、提交权限、执行归属及任务依据。
2. New：调用 AppendResultReply；Existing：调用 GetRevision，验证属于同一任务且原作者/执行依据匹配有效 Buddy 执行，不能直接登记别人的普通消息或其他任务内容。
3. 验证内容至少包含一种有效结果，文件版本可用，链接保留来源；需要时运行注入的额外检查器。
4. 写入交付引用、交付版本和 PinRevision，再保存变更、审计与 delivery.submitted 事件。
5. 统一提交；失败不遗留“回复已发但未登记”的半份结果。引用已有回复失败时不删除其原消息。

回复创建与交付登记共享同一 SQL 实例及事务。首次创建回复可能同时产生 message.posted 和 delivery.submitted 两条关联事件，客户端按 ID/版本去重展示，流转只依据正式交付事实。此事务不发起 S3 上传或访问远端链接。

## 4. 编辑、撤回、验收及文件保留

- 编辑回复产生新修订，原交付仍指向原修订；新内容要成为当前结果必须再次 SubmitReply，新建交付版本。
- GetCurrent 读取显式当前结果引用，成功提交时更新、撤回当前结果时清空；不重新扫描历史以选取更早结果，也不因聊天新增消息或修改消息自动切换；其依据是否仍有效由检查结果单独表达。
- 同一任务、执行、依据、回复修订的重复登记不得创建多份交付；幂等键处理重试，领域唯一性约束处理不同幂等键的同一结果登记。改变依据或新修订是新的提交意图，仍受权限和执行有效性约束。
- Withdraw 记录撤回，不删除消息、旧交付或旧验收；撤回后没有当前结果，不自动把更早旧结果重新当作当前交付。复用旧结果需要再次显式提交并通过当前依据检查。
- 新交付、撤回或标准变化不改写旧验收结论；新版本重新进入相应验收规则。已完成任务的后续变化按现有显式 Reopen 策略处理，不能静默重开。
- PinRevision 是保留规则的一部分，不等于永久禁止企业合规删除；删除后历史记录明确显示不可用，不伪造原内容仍可读取。附件引用和回复展示不扩大访问权限。
- 链接默认固定的是 URL 及当时提交的说明，不保证外部页面内容不变。需要长期可验证内容的任务应使用受控附件版本或显式快照能力，不能声称仅存 URL 就冻结了网页。

## 5. 特殊任务扩展

`delivery.ReplyValidator.Check(ctx, ReplyCheckRequest) (ReplyCheckResult, error)` 是可选能力。请求携带已解析的回复修订、材料版本引用及任务依据；结果为通过、待补充或拒绝及原因/证据。

默认无检查子插件，只有基础合法性和授权/执行/版本检查。特殊任务可经 deps.validators 注入多个检查器，例如必须附带某类文件、必须回应指定标准或必须有特定结构化附件。按列表执行且所有必需检查通过才登记，失败整体回滚；“待补充”返回可处理的检查结果，不当作提交成功或技术重试。

检查器只读本地可信事实，不直接修改状态、创建验收或调用远端工具；需要生成或检测外部产物由 Agent 完成后再提交证据。不为了一个特殊产物建立第二套默认交付体系。

## 6. API、状态链和两层配置

- api/deliveries 调用 SubmitReply/Withdraw 和读取能力，与前端、Agent 共用；结果内容语义复用 api/conversations 与 api/artifacts。
- api/conversations 补充按消息 ID/修订读取的接口。ResultReplies 中 AppendResultReply/PinRevision 是内部能力，不独立暴露为可绕过 SubmitReply 的路由。
- executing 状态检查当前有效结果回复引用；accepting 状态建立绑定该引用及标准版本的验收；最终完成检查保持独立。
- 标准流程和无需验收流程都使用同一 delivery/reply；无需验收只是省略独立验收，不省略结果登记与完成检查。
- base 中将 delivery 实例 use 改为 delivery/reply，删除原 Config，注入 replies 和 requests，默认不写 validators；config 日常部署入口无需增加字段，流程示例无需复制交付装配。

完整配置和默认值见[两层配置说明](configuration.md)。尚未实现插件，需验证原子提交、消息修订固定、重复结果登记、附件版本保留、旧执行拒绝及验收引用不随编辑漂移。
