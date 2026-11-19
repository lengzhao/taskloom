# Cap：按真实消费者定义能力

本清单记录 M04 的接口收敛与 M05 的真实消费者增量，取代旧 Repository/万能 Service 命名。新增能力与装配见下文及 [M05](m05-workflow.md)。不兼容旧接口，不提供 type alias 或弃用桥接。没有消费者的领域接口不预建，也不作为已发布 Go 契约。

## 原则

cap 表达跨模块消费者确实需要的能力、DTO 与共享约束，不复制数据库表结构。只有读写需求、替换边界或生命周期确实不同时才拆接口；同一消费者总是一起使用的内聚方法可以保留在一个接口。没有消费者不预造接口，也不因接口拆分新增插件。

SQL 可以实现多个能力，但消费者只通过 typed Deps 得到需要的部分。接口组合只表达真实内聚能力，不打包无关服务规避 Deps 计数。跨模块写入仍加入同一事务，命名用例依旧负责认证、成员关系、版本/依据及幂等。

## 当前清单

| 包 | 能力 | 实际消费者与决定 |
| --- | --- | --- |
| task | Reader.GetTask；Store 组合 Reader、ListTasks、CreateTask、UpdateBasis | access 仅拿 Reader；workspace 同时读写所以保留完整 Store，不额外预造 Writer |
| commit | Log.Append/Get/List | workspace 真实同时追加/回读/分页，保留一个内聚不可变 Log；无编辑、删除或任意 Execute |
| identity | Authenticator；CredentialLookup；Principals；Credentials；Passwords；BootstrapClaim | HTTP/命令用认证；认证插件仅拿 CredentialLookup；管理命令用主体与凭据管理；离线初始化使用 BootstrapClaim。目录读取留在 Principals，不再经认证插件转发；没有独立消费者就不预留 Directory |
| authorization | MemberReader/MemberWriter；Relations（成员+参与只读）；ParticipantWriter；Authorizer | access 只读关系和任务；workspace 只查成员并写参与关系；成员管理只拿 MemberWriter。固定业务规则保留，没有 Scope/Grant |
| flow | DefinitionRef、DefinitionStore、PositionStore、State、Controller、Role、Phase | 版本化定义和可变位置分开；flow controller 显式组合两者；workspace 调用 Controller，不能绕过它写位置。SavePosition 带预期版本；Controller.Step 按步骤 key 返回已到达条目的上下文，供步骤自有动作使用 |
| workspace | Commands、Queries、ChangeFeed | HTTP 各命名写入/快照查询/变化补拉分别依赖能力，可独立替换；仍由同一 workspace 实例提供，无新增容器或服务分发 |
| security | Administration（身份、密码、登录及 Agent 凭据管理） | HTTP 仅持有已认证管理员的日常管理命令，不包含 bootstrap/recover；凭据 DTO 位于 cap/security |
| record | Receipts；AuditWriter；EventWriter；EventLog | 命令回执 find/save 总在一起；审计只追加；命令和安全管理只写事件；workspace 同时写/扫描，才使用 EventLog（写入加内部扫描）；没有只读消费者，不预留 EventReader |
| command | Handler.Do，typed Request | security/workspace 注入共享命令生命周期接口，不依赖具体 Runner；runtime 的泛型 Run 只计算类型化输入摘要并调用 Handler，不派发业务动作 |
| unitofwork | Manager.Do | 保留同事务及 rollback-only 机制；只包含 ErrTransaction/ErrRollback，不承载通用业务错误 |
| fault | ErrConflict/ErrNotFound/ErrInput/ErrUnavailable | 跨领域共享错误分类，供仓储、命令与 HTTP 映射使用；身份/权限错误仍由对应包拥有 |
| httpapi | Module、Route、ModuleLifecycle | 保留协议适配与 HTTP owner 的生命周期边界；不把路由模块变成领域服务 |
| lifecycle | Resource、Component、App | 保留已有资源/运行/进程 owner；虽然 App/Component 方法同形，所有权语义不同，不为减少声明而混淆 |

