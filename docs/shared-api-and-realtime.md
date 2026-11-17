# 共用业务 API、WebSocket 与 Agent steer

日期：2026 年 11 月 17 日。状态：接口设计草案，尚未实现。

## 1. 已确认边界

前端与 Agent 共用任务、会话、计划、执行、交付、验收等业务 API，按实际身份和权限读取或操作。任务系统不提供专用 Agent API，不组装 Agent 提示词、上下文或摘要，不远端启动 Agent、不派发运行指令、不接收另一套协议的专用回调。

任务系统通过 WebSocket 推送任务变化。已有 Agent 层负责连接、去重、读取通用业务 API，并将新信息作为 steer 输入；是否继续当前 turn、在安全点注入或从空闲唤醒由已有 Agent 层决定。连接在线不等于模型持续运行。收到通知不自动获得执行权限，也不意味着每条消息都要回复。

业务写入 → SQL 事务中的持久事件 → 两个独立消费路径：

- 状态 Hook → flow → 状态子插件检查与进入动作 → 提交状态变化。
- 授权任务事件读取 → WebSocket → 前端更新界面／Agent 自主读取与 steer。

任务详情或会话变化即使未引起状态变化，也应推送。通知收件箱是面向人的提醒能力，不是 WebSocket 事件可靠性的来源。所有写操作通过 HTTP 业务接口提交，WebSocket 仅承担订阅、推送、确认和连接控制。

## 2. API 模块化

HTTP 服务只负责监听、认证、中间件、路由装配、错误处理及连接生命周期。API 模块解析和校验协议请求，调用业务小接口；领域插件拥有权限、事务与业务规则。API 模块不直接读写 SQL、S3 或调用 State.Enter。

```go
type Module interface {
    Routes() []Route
}

type Route struct {
    Method    string
    Path      string
    Operation string
    Handler   http.Handler
}
```

这是草案；Routes 在构造后固定，注册不修改全局路由表。认证由 HTTP 统一建立可信主体，业务方法仍校验权限。路由冲突及重复 Operation 是启动错误，不能通过覆盖路由或挂载顺序改变权限。全局挂载前缀由 HTTP 的 apiPrefix 配置；模块路由元数据不能携带可自选可信身份。开放接口版本首版示例为 /v1。

每个 API 模块 Config 为空，声明固定相对路由，HTTP 服务通过 apiPrefix 统一挂载（默认 /v1）；Deps 只包含下表中的业务接口。领域 Service 在确有多个操作需要时组合小接口，不增加通用 Execute 或任意状态设置接口。

| kind | Deps | 对前端和 Agent 提供的能力 |
| --- | --- | --- |
| `api/tasks` | `tasks: task.Service` | 创建、详情、过滤列表、目标与标准修改、负责人及参与者管理、归档 |
| `api/conversations` | `conversations: conversation.Service` | 消息增量、发送、按修订读取、引用回复、编辑和已读 |
| `api/decisions` | `decisions: decision.Service` | 请求输入/决策、答复、解决及失效处理 |
| `api/plan` | `plan: plan.Service` | 任务树、依赖、影响查询、成组修改及恢复 |
| `api/execution` | `execution: execution.Service` | 分配与接受、执行登记、进度、结束、停止请求和确认停止 |
| `api/deliveries` | `delivery: delivery.Service` | SubmitReply 原子创建/登记结果回复引用、读取、版本列表及撤回；正文复用消息 |
| `api/acceptance` | `acceptance: acceptance.Service` | 验收策略、记录、结论和退回意见 |
| `api/artifacts` | `artifacts: artifact.Service` | 附件、版本、上传内容、引用、下载和删除申请 |
| `api/flow` | `flows: []flow.Service` | 按任务 FlowRef 查询定义、位置及阻塞原因，取消、退回、重开；外部不暴露 Initialize/Wake |
| `api/access` | `grants: authorization.GrantManager`、`confirmation: confirmation.Service` | 授权和撤销、逐次操作确认 |
| `api/query` | `query: query.Service` | 工作台、搜索、版本差异、历史、审计及授权事件查询 |
| `api/notifications` | `notifications: notification.Inbox` | 收件箱、已读和偏好 |
| `api/changes` | `feed: feed.Reader` | 通用变化游标与 HTTP 增量补拉，不组织 Agent 上下文 |

api/execution 不是专用 Agent API：人或有权限的系统也可读取记录、分配及请求停止，Buddy 按其身份提交进度、结果和停止回执。更换客户端不改变业务权限和幂等规则。

默认结果为 Agent 的结果回复，普通发言不自动交付；消息编辑与正式结果再次提交分开，详见[交付契约](reply-delivery.md)。

## 3. `task/feed`：面向客户端的授权事件能力

**能力：** `feed.Reader.StartCursor/ReadChanges`；`feed.Subscriber.Open`，返回具有 `Next/Ack/Close` 的受控 Stream。HTTP 和 WebSocket 使用相同过滤、游标及权限语义。

**Config：** `maxBatchSize: int`、`pollInterval: Duration`、`maxReplayAge: Duration`。

**Deps：** `events: event.Reader`、`cursors: event.CursorStore`、`tasks: task.Reader`、`authz: authorization.Authorizer`。

订阅过滤可按明确任务列表、本人负责/参与范围、当前 Buddy 被分配或允许代理的任务范围设置。服务端按当前身份解释“本人”，不能由客户端伪造用户或 Buddy ID。任务创建和分配变化必须支持动态匹配，不能只订阅连接时已有的任务 ID。

