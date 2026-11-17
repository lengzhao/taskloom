# 任务系统插件能力、Config 与 Deps 草案

日期：2026 年 11 月 17 日。状态：开发设计参考，尚未实现或冻结 Go 公共契约。

本文承接[模块划分](task-system-modules.md)。默认交付以[Agent 结果回复契约](reply-delivery.md)为准，正文复用会话消息，交付只登记稳定引用及依据。前端与 Agent 共用 API 及实时推送以[共享 API 与 WebSocket 契约](shared-api-and-realtime.md)为准；任务流转以[状态子插件与 Hook 契约](task-flow-plugins.md)为准，状态提交统一归 task/flow，领域插件只写自身业务事实。本项目只建设任务系统；人是默认负责人，Agent（Buddy）是执行者，运行实例归入执行记录。软件分发与 Agent 执行层直接复用已有能力。

## 1. 插件装配原则

参考本地 AgentKit 的 `docs/go-agent-harness-architecture.zh.md` 中插件装配、依赖注入及生命周期约定：

- 复用 PluginKit 的注册、配置识别和实例图构建，不另建注册表、服务容器或流程引擎。
- 顶层实例 ID 标识配置实例；`use` 标识注册 kind，`config` 提供静态配置，`deps` 引用其他实例或内联插件。
- 公共接口与 DTO 放 `cap/*`；插件实现放 `plugins/*`；公共运行机制放 `runtime/*`。
- 构造函数形如 `New(Config, Deps) (*Implementation, error)`；`Deps` 字段类型为能力接口，JSON tag 与配置依赖名一致。具体返回类型可以实现多个小接口，不要求消费者依赖完整实现。
- 插件包不直接引用其他插件实现；依赖通过构造注入，不在请求中查找全局服务或进行具体实现类型断言。
- 根插件负责装配后的启动和停止；构造函数不启动后台工作。插件启停按依赖顺序进行，退出时逆序释放资源。
- 配置通过有序 `deps.states` 组装任务状态子插件，Hook 根据业务事件触发检查；具体检查与动作由能力接口实现，不增加脚本式流程语言或统一 `application/default` 编排层。
- 插件按需可替换，不代表所有插件都能任意删除。启用结果提交入口时，其任务、会话、执行、权限及事务依赖必须齐全；是否经过独立验收由流程配置决定。

SQL 采用何种数据库、S3 的具体供应商、Agent 对接协议仍可替换；本文的 `storage/sql` 和 `storage/s3` 是拟注册 kind，不表示已有实现。

声明形式示例（设计片段，不是当前可编译代码）：

```go
// cap/blob：消费者依赖能力，不依赖 S3 实现。
type Store interface {
    PutObject(context.Context, PutObjectRequest) (ObjectInfo, error)
    HeadObject(context.Context, HeadObjectRequest) (ObjectInfo, error)
    OpenObject(context.Context, OpenObjectRequest) (ObjectContent, error)
    DeleteObject(context.Context, DeleteObjectRequest) (DeleteObjectResult, error)
}

// plugins/artifact：Config 字段描述策略，Deps 字段描述实例连接。
type Config struct {
    MaxUploadBytes int64    `json:"maxUploadBytes"`
    PendingTTL     Duration `json:"pendingTTL"`
    OrphanGrace    Duration `json:"orphanGrace"`
}

type Deps struct {
    Store        artifact.Repository        `json:"store"`
    Objects      blob.Store                 `json:"objects"`
    Tasks        task.Reader                `json:"tasks"`
    Authz        authorization.Authorizer   `json:"authz"`
    Confirmation confirmation.Service       `json:"confirmation"`
    Tx           persistence.UnitOfWork     `json:"tx"`
    Changes      revision.ChangeRecorder    `json:"changes"`
    Audit        audit.Recorder             `json:"audit"`
    Events       event.Writer               `json:"events"`
}

func init() { pluginkit.Register("artifact/default", New) }
func New(cfg Config, deps Deps) (*Service, error) // 实现全部材料能力
```