删除 record.Revisions 及没有生产消费者的通用版本表/Advance 实现，任务 CAS 由 UpdateBasis 的预期版本约束负责。cap 中唯一的审计构造辅助逻辑移回 runtime/command；公共能力包不承担命令动作顺序。

## State 的明确结果

Describe 返回 Description{Key,Version,Role,Rules}，Role 为固定契约（见[状态模块](task-flow-plugins.md#角色契约与版本)）；acceptance.Actions 也暴露 Describe 以便命令定位其步骤；当前 flow 的行为摘要来自该稳定描述；改变行为需更换版本，未来新增可配置规则时需把有效规则纳入描述，不声称能自动检测任意代码变化。

CheckEnter/CheckComplete 返回 Check{Status,Reason}；Status 为 allow/wait/reject。阻塞必须有原因，非法状态拒绝提交。Position.Complete 表示当前步骤检查完成，Position.Status 单独区分 active/completed/cancelled；Waiting 保存阻塞原因。State 接收包含任务与位置的 Context。Enter 保持本地幂等写入/空动作，检查仍只读。

FlowRef 的归属移至 flow.DefinitionRef，task.Task 不依赖 flow 或持有流程位置。SQL 只存引用与事实，不替 State 作判断。

## 实际装配变化

M04 提交时为 12 个实例；M05 为 22 个，当前含项目目录、请求输入与 MCP 接入共 25 个，见[实际图](m05-dependencies.md)。以下保留 M04 基础模块说明，M05 的 task/flow 改为 states 列表：

- identity/local：store→identity.CredentialLookup（sql），不再提供 Directory 转发。
- access/default：store→authorization.Relations（sql）、tasks→task.Reader（sql）。
- task/flow：definitions→flow.DefinitionStore（sql）、positions→flow.PositionStore（sql）、state→ready。
- commands/runtime：6 字段，transactions、requests、identity、authorization、audit、events；同事务生命周期，不分发业务。
- commands/security：9 字段，分别为 commands、transactions、principals、credentials、passwords、bootstrap、members、audit、events。HTTP 使用 Administration，不获得离线初始化/恢复能力；members 使用 MemberStore 校验所属人和签发对象的有效性。
- workspace/tasks：12 字段，catalog、transactions、tasks、commits、flow、identity、authorization、commands、members、participants、events、progress。progress 为 execution.Progress，仅用于执行者本人留言时同事务记录最近进展。它是当前纵向切片编排边界，事实所有权未合并；成员只读与参与写入明确分开。members 使用 MemberDirectory（GetMember + ListActiveMembers + ListMembers），由现有 SQL 联查成员和主体供候选选择，不增加依赖字段或新表；workflow 等其他消费者仍只获得 MemberReader。
- api/workspace：6 字段，commands、queries、changes 分别指向同一个 workspace 实例，identity 与 security 提供认证和成员命令，catalog 指向空间/项目应用模块。

超过五字段的三处协调实现明确保留：command-runtime 需幂等/身份/权限/审计/事件的原子生命周期，security 需管理/离线能力，workspace 需同事务协调多种事实。不通过合并不相关接口、依赖包或隐藏查找缩小计数；后续执行/验收应使用独立命名模块，不继续扩张 workspace。

没有改两层合并、PluginKit 注册和接口注入机制。配置字段与 Deps JSON tag 同步，旧字段不作兼容映射。

## 核心约束与验证

保留租户隔离、成员即时撤销、重放前鉴权、原子业务/commit/审计/outbox/回执、执行写入隔离。不可变 commit、任务依据版本、轮次和执行记录仍各自独立；准备完成不代表交付或验收。

SQLite/HTTP 集成回归覆盖现有闭环；新增只读 provider 测试证明认证无需管理接口、授权无需任务或关系写接口；typed State 测试覆盖等待/完成、无效检查结果和固定定义冲突。导入边界继续禁止领域/运行机制依赖 GORM 或具体插件实现。schema 与接口均无兼容桥接，旧开发数据库可能因迁移校验和变化需要重建，程序不自动删除。

workspace 不再自行构造第二个 Runner：配置将 commands 指向现有 command-runtime，回执/审计能力只由该运行机制持有。API、workspace、security 均通过接口获得跨模块能力；只读字段不能调用提供者的写方法。

任务依据写入进一步收紧为 BasisChange：只允许目标/标准/标题及 ExpectedVersion，仓储保留负责人、租户和身份不变，并原子推进该任务版本/依据版本；不接受完整 Task 来覆盖无关字段。

## M05 的真实消费者增量

- execution.Reader/Store：执行事实与写入资格；delivery.Reader/Store：固定 commit 的候选事实；acceptance.Reader/Records/Actions：状态自有记录与验收动作。
- workflow.ExecutionCommands/ResultCommands/ControlCommands/Queries/FactsReader 分别服务协议、命令协调和只读聚合；不扩张 workspace。query/workflow 的 tasks 依赖为只读 task.Lister（GetTask + ListTasks），用于 Inbox，不获得写接口。question.Reader 供查询与 flow 完成门只读使用，question.Store 只注入 commands/question。execution.Progress（RecordProgress）由 commands/execution 提供，只注入 workspace/tasks 与 commands/question，用于同事务记录执行者最近进展；commands/question 另以只读 authorization.Relations 校验被提问人是否为有效成员且参与任务；没有租约或续期接口。api/mcp 以局部窄接口消费 workspace 的 Get/Comment 与 workflow 的 Open、Submit、Ask/Answer 及只读 Queries，不获得分配、验收或任务控制能力；回答仍只限被提问人。
- commit.Lookup、identity.PrincipalReader、flow.PositionReader 为新增只读消费者收窄权限；State 使用 Context，目录按固定 DefinitionRef 路由。
- checkpoint.Records/Actions 只在可选步骤示例挂载；httpapi.RequestSecurity 复用会话、Origin 与 CSRF 协议处理。
- 具体方法以 cap 源码为准；Deps、超过五字段的保留理由和授权回调的可信边界见 [M05](m05-workflow.md)。

人工处理复用 workflow 的分配/执行/交付命令；execution.Reader.Assignee 和 Store.AssignSubject 使用中性处理人命名。ResultAccess 只在 Reader 上增加 SaveExecution，供交付事务结束人工执行；其余只读消费者仍使用 Reader。authorization.Relations 增加 IsAssigned 用于人工处理人的有限转交资格；字段数量、配置及插件数量不变。公共 Snapshot 增加 CanAssign/ViewerKind，Assignee 和 NextActor=assignee 同时服务用户与 Agent。

用户/Agent 管理复用 workspace 的只读成员查询和 security 的已有命令：Viewer 返回当前身份及 CanAdmin，Members 仅返回当前租户管理目录（含停用成员）。API/workspace 当前为 6 个 Deps（含后续新增的 catalog），Security 改为 Administration；workspace 的依赖字段数量不变；安全模块增加 Passwords 依赖，用于密码存储与单租户登录范围发现。原 domain/security 的凭据 DTO 移至 cap/security，所有 CLI/API/测试消费者同步更新，无别名兼容层。

## 工作空间与项目

cap/catalog.Store 由 SQL 实现，catalog/projects 消费；ProjectReader 供 workspace/tasks 校验任务归属，应用 Service 供 api/workspace 调用。catalog 为独立职责模块，5 个依赖。workspace/tasks 为 12 个依赖（含 progress），新增只读 catalog 仅用于任务创建和移动的同事务校验；项目 CRUD 不放在该模块。api/workspace 为 6 个依赖，新增 catalog 是命名 API 的协议适配，不承载业务校验；保留这一共享浏览器入口与其现有 Cookie/CSRF 生命周期，避免复制认证处理。task.Store 新增显式 SetProject，不增加通用字段更新能力。
