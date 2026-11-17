# 任务流转：状态子插件列表与 Hook

日期：2026 年 11 月 17 日。状态：已确认设计方向下的接口草案，尚未实现。

## 1. 职责与依赖方向

每个任务绑定一份有版本的状态插件有序列表。状态插件负责进入检查、进入动作和完成检查；`task/flow` 是任务流程位置及业务状态的唯一写入者。Hook 接收正式事件，定位受影响任务并请求重新检查，不直接修改状态，不把普通聊天或函数返回成功当成业务完成。

依赖方向：事件工作组件 → 流转 Hook → task/flow → 状态子插件 → 任务、执行、交付、验收、依赖等领域能力。领域插件保存事实并发事件，不反向依赖 flow 或 Hook。WebSocket 独立推送任务变化，Agent 经共享业务 API 按需读取并在已有运行时 steer；详见[通信契约](shared-api-and-realtime.md)。SQL/S3 及现有 Agent 层边界不变。

配置支持为不同任务类型组装不同列表，不需要统一应用编排层。复杂状态可继续通过 Deps 注入完成检查器等子插件；单纯辅助函数不必插件化。

## 2. 核心能力与类型

以下是 Go 契约草案，省略包限定符及非关键 DTO 字段；不是可编译实现。

```go
type State interface {
    Key() StateKey
    Describe() StateDescriptor // kind、规则版本、行为配置及子依赖摘要，不含密钥
    CheckEnter(context.Context, EnterRequest) (CheckResult, error)
    Enter(context.Context, EnterRequest) (EnterResult, error)
    CheckComplete(context.Context, CheckRequest) (CheckResult, error)
}

type CheckResult struct {
    Outcome  CheckOutcome // allow | wait | reject
    Reason   string
    Evidence []EvidenceRef // 对象 ID、版本、结论，不接受无来源的“通过”
    Waiting  []WaitItem    // 等谁、等什么、是否需人工介入
}

type EnterRequest struct {
    TaskID      TaskID
    FlowRef     FlowRef
    EntryID     EntryID // 每次进入都生成新 ID，退回后再进也不复用
    TaskVersion Version
    Cause       CauseRef
}

type CheckRequest struct {
    TaskID      TaskID
    FlowRef     FlowRef
    EntryID     EntryID
    TaskVersion Version
    Cause       CauseRef
}

type EnterResult struct {
    Records []RecordRef // 本事务内形成的领域记录，可为空
}

type Controller interface {
    Initialize(context.Context, InitializeRequest) (FlowResult, error)
    Wake(context.Context, WakeRequest) (FlowResult, error)
    Return(context.Context, ReturnRequest) (FlowResult, error)
    Cancel(context.Context, CancelRequest) (FlowResult, error)
    Reopen(context.Context, ReopenRequest) (FlowResult, error)
}

type Reader interface {
    GetDefinition(context.Context, DefinitionRequest) (Definition, error)
    GetPosition(context.Context, PositionRequest) (Position, error)
}
```

`FlowRef` 包含流程 ID、版本及定义摘要；`Position` 包含当前 StateKey、列表位置、EntryID、流程运行版本和等待原因。`FlowResult` 区分 advanced、waiting、rejected、unchanged、terminal，返回新位置和依据。技术失败返回 error，不伪装成业务拒绝。

所有写请求遵循主契约的幂等、身份、预期版本及审计约定。`Return`、`Cancel`、`Reopen` 是独立的有权限业务命令，不能用 `Wake` 绕过。

检查的 allow 含义随方法明确：CheckEnter 表示可进入，CheckComplete 表示业务条件已满足；wait 表示可在后续事件后重查，reject 表示规则拒绝，需要明确修改或人工处理，不自动跳过。所有检查只读，依据来自可信当前业务事实。Enter 只做本地幂等写入及 outbox 记录，不调用 Agent、S3 或其他远端接口。执行状态可以没有额外写入，由 flow 的状态事件通知客户端。

`task/default` 不再提供公共 Transition；其更新接口不得改流程状态字段。`flow.Repository` 与任务仓储使用同一 SQL 事务及同一任务并发版本：状态提交同时更新任务版本、当前状态与流程位置，任务 Reader 读取这一权威位置，不能再维护独立可写状态副本。

## 3. `task/flow` 主插件

**能力：** `flow.Controller`、`flow.Reader`；配置中使用的 flow.Service 只是两者的组合接口。