同一 SQL 实例可以被 `store`、`tx` 等字段引用，因为其实现不同的小接口；S3 实例只需满足 `blob.Store`。替换 S3 供应商或存储实现不改变材料业务插件。

## 2. 共同契约

以下每个能力方法均遵循 Go 形态：`Method(context.Context, MethodRequest) (MethodResult, error)`。表中的方法是独立强类型方法，不是字符串动作分发。正式 DTO 在实现前逐项定义；本文先固定职责、方法语义及必需信息。

- 可信认证上下文包含企业、实际操作者、代理归属、请求追踪标识。身份由认证器产生，业务请求不能自行指定可信身份。上下文只传请求身份和事务等运行信息，不用作服务查找容器。
- 写请求携带幂等键、原因以及被修改对象的预期版本；创建无需旧版本。相同幂等键与内容返回原结果，同键不同内容报冲突。
- 跨对象操作携带各受影响对象的版本前提；原子提交返回变更 ID 及新的对象版本。
- 所有对外业务能力校验权限，不能只在 HTTP 入口检查。内部持久化端口不对外暴露；内部调用仍传原始主体，不换成无限权系统身份。
- 列表请求包含过滤条件、数量上限与游标；历史、搜索、事件和对象内容下载同样受当前授权约束。
- 统一错误类别：无权限、需要确认、未找到、无效输入、版本冲突、状态冲突、执行过期、依赖未满足、暂时不可用。对越权资源是否统一返回未找到，由企业安全策略确定。
- 正式写入、变更、必要审计及 outbox 事件必须在同一 SQL 事务中提交。不能先发事件再提交，也不能先提交业务后补写版本。
- 业务状态变化与技术执行结束分离；收到停止请求与已经停止分离；历史结果与当前有效交付分离。

### 配置类型和默认值

下文 Config 采用 `字段: 类型` 列举，与 Go struct 的 JSON tag 一一对应；Deps 采用 `字段: cap/package.Interface` 列举。无配置项的插件使用 `Config struct{}`，无依赖的插件使用 `Deps struct{}`。示例值用于展示装配，不是已经确认的产品默认策略或服务承诺。

`Duration` 在配置中写作 `10s`、`5m` 等字符串，由配置类型解析与校验。`SecretRef` 是凭据引用，不能当作明文密钥输出。零值默认与所有枚举的合法集合必须在插件实现前声明；对授权、完成及自动重试等关键策略，不允许以未声明零值静默放行。

## 3. 存储插件

### 3.1 `storage/sql`：事务与领域仓储

**提供能力：**

| 接口 | 方法与职责 |
| --- | --- |
| `persistence.UnitOfWork` | `Within(ctx, func(txCtx context.Context) error) error`；启动事务，嵌套调用加入同一事务，成功提交、失败整体回滚 |
| `persistence.Health` | `Check`，验证连接及 schema 兼容性 |
| `runtime.Resource` | `Open`、`Close`，由根插件统一管理连接资源 |

每个插件的实际 Deps 使用表中声明的小接口。SQL 具体类型同时实现各领域仓储接口，由 PluginKit 直接按字段接口类型注入同一实例，不设置仓储查询容器。这些接口用领域前缀区分方法名，避免 `Get` 等方法签名冲突。

领域仓储最低能力：

