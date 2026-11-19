# M05：执行、结果、验收与取消

M04/权限/cap 简化已提交为 `f71bde2`。M05 在其上提供本地任务执行与交付闭环；本页区分已实现协议/SQLite/界面验证与尚未进行的真实 Agent 验证。没有实现 Harness 适配或 Agent 运行时。

## 运行与身份

沿用 M04 的私有管理员凭据、两层配置和本地工作台 `/v1/app`。默认新任务绑定 standard/1：ready → executing → accepting → completed；原 preparation/1 保留，只提供准备行为，不偷偷升级旧任务的 FlowRef。

管理员可通过本地受控 CLI 登记 Agent 并分别签发业务/运行时证明凭据：

```sh
go run ./cmd/taskloom-admin -mode enroll-agent -tenant example \
  -subject worker-1 -owner admin -instance machine-1 -incarnation boot-1 \
  -purpose business -operator local-operator -reason agent-enrollment \
  -credential-file /private/tmp/taskloom-worker-business.json
# 同一绑定另行执行 -purpose runtime，使用新的私有输出文件。
```

CLI 不打印秘密，不覆盖文件，保留操作者、原因、owner/实例/用途审计。登记/签发不自动分配任务；负责人或管理员通过“分配 Agent ID”显式分配，原子建立该 Agent 的任务参与关系。Agent 只能认领自己的分配，仍需有效成员身份、业务用途凭据和匹配的实例/incarnation。owner 归属不授予任务或验收权限。更换实例需新的凭据绑定，旧执行未安全退出不得替换。

## 领域与装配

| 模块 | 当前职责与 Deps |
| --- | --- |
| commands/execution | 分配、认领、主运行结束、停止证据，并提供 execution.Progress（记录执行者最近进展）；9 项：principals、members、participants、commands、tasks、flow、executions、snapshots、events |
| commands/result | 原子提交正文/交付、撤回、调用验收 State 的命名动作；9 项：commands、tasks、flow、executions、results、commits、accepting、snapshots、events |
| commands/flow | 取消、退回、重开、管理员隔离证明；6 项：commands、tasks、flow、executions、snapshots、events |
| query/workflow | 当前执行/交付/验收与操作提示；9 项：commits、transactions、tasks、flow、executions、results、reviews、identity、authorization |
| task/flow | 定义固定、位置/轮次唯一写入者；definitions、positions、states、executions；states 有序组合 |
| flow/directory | defaultID/version 与 flows 列表，按任务绑定引用路由；positions 只读、flows 列表 |
| state/executing | 检查当前轮当前依据的有效交付；只读 results |
| state/accepting | 自有 pending/结论记录和当前资格判断；results、reviews、members；提供 RecordVerdict，不依赖 flow/命令 |
| state/completed | 当前交付有效性；只读 results，控制器另执行固定安全退出门 |
| api/workflow | 独立协议模块；executions、results、controls、workflow 查询、questions、security 六项 |
| api/mcp | MCP（Streamable HTTP）协议适配，见 [MCP 接入](mcp-adapter.md)；tasks、comments、workflow、executions、results、questions、security 七项，均为面向执行 Agent 的局部窄接口 |

超过五字段保留理由：各命令模块必须显式协调事务内领域事实，但分配/执行、交付/验收、流程控制保持独立 provider；只读聚合单独提供内部 FactsReader，命令不额外持有查询仓储写能力。共同代码是包内辅助逻辑，不是依赖包或服务定位器。HTTP 的 Origin/CSRF/会话凭据解析作为内聚 RequestSecurity 复用，HTTP owner 只启动/关闭一次会话模块。没有新增 registry 或配置层。

根默认图为 25 个实际实例（含后续新增的 catalog、question-commands 与 mcp-api），见[逐字段依赖图](m05-dependencies.md)，121 条字段目标边，无环且全部根可达。准备定义与标准定义共存；标准定义的行为摘要包含有序状态描述。改同 ID/版本的步骤/规则导致不匹配时拒绝，不能继续沿旧位置执行新规则。示例片段必须并入同一个 config.yaml；不能作为第三层加载。

## 执行、进展与停止

认领在同 SQL 写事务内检查任务/轮次/依据、流程 Revision、明确分配、主体/实例、已到达步骤以及执行角色。两个并发认领最多一个成功；Generation 按任务单调增长，换轮也不复用。正常状态推进保留 RoundID，EntryID 每次进入变化。