```go
type Config struct {
    ID              string             `json:"id"`
    Version         string             `json:"version"`
    TerminalState   StateKey           `json:"terminalState"`
    MaxAutoAdvances  int                `json:"maxAutoAdvances"`
    ReturnTargets   map[StateKey]StateKey `json:"returnTargets"`
    ReopenState     StateKey           `json:"reopenState"`
}

type Deps struct {
    States       []flow.State             `json:"states"`
    Store        flow.Repository          `json:"store"`
    Tasks        task.Reader              `json:"tasks"`
    Authz        authorization.Authorizer `json:"authz"`
    Confirmation confirmation.Service     `json:"confirmation"`
    Tx           persistence.UnitOfWork   `json:"tx"`
    Requests     idempotency.Repository   `json:"requests"`
    Changes      revision.ChangeRecorder  `json:"changes"`
    Audit        audit.Recorder           `json:"audit"`
    Events       event.Writer             `json:"events"`
}
```

列表顺序只写在 `Deps.States`，Config 不再重复状态列表。构造时要求非空列表、唯一 StateKey、最后一个 key 等于 terminalState，returnTargets 的来源和目标存在且只允许回到较早状态，reopenState 存在。按整数索引作为内部位置，但持久身份使用稳定 key 与版本，不用可变数组下标标识历史。

任务创建时记录 FlowRef，创建事件驱动 Initialize。初始化尚未完成时展示“待初始化”，不伪造已经进入第一个状态。初始化及自动推进流程如下：

1. 在 SQL 事务中锁定或按版本保护任务与流程位置，读取当前事实；重复或过期事件重新读取，不沿事件载荷中的旧位置推进。
2. Initialize 检查首状态是否可进入，允许时在本事务内写首位置并建立 EntryID、调用 Enter，随后继续正常完成检查；等待时保留未初始化位置及原因，拒绝时记录拒绝。
3. Wake 检查当前状态 CheckComplete；未完成则保留当前位置和明确等待原因。
4. 完成后检查紧邻的下一状态 CheckEnter；等待或拒绝时仍保留当前状态，记录下一状态的进入障碍，不能先改状态后补条件。
5. 允许进入时，在同一事务建立新 EntryID，先条件写入新位置及任务版本，使 Enter 内部读取能看到本次进入，再调用下一状态 Enter，保存变更、审计和 outbox。Enter 出错则整个事务回滚，重试仍从上次已提交位置开始。
6. 可以继续检查已进入的新状态；单次 Wake 最多前进 maxAutoAdvances 次。达到上限且还有待检查位置时，同事务写 `flow.check-requested` 事件，由 Hook 再处理；不能静默停住或无限忙循环。等待时不自行发检查事件。
7. 抵达末状态即表示本轮流程完成，不再调用其 CheckComplete 尝试向后推进；进入检查是最后一道完成保障。同事务发 `task.completed` 及状态变更事件。

为了保证本地检查到提交之间的一致性，交付、目标、标准、计划等证据版本要纳入校验或锁定范围，隔离级别和必要锁由实现明确。修改相关事实的领域操作必须增加相应版本并产生事件。自动推进不消费用户逐次确认来冒充其他操作；若状态动作需要新确认，检查返回 wait，用户走确认命令后再检查。

取消是流程外的控制标记：记录 cancelled、原位置及新代次，并发 `task.cancelled`。不需要在顺序列表中插入取消状态。后续 Wake 不推进已取消流程，执行停止通过共用业务 API 单独确认。Reopen 和 Return 创建新 EntryID、使旧进入动作及执行依据失效，按目标状态 CheckEnter 后 Enter；拒绝、等待时保留原位置。重开或退回前必须先检查旧执行是否已停止或安全隔离，未满足时保持原位置并返回等待，不能只靠状态回退就重跑外部副作用。

重开后的新一轮检查不能复用旧交付、旧验收直接再次完成；进入动作将本轮所依据的目标版本和交付/执行基线绑定新 EntryID。退回来源、目标、原因及过期证据都保留。

## 4. 默认状态子插件

每个具体插件均实现 flow.State。下表字段为显式 Config/Deps，空配置写 `Config struct{}`。状态插件不依赖 flow.Controller，不提交任务状态。