| 接口 | 最低方法 |
| --- | --- |
| `task.Repository` | `LoadTask`、`QueryTasks`、`InsertTask`、`CompareAndSwapTask` |
| `flow.Repository` | `LoadDefinition`、`SaveDefinitionIfAbsent`、`LoadPosition`、`InitializePosition`、`CompareAndSwapPosition`；原子维护流程位置、任务状态和版本 |
| `conversation.Repository` | `LoadMessage`、`LoadMessageRevision`、`QueryMessages`、`InsertMessage`、`AppendMessageRevision`、`PinMessageRevision`、`LoadReadState`、`SaveReadState`；内容修订不可变 |
| `decision.Repository` | `LoadDecisionRequest`、`QueryDecisionRequests`、`InsertDecisionRequest`、`CompareAndSwapDecisionRequest` |
| `plan.Repository` | `LoadRelations`、`ApplyRelations`、`QueryBlockers`、`QueryDependents`、`QueryDescendants`、`QueryAncestors`；同事务保护并发环检查 |
| `execution.Repository` | `LoadAssignment`、`SaveAssignment`、`LoadExecution`、`QueryExecutions`、`InsertExecution`、`CompareAndSwapExecution`；执行代次保护当前所有权 |
| `delivery.Repository` | `LoadSubmission`、`QuerySubmissions`、`InsertSubmission`、`WithdrawSubmission`、`LoadCurrentSubmission`、`CompareAndSwapCurrentSubmission`；只保存回复修订引用及依据，撤回当前结果时清空引用，不自动回退旧结果 |
| `acceptance.Repository` | `LoadAcceptancePolicy`、`SaveAcceptancePolicy`、`LoadReview`、`QueryReviews`、`InsertReview`、`CompareAndSwapReview` |
| `revision.Repository` | `AppendChange`、`LoadChange`、`QueryChanges`、`LoadSnapshot` |
| `artifact.Repository` | `LoadArtifact`、`InsertArtifact`、`InsertArtifactVersion`、`QueryArtifactVersions`、`UpdateArtifactState`、`AttachArtifact`、`DetachArtifact`、`RetainArtifactVersions`、`QueryArtifactRetentionRefs` |
| `access.Repository` | `QueryGrants`、`InsertGrant`、`RevokeGrant`、`LoadConfirmation`、`InsertConfirmation`、`ConsumeConfirmation`、`RejectConfirmation` |
| `notification.Repository` | `QueryNotifications`、`InsertNotification`、`UpdateNotification`、`LoadPreferences`、`SavePreferences` |
| `event.OutboxRepository` | `AppendOutbox`、`ReadOutbox`、`TrimOutbox` |
| `event.CursorRepository` | `LoadCursor`、`CompareAndSwapCursor`、`ClaimConsumer`、`ReleaseConsumer`、`SaveEventFailure`、`QueryEventFailures`、`RequestReplay` |
| `audit.Repository` | `AppendAudit`、`QueryAudit` |
| `idempotency.Repository` | `FindRequest`、`ClaimRequest`、`SaveRequestResult`；与业务写入同事务 |

仓储写入只接受明确领域类型，不开放任意字段更新或 SQL 字符串给业务插件。幂等运行机制由 `runtime/command` 复用，使用 SQL 的 `UnitOfWork` 和 `idempotency.Repository`，不另设可配置业务编排器。

**Config：** `driver: string`、`dsnRef: SecretRef`、`maxOpenConns: int`、`maxIdleConns: int`、`connMaxLifetime: Duration`。

**Deps：** `credentials: cap/credentials.Resolver`。

schema 迁移使用独立的部署迁移命令，不由每个业务插件自行建表；启动校验 schema 兼容。首个实现需选定并验证一个 SQL 方言，不承诺所有 SQL 数据库天然兼容。

### 3.2 `storage/s3`：对象内容

**接口：** `blob.Store` 提供 `PutObject`、`HeadObject`、`OpenObject`、`DeleteObject`；写入返回对象 key、内容摘要、大小和可用的对象版本标识。`runtime.Resource` 提供 `Open`、`Close`。

**Config：** `endpoint: string`、`region: string`、`bucket: string`、`prefix: string`、`credentialRef: SecretRef`、`pathStyle: bool`、`requestTimeout: Duration`。

**Deps：** `credentials: cap/credentials.Resolver`。

SQL 保存附件归属、任务引用、业务版本、摘要与对象定位；S3 保存文件字节。对象 key 由材料插件按企业及不可变内容版本生成，不接受客户端任意访问其他 key。附件变更写新对象，不能覆盖历史内容。业务版本不依赖存储端是否开启原生对象版本功能。

SQL 与 S3 不具备共享事务：先在 SQL 登记待上传项，再上传对象并校验，最后在 SQL 将其置为可用。只有可用且有权限的内容可进入正式交付。失败及未引用对象由可重试的维护任务清理；删除采用先标记、后删除对象、再确认的方式。读取默认经应用鉴权代理，不默认生成可长期转发的公开下载链接。