对外 Change 至少包含：事件 ID、可补拉游标、任务 ID、任务前后版本、变更类型、涉及对象引用、服务端时间及关联操作 ID。状态流转附前后状态 key；其他事件仅在确有状态变化时填写该字段。TaskVersion 与需求修订版本、消息序号分别表达，不宣称可相互替代。事件不携带模型上下文、完整历史或工具凭据；必要的变更摘要也必须鉴权。领域写入需在同事务捕获前后版本，不能推送时查询当前内容伪造历史。

客户端先取得 StartCursor，再通过普通业务 API 读取所需快照，随后从该游标补拉/订阅；根据对象版本去重和重新读取，避免先快照后订阅的空窗。不要求多个业务接口快照天然跨对象一致，必要时按版本重读。游标是受企业、主体和过滤范围约束的不透明值，不能假定 SQL 自增 ID 就是提交顺序。

Open 验证订阅权限，逐次 Next/ReadChanges 发送前重新校验当前权限；撤销后停止投递和关闭相关范围，不泄露旧内容，可发送仅含必要标识的访问失效提示。原始内部 outbox 不能直接暴露给客户端。

Ack 只表示客户端接收进度，不代表 Agent 已理解、执行或任务完成；执行确认与停止回执仍是业务写入。重复投递正常，按事件 ID 去重。持久消费游标与连接临时状态分离；同一主体的多个客户端使用独立 subscriptionID，服务端不能让一个连接的 Ack 推进其他连接。

游标超期明确返回 resync_required，重新获取 StartCursor 和业务快照。慢客户端采用有界缓冲，超过上限断开并要求按游标补拉，不无限占用内存。保留期内补拉与低频核对用于恢复，不能承诺无限历史重放。

## 4. `transport/websocket`

**能力：** `httpapi.Module` 和可选的公共 `httpapi.ModuleLifecycle.Open/Close`，由 HTTP 模块 owner 负责挂载和长连接生命周期。只通过已命名的公共接口识别受管模块，不依赖具体实现类型。

**Config：** `path: string`（相对于 apiPrefix，默认 /events/ws）、`heartbeatInterval: Duration`、`idleTimeout: Duration`、`maxPendingMessages: int`、`maxSubscriptions: int`、`allowedOrigins: []string`。

**Deps：** `feed: feed.Subscriber`。

握手认证沿用 HTTP 服务；浏览器 Origin 校验、凭据有效期及重新连接策略需明确。客户端不得通过消息覆盖握手主体；不在 URL 查询参数中放访问凭据。订阅、取消订阅、Ack 与心跳是连接协议，不承担业务字段修改。Stop/Close 关闭连接及其读取循环，不另建全局连接注册表；连接会话只持有生命周期内资源。

## 5. `transport/http` 与根服务

**transport/http Config：** `listen: string`、`apiPrefix: string`、`readTimeout: Duration`、`writeTimeout: Duration`。

**Deps：** `identity: identity.Authenticator`、`modules: []httpapi.Module`。

普通请求超时不能直接套用到升级后的长连接。路由就绪、资源打开后才接受请求；关闭时停止新请求，终止 WebSocket 订阅循环，再释放 SQL/S3。HTTP owner 打开已注入的受管模块，成功后才开始服务；失败清理已打开部分，退出时关闭 WebSocket 连接及订阅再释放模块。根插件只负责 HTTP 组件和 SQL/S3 资源，不能重复登记 WebSocket，避免双重启停或漏配。

## 6. 执行与状态链调整

- 删除 agentbridge、agentconnector、专用 callback transport、Agent 派发 worker 及其凭据配置。
- 移除 GetTaskContext、远端 Dispatch/GetRun/BindRemoteRun、execution.Requester 及执行请求仓储；任务系统不再管理远端运行连接。
- 保留逻辑 Buddy、实例、执行 ID、当前所有权、代次、依据版本和停止确认。Agent 经共享 API 登记或认领有效执行后才能提交正式交付，不能因收到事件即获得写入权。
- 指定执行者通过 execution.AssignBuddy；未指定时，OpenExecution 可按负责人默认 Buddy 关系解析并在同事务校验和绑定，不增加自动派发消费者。其他 Buddy 不能抢占不属于其授权范围的任务。
- state-executing.Enter 仅建立本地状态进入基线，可为空操作，flow 会持久化状态及事件。Agent 收到变化后自行读取并判断是否需要处理；问题答复、消息、材料更新同样可进入已有 Agent 层的 steer，不必重复进入状态或重开执行。
- execution.RequestStop 保留为通用业务操作；取消后 Agent 从 task.cancelled 通知获知并通过同一 execution.AcknowledgeStop 确认，服务端不能把通知送达当成已停止。任务离线时取消仍是持久事实，重连须读取当前状态；旧代次写入被拒绝。
- “steer”只在已有 Agent 层实现，任务系统不决定具体运行时如何注入、压缩、恢复、唤醒或抑制自回复循环。

## 7. 验证范围

完整默认组装见[base](../base.yaml)，日常覆盖见[config](../config.yaml)，两层规则见[配置说明](configuration.md)，尚无插件实现。静态验证覆盖 YAML、引用、无环、所有实例根可达、API 模块依赖、状态链与移除旧派发节点；不代表 HTTP、WebSocket 或 PluginKit Go 构建已经验证。

实现需验证：同一 API 对人和 Buddy 的权限差异、路由冲突、动态订阅新任务、只改消息而不改状态的推送、快照与订阅空窗恢复、重复与乱序事件、跨任务隔离、权限撤销、游标超期、慢客户端、断线取消、有效执行认领及关闭时连接清理。