| kind | Config | Deps | 进入与完成职责 |
| --- | --- | --- | --- |
| `task/state-ready` | `key: string` | `tasks: task.Reader`、`identity: identity.Directory`、`plan: plan.Reader`、`decisions: decision.Reader` | 可作为首状态；检查目标、人的负责人、必要输入、分配资格、前置依赖及阻塞决策是否满足。完成依据不等于任务可直接完成 |
| `task/state-executing` | `key: string` | `tasks: task.Reader`、`plan: plan.Reader`、`decisions: decision.Reader`、`executions: execution.Reader`、`deliveries: delivery.Reader` | CheckEnter 检查允许执行；Enter 只建立本次进入的本地依据，不启动或派发 Agent；flow 保存状态变化及 outbox，Agent 自主读取；完成检查要求本轮有效结果回复交付引用和必要子任务条件满足，不以 Agent 运行结束为完成 |
| `task/state-accepting` | `key: string` | `tasks: task.Reader`、`deliveries: delivery.Reader`、`acceptance: acceptance.Service` | CheckEnter 要求当前有效结果回复交付；Enter 幂等建立绑定交付 ID、回复修订及标准版本的验收；完成要求相同有效版本验收通过。退回仍是独立 Return，不由检查函数写状态 |
| `task/state-completed` | `key: string` | `completion: completion.Checker` | CheckEnter 执行最终完成检查；Enter 无远端动作；为末状态，CheckComplete 不参与继续推进 |

验收退回作为显式 Return 命令，来源和目标受主插件 returnTargets 限制；Hook 不因验收拒绝擅自回退或重跑。状态保留验收拒绝原因，通知有权限的人或 Buddy 决定后续操作。

### `task/completion-check`

**能力：** `completion.Checker.Check(context.Context, CompletionRequest) (CheckResult, error)`。

**Config：** `requireAcceptance: bool`、`requireChildrenComplete: bool`。

**Deps：** `tasks: task.Reader`、`deliveries: delivery.Reader`、`acceptance: acceptance.Reader`、`plan: plan.Reader`、`decisions: decision.Reader`。

组织最终完成证据：当前目标与标准、本轮有效结果回复引用、是否有未解决阻塞项、子任务条件，以及该流程是否要求验收。无需独立验收的流程只关闭 requireAcceptance，仍须满足其他完成条件；不能只移除验收状态却继续绑定强制验收检查器。复杂规则以后可继续拆分检查子插件，经 Deps 组合，不增加全局规则容器。

`acceptance.CompletionEvaluator` 由此移除，验收插件只提供验收事实和规则。交付插件不再决定完成，默认 delivery/reply 保留 SubmitReply/Withdraw 和轻量引用版本管理；正文归会话。见[结果回复契约](reply-delivery.md)。

## 5. `task/hook-flow`：事件到检查请求

**能力：** `event.Subscriber`，包含 `Handle(ctx, Event) error` 和 `Topics() []string`。

**Config：** 无。默认插件在实现中声明 Topics 及目标影响范围（task、dependents、descendants、ancestors），不是逐项配置的路由表。相同事件的受影响目标取并集并去重；需要不同语义时通过 deps 替换 Hook 子插件，不引入脚本或任意目标状态。

**Deps：** `flows: []flow.Service`（Controller + Reader）、`tasks: task.Reader`、`plan: plan.ImpactReader`、`authz: authorization.Authorizer`。

- 根据事件定位任务，按其持久 FlowRef 从注入列表选择流程；只允许配置内、且版本和摘要匹配的流程，不动态查全局容器。
- task 范围为事件所属任务；dependents 查直接及传递阻塞依赖的下游；descendants 查子树，ancestors 查父任务链以便子任务变化后重查汇总条件。plan.ImpactReader 提供 `ListDependents`、`ListDescendants`、`ListAncestors`，均分页并受读取范围约束。计划变更事件携带变更前后受影响 ID，使用前后目标并集，避免关系删除后漏查旧下游。
- 附件等事件必须携带已授权关联的任务 ID；多任务引用形成逐任务受影响列表，不能猜测 taskID。单条事件涉及多个任务时，每个任务独立事务和幂等键，部分失败可重试。
- 默认 Topics 覆盖 task.created、flow.check-requested、目标/负责人变化、执行分配及结束、交付提交与撤回、验收结论、决策创建/解决、材料可用及 plan.changed；任务完成/重开/取消重查下游，子任务完成/重开重查父链，目标变化重查子树。此默认映射作为插件行为契约维护。
- task.created 调 Initialize；其他相关事件调 Wake。流程初始化遗漏后仍可补偿 Initialize，不能以事件消费顺序假定已经初始化。
- Hook 只按已授权流程规则自动推进，使用有企业和任务范围限制的自动化执行主体，记录原始事件和发起者归属，不能长期重放用户旧权限或创建无限权系统身份。实际副作用仍由领域和 Agent 层授权。
- 重复事件先读取当前事实；执行代次及 EntryID 保护旧执行写入。对完成或取消流程，普通 Wake 不重开；目标或交付变化应标记需重新评估，只有显式 Reopen 启动新轮。
- 事件读取有保留期，超期恢复通过任务状态核对或人工重放；不能承诺无限历史补拉。