### 3.3 `credentials/env`：凭据解析

**接口：** `credentials.Resolver.Resolve`，按引用读取部署环境中的凭据；返回敏感类型，日志和配置诊断脱敏。

**Config：** `allowedRefs: []string`。**Deps：** 无。

用于 SQL DSN、S3 凭据及已有身份接口凭据。示例中引用形式为 `env:NAME`，由本插件明确解析；不假设 PluginKit 自身负责读取密钥。已有凭据系统可提供相同接口替换。

## 4. 身份、授权与业务记录插件

### 4.1 `identity/existing`

- 能力：`identity.Authenticator.Authenticate`；`identity.Directory.GetMember/GetBuddy/GetInstance/ResolveDefaultBuddy`。
- Config：`baseURL: string`、`credentialRef: SecretRef`、`timeout: Duration`。
- Deps：`credentials: credentials.Resolver`。
- 只对接已有身份与 Buddy 目录，不新建另一套身份事实。实际接口协议需与已有平台核对。

### 4.2 `authorization/default`

- 能力：`authorization.Authorizer.Check/CheckBatch`，返回允许、拒绝或需要确认；`authorization.GrantManager.Grant/Revoke/ListGrants`。
- Config：无。未授权默认拒绝是固定规则；具体授权以业务数据存储，不作为静态插件图的一部分。
- Deps：`identity: identity.Directory`、`store: access.Repository`、`taskStore: task.Repository`、`planStore: plan.Repository`、`tx: persistence.UnitOfWork`、`changes: revision.ChangeRecorder`、`audit: audit.Recorder`、`events: event.Writer`。
- 基于领域仓储读取资源范围，避免反向依赖 `task/default` 形成环；授权写操作检查管理权限和可授予上限。

### 4.3 `confirmation/default`

- 能力：`confirmation.Service.Propose/Confirm/Reject/Consume`。
- Config：`ttl: Duration`。
- Deps：`store: access.Repository`、`authz: authorization.Authorizer`、`tx: persistence.UnitOfWork`、`audit: audit.Recorder`、`events: event.Writer`。
- 操作确认绑定主体、动作、对象、内容摘要、依据版本和期限；消费与对应业务动作同事务，不能重复使用。普通提问答复不等于授权确认。

### 4.4 `revision/default`

- 能力：`revision.Reader.ListChanges/GetChange/Compare/GetSnapshot`；`revision.ChangeRecorder.RecordChange`；`revision.RestorePlanner.PrepareRestore`。
- Config：`snapshotEvery: int`，仅为加速策略，不改变历史语义。
- Deps：`store: revision.Repository`。
- 写能力仅作为内部事务端口，要求有效事务；原子追加前后内容及依据，不能依赖后台事件补建版本。读取及恢复方案对外必须经过鉴权读取包装器。恢复只输出方案，交由原领域插件执行权限与一致性检查。

### 4.5 `audit/sql`

- 能力：内部 `audit.Recorder.Record`；`audit.Reader.Query`。
- Config：无。
- Deps：`store: audit.Repository`。
- 正式业务成功的审计同事务提交；请求被拒绝的安全审计走独立短事务，不能因业务回滚而丢失。查询经鉴权读取包装器。与版本记录分别保存、用请求及变更标识关联。

## 5. 业务插件

以下业务插件的公共基础 Deps 为 `authz: authorization.Authorizer`、`confirmation: confirmation.Service`、`tx: persistence.UnitOfWork`、`changes: revision.ChangeRecorder`、`audit: audit.Recorder`、`events: event.Writer`；每个插件都在其具体 `Deps` struct 中显式声明，不能隐式查找。只读接口不消费确认凭据；修改只有命中逐次确认策略时才消费。

### 5.1 `task/default`

- 能力：`task.Reader.Get/List`；`task.Writer.Create/UpdateDetails/AssignOwner/SetParticipants/Archive`。
- Config：`defaultFlow: {id: string, version: string}`。
- Deps：公共基础 + `store: task.Repository`、`identity: identity.Directory`。
- 人为默认负责人，执行 Buddy 单独记录。任务创建绑定 FlowRef 并发事件，由 Hook 初始化状态链。移除公共 Transition，信息更新接口不能写状态字段；取消、退回、重开走 flow.Controller。任务插件不依赖 flow 主插件，避免循环。