执行没有租约、心跳或超时。执行记录 `ProgressAt`：认领、执行者本人留言（workspace 经 execution.Progress）、提问（question-commands 经 execution.Progress）和提交结果时在同一事务内更新；他人留言不算。它只供负责人和 Agent 判断进展，不推动流程 Revision、不产生状态变化，也不自动重派；长期沉默的执行需负责人留言、隔离或改派。没有后台扫描器。主运行 End 不证明子进程或外部工作已停止，结果与 End 可前后独立提交；安全结束且未失效/被替换的执行可以补交。

停止证明需 runtime 用途凭据，绑定 ExecutionID、Generation、实例/incarnation，以及服务端 challenge：自然结束使用 EndEvidenceID，已请求取消则必须使用 StopRequestID。业务凭据不能证明清理。MainStopped、ChildrenStopped、ExternalSettled 全部明确成立才标记安全退出；缺失/部分证明保持 unknown，保存不可变来源与接收时间。没有任意工具副作用“恰好一次”或自动回收保证。

取消立即保存 cancelled 意图、失效业务写资格、持久化 stop-requested 事件；不等待远端调用，不把请求送达当停止。旧实例的普通提交/结束拒绝。仍有效的 runtime 凭据可以受限关闭自己的旧执行，即使任务参与关系已移除；回执不返回任务内容，不改当前结果或新执行。成员停用或凭据撤销始终拒绝，恢复需管理员明确处理。

管理员可记录有操作者、原因、隔离范围的外部隔离证明；状态为 isolated，失效写资格，不伪称执行已停止。仅有效人类租户管理员可用，普通负责人资格不足。Taskloom 保存该运维证明，不自行验证物理隔离；真实使用必须有实际证据。该入口用于无法再取证的受控恢复，不是自动超时解锁。

## 交付、验收与一致性

Submit 原子追加不可变 result commit、登记 Submission，再核对已到达步骤；正文只存一份，交付/评审引用 CommitID。当前首版提交新文本；普通评论不能伪造 result 类型。同执行、轮次、依据下重复相同正文的新请求键会返回当前候选，不重复登记；不同正文生成新 commit 和候选。并发/旧依据请求冲突，技术失败回滚正文/交付/步骤/事件/回执。

人工验收 State 默认绑定创建时负责人（当前负责人无转移 API），PolicyVersion=1；记录绑定轮次、EntryID、SubmissionID、CommitID、BasisVersion、PolicyVersion 和 reviewer。首版不提供策略编辑/职责分离开关；未知配置或请求字段拒绝，不假称复杂策略已实现。角色授权不代替验收资格，管理员不是自动 reviewer。

新候选使旧结论不再当前：同步所有已到达状态的 Enter，创建对应候选的新 pending 记录，再重查整个前序。审核 wait/reject 保留合法交付，技术错误整体回滚；旧验收点击或外部确认不能覆盖新候选。撤回当前候选不恢复旧候选，历史正文/交付/结论保留。

验收通过、执行结束、安全退出、任务完成分别记录。完成前重查当前轮全部已到达步骤和所有相关执行的安全退出；末状态完成才保存 completed。终态普通评论不重开任务，也不能提交/撤回改变当前结果。流程乐观版本使用 Position.Revision，任务依据写入仍使用 Task.Version，心跳不递增 Task.Version。

退回只走显式受权命令，安全退出后新轮回准备位置；重开只允许 cancelled/completed 且安全。均失效旧执行写资格，当前轮候选/结论为空，历史不删除。新轮尚无执行时可更新依据；若有人已认领则拒绝编辑，不能在运行中悄改标准。初版通过“安全退回→修改依据”完成采用新要求，冲突时需刷新，未提供跨远端动作的原子操作。

## API 与薄界面

下列路径均为 `/v1/tasks/{id}` 下的命名操作，业务写需 Idempotency-Key，Cookie/Origin/CSRF 与 M04 一致；Agent 可用同一 API 的 Bearer 认证。Basis 字段为 RoundID、BasisVersion、ExpectedRevision（流程 Position.Revision；任务依据修改另用 ExpectedVersion 即 task.Version，旧字段名在流程命令上按未知字段拒绝），界面自动读取，不要求手工填写标识。