列表中每个 flow.State 都是可配置能力，Hook 仅触发主插件检查。业务消息不自动修改状态，hook 失败由事件工作组件重试；检查返回业务 wait/reject 时算事件已处理，等待新的事实事件，不触发无意义技术重试。

事件工作组件从注入的 event.Subscriber.Topics 获取订阅集合，不再另配 topics。通知插件由 enabledTopics 给出订阅范围，默认流转 Hook 自身声明 Topics；避免同一订阅列表配置两次。

## 6. 持久化、版本和特殊流转

SQL 增加 `flow.Repository`：`LoadDefinition`、`SaveDefinitionIfAbsent`、`LoadPosition`、`InitializePosition`、`CompareAndSwapPosition`。位置写入原子更新任务状态与任务版本，返回新版本；取消及重开也通过这个入口。定义包含列表稳定 key、kind、Config、子依赖的行为版本引用及摘要，不含明文凭据；由 State.Describe() 显式提供，主插件不能通过具体实现类型断言偷读私有配置。

任务创建请求增加 FlowRef；task/default 的 defaultFlow 配置只给出引用，不注入 flow 主插件，避免 task → flow → state → task 的依赖环。Initialize 校验引用并保存不可变定义；定义不匹配时明确报错，任务仍可见且待初始化。正常部署应预校验默认引用存在。旧任务引用的流程版本必须继续可解析，不得通过重排配置偷偷迁移；升级行为需要新流程版本及显式迁移策略，首版可先不支持迁移。

流程定义版本包含可影响行为的子插件 Config/规则版本；仅保留相同列表 key 不能保证行为未变。发布方需保留旧版规则实现或明确旧版不兼容而停止自动推进，不能只保存 YAML 就承诺可运行任意旧插件。

默认演示路径：待处理 → 执行中 → 待验收 → 已完成。第二条演示路径：待处理 → 执行中 → 已完成。等待原因附着当前位置，不必新增等待状态；若产品需要独立等待状态，可以实现额外 State，但不能将临时阻塞误作前一阶段已完成。取消为独立控制标记，失败及不可行记录为事实和等待/拒绝原因，是否增加独立末状态由后续产品规则决定。

## 7. 入口和其他插件调整

- task/default：保留信息、负责人、参与者、归档管理；移除公共 Transition，取消及重开走 flow.Controller。
- execution/default：保留分配、接受、有效执行认领、进度、结束及停止请求/确认；移除执行请求登记、远端运行绑定和 taskWriter。Agent 通过共享业务 API 提交这些事实。
- delivery/reply：通过 replies 注入会话小能力，原子创建/读取并固定结果回复修订，登记交付引用及事件；不直接写状态、创建验收或复制正文。
- acceptance/default：保留 SetPolicy/OpenReview/RecordVerdict、读取能力；移除 CompletionEvaluator，不直接变更任务状态。共享 API 保存验收结论，Hook 统一触发检查。
- 状态进入执行时只持久化本地依据和任务变化，不产生远端派发请求。消息、决策、材料及状态变化都可经 WebSocket 到已有 Agent 层，用作 steer。
- transport/http 通过 modules 注入业务 API 和 WebSocket 路由；api/flow 按任务 FlowRef 路由显式取消、退回、重开及查询。无专用 Agent 上下文或回调 API。
- event/worker：handler 依赖改为 event.Subscriber，删除 topics Config，订阅只取处理器 Topics()。

## 8. 验证与示例

完整默认组装见 [base](../base.yaml)，日常覆盖写入 [config](../config.yaml)，两层语义见[配置说明](configuration.md)。base 只包含默认人工验收流程，[无需验收片段](../examples/flows/no-review.config.yaml)独立展示新增流程及默认引用覆盖；状态列表通过 deps.states 组装；示例值不是最终产品默认值，插件尚未实现，不代表可以启动。

实现至少验证：进入/完成检查的三种结果、下一状态阻塞时保留原位置、进入动作事务回滚、重复 hook 幂等、证据并发变化、单次推进上限的续查、退回重开新 EntryID、旧执行隔离、取消与停止区别、无验收仍验证交付、跨任务依赖 fan-out、关系删除影响、旧流程版本引用及无环配置构建。已完成的静态检查只能证明 YAML 结构、引用及实例图，不证明业务语义或 PluginKit Go 类型兼容。