### 5.2 `conversation/default`

- 能力：`conversation.ResultReplies.GetRevision/AppendResultReply/PinRevision`（供交付插件内部组合）；`conversation.Reader.GetMessage/GetRevision/ListMessages`；`conversation.Writer.PostMessage/EditMessage`；`conversation.ReadState.MarkRead/GetUnread`。
- Config：`maxMessageBytes: int`、`editWindow: Duration`。
- Deps：公共基础 + `store: conversation.Repository`、`tasks: task.Reader`、`artifacts: artifact.Reader`、`artifactRefs: artifact.ReferenceKeeper`（正式结果固定材料版本引用）。
- 系统事件从正式记录投影到时间线，不允许客户端伪造系统事件。每任务内分配稳定服务端顺序；消息编辑追加不可变修订，正式结果引用的修订及材料版本保留；不覆盖旧内容。读取状态更新不产生需求修订版。附件引用逐项检查访问权。

### 5.3 `decision/default`

- 能力：`decision.Reader.GetRequest/ListRequests`；`decision.Writer.OpenRequest/AnswerRequest/ResolveRequest/InvalidateRequest`。
- Config：`defaultTimeout: Duration`，超时只标记及提醒，不默认推定同意。
- Deps：公共基础 + `store: decision.Repository`、`tasks: task.Reader`。
- 请求记录回答对象、是否阻塞、所依赖版本及结论。回复不直接执行任意业务动作；问题解决后发事件，由明确订阅者判断是否继续执行。

### 5.4 `plan/default`

- 能力：`plan.Reader.GetTree/ListDependencies/GetBlockers`；`plan.ImpactReader.ListDependents/ListDescendants/ListAncestors`；`plan.Writer.ApplyChangeSet`；`plan.Restorer.ApplyRestore`。
- Config：`allowCrossTreeDependencies: bool`、`maxChangeSetNodes: int`。
- Deps：公共基础 + `store: plan.Repository`、`tasks: task.Reader`、`taskWriter: task.Writer`。
- 成组操作覆盖创建子任务、移动、排序、增删依赖，统一验证版本、可见范围和环；失败全部回滚。跨节点创建通过注入的任务能力加入当前事务。依赖满足条件必须明确在边上记录，首版可先只支持前置任务完成。

### 5.5 `execution/default`

- 能力：`execution.Reader.GetAssignment/GetExecution/ListExecutions`；`execution.AssignmentWriter.AssignBuddy/AcceptAssignment/DeclineAssignment`；`execution.RunWriter.OpenExecution/ReportProgress/EndExecution/InvalidateExecution`；`execution.StopWriter.RequestStop/AcknowledgeStop`。
- Config：`offlineGrace: Duration`。首版每任务最多一个有效执行为固定规则；不默认自动重试外部副作用。
- Deps：公共基础 + `store: execution.Repository`、`tasks: task.Reader`、`plan: plan.Reader`、`identity: identity.Directory`。
- Agent 通过共用 execution API 记录任务侧接受、实例及执行身份。当前有效执行使用单调执行代次或等价隔离令牌，原子防止旧实例写入当前事实。断线不等于已经停机，更换执行者或重试必须按策略处理。仅维护任务侧所有权，不接管 Agent 运行时调度，不写任务流程状态；事实变化通过事件触发 Hook。Agent 经共用 API 认领有效执行，写入核对执行代次及本轮依据。未指定执行者时可解析负责人默认 Buddy 并原子绑定；权限不足不得认领。任务系统不发起远端运行。

### 5.6 `acceptance/default`