| 路径 | 行为 |
| --- | --- |
| GET /workflow | 受权快照、最近执行、当前交付/正文/验收、可用操作提示 |
| POST /execution/assign | Basis + Subject，负责人/管理员或当前人工处理人分配用户或 Agent |
| POST /execution/open | Basis，匹配分配的用户或业务 Agent 认领 |
| POST /execution/end | ExecutionID/Generation，绑定自身实例 |
| POST /execution/proof | ExecutionID/Generation/Challenge + 三项完整性事实，仅 runtime 凭据 |
| POST /execution/isolate | Basis + ExecutionID/Generation/Reason/Scope，仅管理员 |
| POST /results | Basis + ExecutionID/Generation/Body |
| POST /review | Basis + SubmissionID/CommitID/Outcome/Reason，Outcome 为 approved/rejected；rejected 必须给出非空 Reason，否则按输入错误拒绝且不改变评审 |
| POST /withdraw | Basis + SubmissionID |
| POST /cancel、/return、/reopen | Basis，当前资格、版本与停止门重新检查 |
| POST /questions | RoundID + Body，执行者等参与者向负责人提问 |
| POST /questions/{qid}/answer | Body，仅被提问的负责人回答当前轮的未答问题 |

界面支持角色切换、分配、认领/结束、结果区、验收/撤回、取消、退回、重开及管理员恢复表单。布局为左侧任务列表（“待我处理/全部”，标出状态与等谁）与右侧详情：顶部“下一步”卡片（等谁、等待原因、待验收时的验收与分配入口），主栏为当前结果与时间线，侧栏为任务约定、执行、参与者、任务控制及仅对 Agent 会话显示的“Agent 调试”。界面不按状态名判断可用操作，只依据快照的 Can*/Blocked/NextActor；状态名仅用于显示。退回、取消、撤回、重开需二次确认；退出登录会清空当前详情。结果正文按稳定引用读取，不依赖时间线首屏。网络结果不明确时保留请求幂等键；编辑期间保留输入并提示刷新。查询操作提示不是授权凭证，服务端每次重新检查。

快照另带两项只读说明：`NextActor`（owner/assignee/reviewer，空表示没有固定角色需要行动，步骤专有原因仍看 `Position.Waiting`）与 `Blocked`（仅对当前查看者相关的 open/end/submit/review 给出不可用原因码，例如 not_assigned、execution_in_progress、basis_changed、not_reviewer，完整列表见 `cap/workflow`）。每个 Can* 与其原因码出自同一判断函数，避免提示与开关漂移；原因码只解释当前状态，不是授权凭证，也不替代命令写入时的重查。

`GET /v1/inbox` 返回当前调用者可读且 NextActor 指向自己的任务（TaskID/Title/NextActor）：负责人对应 owner、验收记录的 reviewer 对应 reviewer，assignee 对应当前处理人（人或 Agent）；仍未安全停止的 Agent 执行优先指向原执行主体。它与快照共用 ReadFacts 派生，不保存收件箱记录、不发通知；管理员恢复项暂不列入。当前逐任务读取，规模评估归 M11。工作台任务列表以 ● 标记并可“只看待我处理”。

## 请求输入与人工引导

人可以随时在时间线给执行 Agent 留言（普通 comment），补充说明、纠正方向或提出要求；Agent 经变化推送得知并自行决定如何处理，留言不算交付，也不改变正式依据。执行开始后不能直接修改目标/标准：正式改要求须先“退回并开始新一轮”再修改，界面在约定卡片中说明这一点。

Agent（或除负责人外的其他参与者）卡住时用 `commands/question` 提问：问题正文是不可变的 question commit，`cap/question` 记录只保存轮次、提问人、被提问人（固定为负责人）、状态及回答 commit 引用。未回答的问题使快照 `Questions` 非空、`NextActor=owner` 并进入负责人的 inbox。回答追加 answer commit（RefID 指向问题），以“仍为 open”条件更新，重复回答冲突；只有被提问的人类负责人可答，且只限当前活跃轮。旧轮或已取消/完成任务的问题不再显示也不可回答，无需额外写入。

当前轮存在未回答问题时任务不能完成：这道门与“全部执行安全停止”一样由 flow 控制器固定执行（`task/flow` 多步定义必须注入 questions），不进入状态描述和 FlowRef 摘要，因此不改变已有定义的版本。Agent 仍可继续执行、提交结果和被验收，只是最后完成被挡住，Waiting 显示“等待负责人回答问题”；回答命令同事务刷新流程，条件满足即完成。

question-commands 有 7 个 Deps（命令生命周期、任务、流程、时间线、问题记录、快照、事件），与其他命令模块同理保留；query/workflow 因此增至 10 个只读依赖，api/workflow 增至 6 个协议依赖，均不引入写接口或服务定位。