- 能力：`acceptance.Reader.GetPolicy/GetReview/ListReviews`；`acceptance.Writer.SetPolicy/OpenReview/RecordVerdict`。
- Config：`defaultMode: enum(manual,none)`、`separationOfDuties: bool`。
- Deps：公共基础 + `store: acceptance.Repository`、`submissionStore: delivery.Repository`、`tasks: task.Reader`、`identity: identity.Directory`。
- 验收绑定交付 ID、消息修订和标准版本；没有交付实现的反向依赖。通过领域仓储读取提交证据，校验代理身份和自验收策略。结论只保存验收事实并发事件，由状态 Hook 请求流转检查；不直接修改任务。最终完成检查移至 task/completion-check 子插件，无需独立验收的流程使用独立检查配置。

### 5.7 `delivery/reply`

- 能力：`delivery.Reader.GetSubmission/ListSubmissions/GetCurrent`；`delivery.Writer.SubmitReply/Withdraw`。
- Config：无。默认交付是 Agent 明确提交的结果回复，可带文件版本引用和链接；普通消息、进度和提问不自动成为交付。
- Deps：公共基础 + `store: delivery.Repository`、`tasks: task.Reader`、`executions: execution.Reader`、`replies: conversation.ResultReplies`、`requests: idempotency.Repository`、可选 `validators: []delivery.ReplyValidator`。
- SubmitReply 默认在一个 SQL 事务中创建结果回复并登记引用，也支持显式登记已有回复的指定修订。正文只保存在会话；交付记录保存回复 ID/修订、执行与任务依据、版本及撤回信息，不再维护独立正文和交付附件字段。
- 复用会话侧文件合法性检查，不直接依赖 artifact.Reader。回复/材料检查、消息修订固定、交付登记及变更事件原子提交；不直接修改任务状态或创建验收。
- 删除 requireCriteriaResponse 开关，默认不强制逐项标准回应或特殊结构；有特殊要求时通过可选检查子插件注入。完整 DTO、检查接口、编辑/撤回和验收语义见[结果回复交付](reply-delivery.md)。

### 5.8 `artifact/default`

- 能力：`artifact.ReferenceKeeper.RetainVersions`（本地事务内固定材料版本引用，供会话正式结果保留）；`artifact.Reader.Get/ListVersions/OpenContent`；`artifact.Writer.BeginUpload/UploadContent/CompleteUpload/RegisterExternal/CreateVersion/Attach/Detach/RequestDeletion`。
- Config：`maxUploadBytes: int64`、`pendingTTL: Duration`、`orphanGrace: Duration`。
- Deps：公共基础 + `store: artifact.Repository`、`objects: blob.Store`、`tasks: task.Reader`。
- UploadContent 通过普通 API 接收限流、限大小的字节流，校验上传项归属后写 S3；CompleteUpload 校验摘要及大小后确认可用。该内容写入在 SQL 长事务之外。文件下载、外部引用和任务附件均鉴权；外部链接标明是否有受控快照。删除引用与删除内容分开，已被交付、验收引用的版本按保留策略处理。后台清理通过独立维护组件启动，不在构造函数中运行。

## 6. 事件、查询、通知与 Agent 对接

### 6.1 `event/sql`

- 能力：内部 `event.Writer.Append`（只写当前 SQL 事务 outbox）；`event.Reader.ReadAfter`；`event.CursorStore.Load/Commit/Claim/Release/RecordFailure/ListFailures/RequestReplay`。
- Config：`retention: Duration`、`maxBatchSize: int`。
- Deps：`outbox: event.OutboxRepository`、`cursors: event.CursorRepository`、`tx: persistence.UnitOfWork`。
- 事件有 ID、企业、聚合对象、聚合内序号、schema 版本、关联请求及变更标识；含最小必要数据。原始事件流仅供可信内部消费者，不作为未经授权的外部查询端口。业务插件不依赖事件订阅者，避免配置图闭环。outbox 同时作为保留期内的事件日志，各消费者独立确认；一个消费者处理成功不能使其他消费者看不到事件。

### 6.2 `query/default`

- 能力：`workbench.Reader.ListMyTasks/ListActionItems/ListBuddyActivity/GetOverview`；`search.Reader.Search`；鉴权包装后的 `revision.Reader`、`audit.Reader` 及 `event.Reader`。
- Config：`defaultPageSize: int`、`maxPageSize: int`。
- Deps：`taskStore: task.Repository`、`decisionStore: decision.Repository`、`executionStore: execution.Repository`、`acceptanceStore: acceptance.Repository`、`messageStore: conversation.Repository`、`deliveryStore: delivery.Repository`、`revisions: revision.Reader`、`audit: audit.Reader`、`events: event.Reader`、`authz: authorization.Authorizer`。
- 首版可直接查询 SQL，后续异步搜索实现必须声明延迟并重新检查当前权限。工作台不维护第二份任务权威状态。过滤和分页不能让计数、摘要、事件内容泄露无权资源。

### 6.3 `notification/default`

- 能力：`notification.Inbox.List/MarkRead/SetPreferences`；`event.Subscriber.Handle/Topics`。
- Config：`enabledTopics: []string`。
- Deps：`store: notification.Repository`、`authz: authorization.Authorizer`、`tx: persistence.UnitOfWork`、`events: event.Writer`。
- 幂等消费业务事件并直接写站内收件箱，同事务发 notification.created（携带原任务引用及收件人范围）供授权 feed 提示刷新。该事件不在通知自身的默认订阅集合中。通知写入和消费进度按可靠事件规则处理，不再经过发送请求链。客户端通过通知 API 获取内容，任务变化仍由授权 feed 独立推送。
- 删除无实际发送行为的 channel、sender 和 sender worker；不影响业务待办记录。后续确需飞书/邮件等外部投递时，再增加独立渠道和可靠发送组件。

### 6.4 `task/feed` 与 `transport/websocket`

前端和 Agent 共用任务变化读取、游标及订阅。task/feed 提供授权变化读取及 Stream；transport/websocket 实现 HTTP 路由模块和受根服务管理的长连接资源。不再设置远端 Agent 派发、专用上下文组装或回调入口。完整能力、Config、Deps 见[共享 API 与实时推送](shared-api-and-realtime.md)。

### 6.6 `event/worker`

- 能力：`runtime.Component.Run/Stop`。
- Config：`consumerID: string`、`batchSize: int`、`retryBackoff: Duration`、`maxAttempts: int`。
- Deps：`events: event.Reader`、`cursors: event.CursorStore`、`handler: event.Subscriber`。
- 订阅集合由 handler.Topics() 提供，不再重复配置 topics；每个消费者独立进度；处理成功再提交游标，失败重试，超过上限持久记录失败及人工重放入口，不静默丢弃。支持多进程时须认领或锁定同一消费者分区。处理器必须幂等。

### 6.7 `artifact/maintenance`

- 能力：`runtime.Component.Run/Stop`。
- Config：`interval: Duration`。
- Deps：`artifacts: artifact.Maintainer`，由材料具体类型提供 `SweepPending`、`SweepOrphans`、`SweepDeletions`，分别清理待上传、孤儿和已申请删除内容。
- 与在线业务路径共享删除与保留规则，不直接绕过材料插件删除 S3 对象。

## 7. 入口与根插件

### `transport/http` 与 `api/*`

- HTTP 能力：`runtime.Component.Run/Stop`。
- HTTP Config：`listen: string`、`apiPrefix: string`、`readTimeout: Duration`、`writeTimeout: Duration`。
- HTTP Deps：`identity: identity.Authenticator`、`modules: []httpapi.Module`。
- 各 API 模块通过 `Routes() []Route` 声明路由并注入自身需要的业务小接口；前端和 Agent 使用相同业务 API，执行记录及停止回执亦然。HTTP 服务不持有所有领域 Service，不直接修改领域数据。
- 流程控制 API 不对外暴露内部 Initialize/Wake；状态子插件不会自动生成可任意调用的业务路由。
- 全部 API 模块及 WebSocket、事件 feed 的 Config 和 Deps 见[共用 API 契约](shared-api-and-realtime.md)。

### `server`

- 能力：`runtime.App.Run/Stop`。
- Config：`shutdownTimeout: Duration`。
- Deps：`resources: []runtime.Resource`、`components: []runtime.Component`。
- 先打开资源，再运行组件；启动失败清理已启动部分，退出先停止入口和工作组件，再关闭存储。配置图按依赖构建，根插件只持有启动清单，不拥有全部业务状态。WebSocket 受 HTTP 模块 owner 管理，不再重复放进根 resources。