## 状态扩展示例

- `state/factcheck`：纯本地文本规则示例，用配置 requiredText 检查结果，规则摘要纳入定义。它演示本地事实/质量门，不代表模型事实核验服务。
- `state/checkpoint`：拥有自己的候选绑定记录、Enter 和完成检查；`api/checkpoint` 提供人工/异步确认，原 flow/交付无需理解专有记录。
- `examples/flows/with-checkpoint.config.yaml`：保留旧定义并新增 with-checkpoint/1、State 和 API，真实 typed 构建可用。
- `examples/flows/automatic.config.yaml`：新增 automatic/1，省略人工验收 State，仍检查有效交付和停止安全，不另设 acceptanceMode。

扩展命令可提供由服务端代码构造的具名资格检查（command.Request.Authorize），在首次执行和重放前都运行；例如 checkpoint.confirm 复用任务管理角色并由 State 再核对 owner/候选。该回调不来自 HTTP，不是跳过授权开关；未提供时使用固定业务规则，未知操作仍拒绝。无需把新步骤动作登记进全局权限目录。

checkpoint 的写入入口是 POST `/v1/tasks/{id}/checkpoint`，输入 RoundID/BasisVersion/ExpectedRevision/SubmissionID/CommitID/Note；新候选需要新的确认。该示例未默认挂载，专有输入走专有 API，薄界面通用部分只展示其步骤/等待原因。

## 验证范围

真实 SQLite/HTTP 测试覆盖并发认领、进展时间记录与沉默不改变执行、结束后补交、业务凭据伪造证明拒绝、错实例/错 challenge、部分证明不放行、取消与旧写入、旧代次隔离、同轮重试、新轮/新依据、验收收尾等待、结果更换/撤回、终态保护及管理员恢复。C01—C11/C13 的本阶段基础场景均有对应回归；文件/链接的 C12 留 M07。

E01—E03 已通过本地真实配置/SQLite/API 扩展验证：新 State/新 API 装配、业务审核等待保留交付、前序新证据重查、旧确认拒绝、技术失败回滚及路由冲突拒绝。脚本静态图不作为这些运行验收的替代。

内置浏览器在独立临时数据库完成负责人创建/分配→业务 Agent 认领/提交/结束→负责人验收等待；独立测试 runtime 通过实际 HTTP 提交匹配证明后，界面补拉显示 completed。这是协议与本地 UI 验证，不是实际进程清理证明。真实 Harness/运行实例、受管子进程和外部工作覆盖，以及 MySQL/PostgreSQL 与生产负载仍未验收，归 M06/后续阶段。

任务分配与参与者控件使用 `GET /v1/tasks/{id}/candidates` 查询有效成员：处理人和参与者都可选择用户或 Agent。候选展示主体 ID、类型及 Agent 所属人；不表示在线或可立即执行。没有有效成员时提示管理员登记，目标/标准不完整时提示先补全。原分配命令仍在事务中校验目标成员和任务状态。

## 人工处理与转交

`Assignee` 是当前处理主体，替代 Agent 专属命名；`NextActor=assignee` 覆盖人和 Agent。`ViewerKind` 用于呈现人工操作或 Agent 调试区，`CanAssign` 单独表达分配资格，不借用负责人管理权限。创建者仍是默认负责人和验收人。

人工处理人复用 Open/SubmitResult：Open 建立服务端标记的 Manual 执行记录，保留轮次/依据/代次与并发隔离；没有受管 Agent 进程，因此不要求实例、心跳或运行时停止证明，Safe 只代表无该类受管运行等待。Submit 同事务结束人工记录并登记交付，人工验收仍按原流程。Agent 路径的停止与实例约束保持。

负责人/管理员可分配给有效用户或 Agent。仍参与任务的当前人工处理人可以转交，普通参与者和 Agent 不能转交。转交失效原执行写资格，旧结果请求不能写入，原处理者保留参与关系而不保留分配权；负责人/验收人不变。运行中的 Agent 必须先提供原有停止/隔离证据才能转交。已提交的结果仍按现有验收、退回流程处理。

复用原 assignmentRow 的 Subject 和 executionRow 的 JSON 数据；不新增表、插件或配置。已有 Agent 执行未带 Manual 字段时自然为 false，不变更历史记录或已应用迁移。API/Go 消费者同步改用 Assignee、AssignSubject 与 assignee 角色；不保留旧字段别名。相关验证见 `plugins/workflow/human_test.go`。