## 8. 关键业务与事务边界

1. 创建任务：保存人的负责人、目标、标准和 FlowRef 及创建事件。Hook 初始化有序状态列表，WebSocket 独立推送变化；Agent 经共享 API 按需读取并在已有运行时中 steer。
2. 调整计划：计划插件在同一 SQL 事务中调用任务能力和关系仓储，保存整次变更并记录前后受影响任务；Hook 对目标任务重新检查，不直接跳转状态。
3. 提交结果回复：交付插件原子创建/引用会话消息修订，固定文件版本并保存轻量交付引用及事件。Hook 调用 flow.Wake；当前状态完成且下一状态允许进入后，flow 在自己的事务内推进并运行本地 Enter。保存交付成功与后续流转成功分别可观测，Hook 失败可重试。
4. 验收：验收插件保存绑定交付、回复修订及标准版本的结论并发事件，末状态通过完成检查后由 flow 原子提交完成状态；验收不反向依赖 flow。未通过则保留当前位置，退回使用显式 Return。
5. 取消任务：flow.Controller.Cancel 保存取消标记、原位置、代次及 task.cancelled 事件；WebSocket 推送取消变化，Agent 经共享执行 API 确认停止。取消不等于工具已停止。
6. 上传材料：S3 操作在 SQL 长事务之外进行，用待上传、可用、待删除等显式状态恢复；正式交付只能引用可用材料。
7. 流程提交：CheckComplete、CheckEnter、Enter、任务状态/流程位置更新及记录处于同一 SQL 事务；Enter 只写本地业务数据及 outbox，不调用远端。状态插件的业务条件依据版本需纳入并发校验。

所有领域仓储、版本、审计和 outbox 必须注入同一 `storage/sql` 实例。UnitOfWork 的事务上下文绑定该实例，错误混接其他实例应报错。禁止自动重放含外部副作用的事务回调；SQL 重试仅允许对已确认无外部动作的业务写入实施。

依赖主干：入口和工作组件 → 业务插件 → 小能力接口及 SQL/S3 适配。授权依赖领域仓储，不依赖任务业务插件；版本、审计、事件是更底层的记录能力，查询包装器负责其对外鉴权，从而保持实例图无环。

流转主插件、状态子插件、完成检查器及 Hook 的完整接口、Config、Deps 见[任务流转插件](task-flow-plugins.md)。旧的交付主导完成及通用 Transition 方案由此替代；无需合并职责不同的领域插件。

## 9. 配置示例与验证要求

两层配置以[配置说明](configuration.md)为准：L0 [base.yaml](../base.yaml) 维护完整默认装配，L1 [config.yaml](../config.yaml) 为日常维护入口，按实例 ID 覆盖。不同流程示例作为独立 L1 编辑片段，不增加第三层。采用已核对的 AgentKit 顶层实例图格式；不是上轮简写的 `plugins: [{id: ...}]` 格式。所有 kind、Config 和 Deps 均为本项目拟定义项，示例目前不能启动运行，尚未通过 PluginKit 构建验证。已完成 YAML 解析、实例引用存在性、依赖无环及根可达性静态检查；这不等同于 Go 接口类型或业务行为验证。

构造时检查未知 kind、缺失依赖、依赖类型、依赖环、非法配置及存储后端混用。实现阶段至少验证：配置图成功构建、替换 SQL/S3 实现后的契约、跨插件回滚、并发计划环、重复交付/进度提交、旧执行隔离、上传中断清理、重连事件补拉及授权撤销。配置诊断必须隐藏所有凭据值。

仍需确定：首个 SQL 方言、已有身份及 Agent 协议、状态及完成规则、依赖满足条件、取消与重派策略、历史恢复范围、保留期限和容量指标。它们应落实为领域 DTO、策略或具体 Config 校验，不凭示例值视为最终决定。

参数省略时的默认值、固定规则与两层合并语义见[配置说明](configuration.md)。公共依赖仍在 base 显式装配，各插件实现时只保留实际使用的能力，不引入万能服务容器。
