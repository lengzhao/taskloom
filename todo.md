# Taskloom 开发清单（M05）

更新：2026-11-18。用户明确插件是接口清晰、依赖经 Deps 并由配置组合的模块；M01 据此修订并统一所有流程步骤为 State。M03 已实现 GORM/SQLite 存储与身份/安全命令基础，任务业务闭环尚未实现。权威架构见[设计](docs/architecture.md)，行为见[核心契约](docs/core-contracts.md)，覆盖见[阶段表](docs/requirements-coverage.md)。

## 历史与当前边界

原 T01—T05 的不变量/案例保留，具体装配及阶段承诺由 M01 修订版替代；原 T06/T07 的 Go/PluginKit、HTTP health 和两层加载底座保留，不能视为业务通过。原 T08—T27 不再是执行顺序，改为下列纵向切片；D01/D02 仅为历史记录。原 T08/T09/T10 对应 M03；T11—T18/T25 前移并拆入 M04—M06；T19—T21 对应 M07/M08；T23/T24 对应 M09；T22 对应 M10；T26/T27 的必要验证随每阶段交付并在 M11 扩展。

- [x] M01 重新设计接口模块与配置组合单体。交付：architecture、核心契约、生命周期、API/交付、配置、需求覆盖、项目规则与本清单。完成依据：`d9886a0` 已提交工程底座和状态插件设计；本次 all/race、插件依赖及设计检查通过。仅代表设计和最小工程，不代表任务业务或 E01—E04 已实现。
- [x] M02 明确独立接入与剩余契约。依赖 M01。按用户决定，Taskloom 自行定义协议，后续由 Harness 插件适配；不以 NEX/Buddy 接口为前提。交付见[接入契约](docs/integration-contract.md)：本地可信身份/撤销、停止证据、State 幂等/认领重查、F02/F06/F07 及 SQL 选择。清理 runtime/config 的旧流程检查并补回归；真实接入验收仍归 M06。
- [x] M03 实现最小持久化与安全命令基础。依赖 M02。采用 GORM 多方言仓储，本地默认 SQLite；首批目标 SQLite/MySQL/PostgreSQL，部署配置 driver + dsn，业务不绑定具体数据库。落实版本迁移、同事务仓储及各方言并发保护；实现 identity/local、默认拒绝的动作/范围授权、受控管理员初始化、事务/幂等/版本/审计/outbox。验收：真实 SQLite 文件数据库原子回滚、并发幂等、版本冲突、撤销重放、跨租户拒绝、晚提交不漏事件，覆盖 C13/C15—C18。生产方言启用前运行同一事务/迁移契约测试，记录支持版本；SQLite 通过不替代生产并发验证。只建立后续用例需要的仓储，不生成完整 schema 和公共接口空壳。

## 第一阶段：单任务真实文本闭环

- [x] M04 创建/读取/commit 与薄界面纵向切片。依赖 M03。配置组合任务事实、命令与流程能力，CreateTask 同事务建立策略、FlowRef、首轮和默认首状态；不可变 commit、目标/标准版本、当前权限查询、授权变化 feed 和最小任务列表/详情界面。前端技术在本项确认。验收：信息不足可见、普通聊天不交付、快照/补拉无漏窗、重复与游标过期正确、界面能展示依据和阻塞。本项按用户最新要求不包含 Harness 适配。
- [x] M05 执行/交付/验收/取消纵向切片。依赖 M04。分领域命令模块经 Deps 组合事实与流程控制器，状态列表可配置，单任务本地事务原子更新；保活未知、执行代次、停止证明、文本 SubmitResult、人工验收状态自有记录、含/不含人工验收步骤的流程、Return/Reopen 和采用新要求。验收：C01—C11/C13 的实际事务与并发场景；旧回执仅关闭旧执行、结果修订不漂移、完成等待收尾、终态不被普通消息重开。增加 E01 本地事实审核步骤、E02 带人工/异步输入的状态和 E03 API 模块的真实装配验证：新增模块/配置但不改原消费者逻辑，涵盖业务审核未过保留交付、技术失败回滚、前序步骤新证据重查、旧回执拒绝、旧定义兼容及路由冲突。薄界面同步提供结果、验收及恢复操作，不让用户手工输入轮次标识。
- [ ] M06 第一阶段真实联调与可用交付。依赖 M05。真实 Harness 适配插件与薄界面完成任务，覆盖运行中新消息、输入问答、断线/升级、取消、替换及旧写入；Agent 自己负责 steer、上下文和避免自回复。第一阶段加入必要运维文档、迁移/备份恢复、健康与消费失败查询；验证重连、慢客户端、关闭清理、权限撤销和租户隔离。记录真实/模拟/跳过；未通过真实 Agent 联调不算阶段完成。

阶段出口：人创建任务→真实 Agent 经 Harness 适配认领→提交文本结果→验收完成，以及取消/恢复失败路径，均可从薄界面操作。不存在假成功材料或计划服务；业务图仅挂已实现能力。

## 第二阶段：文件与多人协作

- [ ] M07 文件和链接交付。依赖 M06。实现真实 S3 适配、上传状态、文件版本/保留引用、删除补偿；应用用例同事务固定回复及引用。验收：C12 文件/链接、上传中断、引用与删除竞争、历史材料鉴权；链接不承诺页面快照，SQL/S3 不共享事务。
- [ ] M08 任务树与依赖。依赖 M06。单父树、成组变更、依赖环并发保护、跨任务 outbox 影响及周期核对。关键命令同步校验权威依赖；异步只负责传播和展示。验收：前后影响并集、逐目标失败恢复、父独立结果、取消/重开不级联越权；界面展示子任务和阻塞。
- [ ] M09 协作治理与提醒。依赖 M07、M08。项目/成员、规划/协调授权、精确一次确认、树移动范围、搜索与收件箱。验收：C14 成组授权一次消费、回答不授予权限、提醒不自消费、撤销影响查询/历史/附件/feed。验证 E04 新增通知渠道不修改原消费者逻辑，部分失败独立重试；未实现复杂策略明确拒绝。

## 第三阶段：历史与运维扩展

- [ ] M10 历史树快照、差异及选择性恢复。依赖 M09。验收：A08 恢复生成新变更，相关依据失效，不恢复运行/权限/副作用；并发冲突明确。仅在明确场景证明收益后另提复杂分支流程或多级会签方案，不以预留抽象代替需求。
- [ ] M11 容量与升级验收。依赖 M10。确认在线数、消息量、事件延迟、保留期及恢复目标后做真实压测；验证流程定义/规则版本兼容或拒绝、迁移、备份恢复、消费者重放和部署升级。每阶段已有必要检查，不把基本安全/恢复验证拖到此项。

## 每项共同要求

业务实现前按消费者定义小接口；修改后执行匹配的测试及现有 all/race 检查。运行态覆盖事务、并发、授权、取消和恢复。完成项记录文件、实际命令/结果、真实依赖和限制。文档与 root base 只承诺当前可运行能力，未来方案不注册占位 kind。

当前下一步为 M06 真实 Agent/运行时接入验收；M05 本地闭环见 docs/m05-workflow.md，当前未开发 Harness 适配。

M02 按简化方案明确 State/Enter 幂等证据、CheckAction 认领前重查、F02 材料引用保留、F06 操作/权限、F07 步骤自有仓储/迁移；不再建设 Eligibility/CompletionCondition/ReviewEvaluator 引擎。M05/M06 验收覆盖 F03 新审核事实触发与 F09 HTTP 安全基础；M09 覆盖 F05 通知规则和 F10 渠道生命周期。F04 多方会签可作为后续步骤实现；F08 复杂分支控制器仍后置。清单展开和无环静态验证不能勾选 E01—E04 的真实扩展验收。

M04/M05 补充结构验收：flow 不依赖验收服务；delivery 不依赖 review/checks；人工验收 State 不依赖 flow/命令；记录结论的命令单向依赖 State 和 flow。不同步骤共享当前轮证据但各自拥有记录，重提结果后所有已到达步骤失效/重查，不能只替换最终评审。


## 提交与验证记录（2026-11-18）

- 已先提交 `d9886a0`：最小 Go/PluginKit/HTTP 工程、当前可运行 health 配置、状态插件方案、接口/Deps 清单与依赖图；未推送。
- 随后更新本清单。该次提交时 M01 完成，M02—M11 尚未完成；没有新增任务业务实现。
- 提交前 `GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh all`、`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh race`、`python3 scripts/check-plugin-inventory.py`、`git diff --check` 通过。all 有 Go 模块 stat cache 写入权限提示，但退出 0，构建及检查成功。
- 上述为 M01 提交记录。M02 已按后续用户决定改为独立协议设计，不再核验 NEX/Buddy 兼容性；真实适配验收留在 M06。
- SQL、身份适配、业务 API/WS、S3、真实 Buddy 联调及 E01—E04 扩展行为尚未实现/验证。

## M02 验证记录（2026-11-18，尚未提交）

- 新增 `docs/integration-contract.md`，同步身份插件 Deps、依赖图及相关设计文档；停止证据采用独立契约，Harness 后续适配。
- 旧配置检查回归先复现失败，移除业务专用校验后通过；可达未实现 kind 仍拒绝。
- `GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh all` 与 `race` 通过。首次受沙箱本机端口限制失败，获准在沙箱外重跑通过。
- 插件静态检查通过：51 节点、206 字段目标边，无环、无缺失引用、根可达；设计/链接及 `git diff --check` 通过。
- 仅契约与通用加载器调整；GORM/SQL 存储、身份/业务 API、停止证明及真实 Harness 接入未实现或联调。下一步 M03。

- 存储方案修订：依用户要求取消 PostgreSQL 绑定，采用 GORM，默认 SQLite 本地验证，生产通过 driver/DSN 配置切换；本次仅同步设计和清单，尚未引入 GORM 或实现 SQL 插件。


## M03 实现与验证（2026-11-18，尚未提交）

- 已实现：`storage/sql` GORM 三种驱动选择及真实 SQLite；共享事务、rollback-only、模块版本迁移、摘要凭据、显式范围授权、条件版本更新、幂等回执、审计及有提交顺序保证的 outbox。代码与限制见[M03 实现说明](docs/m03-storage.md)。
- 已实现 `identity/local`、`access/default` 的真实 Deps 注入；数据库驱动和 GORM 仅留在 SQL 实现内，有导入边界测试。命名安全命令在 domain/security，共享机制在 runtime/command。
- 已实现离线 bootstrap/recover CLI：一次性初始化、显式操作人与原因、恢复撤销旧凭据；秘密仅写新建 0600 文件，回执不存明文。没有匿名初始化或业务 HTTP。
- 实际通过：`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh all`、`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh race`、`python3 scripts/check-plugin-inventory.py`、`git diff --check`。all 包括单测、vet、两个 CLI 构建、根四实例/最小三实例 typed 构建及设计检查；HTTP 测试在获准监听本机端口的环境执行。
- SQLite 验证覆盖跨仓储回滚、嵌套错误及捕获 panic、取消/关闭期限、版本竞争、并发幂等、撤权重放、跨租户、迁移拒绝/恢复、外键失败、凭据摘要/过期/用途、输出文件保护、outbox 失败整体回滚及游标顺序。C13/C15—C18 仅基础机制通过，真实任务领域场景随 M04—M06 完成。
- 限制：写事务暂由数据库协调行串行化，连接池上限 1；SQLite 需要 CGO。MySQL/PostgreSQL 已编入但未真实服务验证，不能声称生产适配验收通过。外部 State 的仓储/迁移扩展归 M05；业务 API/WS、S3、真实 Harness 和 E01—E04 均未实现/联调。

## M04 推进记录（未完成）

- M02/M03 已提交为 `6a7d11f`，M04 在 `codex/m04-task-commits` 开发；本轮按用户要求不涉及 Harness。
- 已新增任务事实及 commit 仓储接口、GORM task/001 迁移和 SQLite 仓储实现；验证不可变 commit、旧版本冲突及租户隔离。
- 任务命令、配置装配的流程初始化、业务 API、授权变化流和薄界面尚未完成，M04 保持未勾选；这些仓储接口仍是开发中的内部契约。

模型修订：commit 时间线/commit由不可变 commit 替代，见[commit 契约](docs/commits.md)。当前仓储已调整，M04 整体仍未完成，Harness 继续排除。

## 整体设计修订：Commit 基线

已统一架构、核心契约、产品需求、交付/API、插件接口及依赖图，详见[整体设计](docs/architecture.md)。删除独立会话和消息修订模型：正文固定 CommitID，更正追加引用；普通 commit 不改变依据、状态或当前交付。decision/facts 独立拥有结构化问题与阻塞事实。

目标图现为 52 个跨阶段候选、206 条字段目标边，第一阶段 37 个候选；无循环、悬空引用和根不可达节点。数量不是实际已装配实例数。M04 保持未完成：后续需分离 Task/Flow position 仓储职责、实现命名命令及当前权限查询、授权游标/快照与薄界面；准备位置必须由 flow 初始化，禁止 API/任务事实直接写状态。Harness 继续后置。

M04 验收：创建原子建立任务/策略/FlowRef/首轮/准备位置；信息不齐可见；评论追加及更正均不可变、只作者可更正引用且不改旧证据；正式依据变更命名授权和版本冲突；任务/commit/历史/变化流逐次鉴权；快照补拉无漏窗、游标超期重同步；界面展示依据、等待及当前权限可用操作。结果与验收仍由 M05 命名命令接入。


## M04 可用闭环（2026-11-18，未提交）

- 以本条替代此前 M04“未完成”的推进快照：已实现任务创建/读取、正式依据更新、不可变 commit 时间线及更正引用、独立准备流程初始化、授权 HTTP API、服务端会话与内嵌薄界面。Task 不再包含流程位置字段，flow 是位置唯一写入者。
- [实际接口、配置、启动与限制](docs/m04-workspace.md)。根默认图为 12 个实际实例；目标 52 节点图仍是跨阶段设计。preparation/1 是本阶段真实定义，未配置执行/验收假插件。创建默认负责人及准备策略；无人工验收步骤时不重复配置 reviewer。
- 创建、依据变更和评论有事务内 task.changed 事件；HTTP opaque cursor 绑定租户/主体、1 小时期限，先游标后快照再补拉。UI 每 4 秒读取变化，WebSocket 按 M06 后续接入。
- SQLite/HTTP 验证并发幂等、初始化、目标/标准等待、评论/更正不改依据、跨任务引用、分页、冲突、撤权读取/重放、游标主体绑定、Origin/CSRF 和会话失效；内置浏览器实际完成登录→创建→评论→更正并观察旧记录保留。
- M04 以此次明确的本地任务/commit 闭环验收；结构化阻塞问答随 M05 的 State/decision 用例接入，不把普通评论解释为答案。生产数据库、WS/负载、执行/结果/验收及 E01—E04 仍未验收。Harness 本轮未涉及。

- 最终验证通过：`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh all`、`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh race`、`python3 scripts/check-plugin-inventory.py`、`git diff --check`。all 包含 SQLite/HTTP 集成测试、vet、构建及根 12 实例/最小 3 实例真实装配。浏览器临时测试凭据已撤销，预览服务关闭；未提交或推送本轮改动。


## 权限简化修订（2026-11-18）

- 删除通用 Scope/Grant 接口、动作目录配置、Grant 仓储及逐动作授权 API/界面；初始化管理员直接可用。
- 改为有效租户成员（admin/member）、任务负责人及参与关系。成员可创建任务；管理员/负责人管理约定和参与者；参与者读取/评论。Agent 不继承所属人权限，执行写仍待 M05 按分配/代次实现。
- 命名成员管理及参与关系 API；角色/参与关系由领域命令校验，未知业务操作默认拒绝。撤销与正式写入共享数据库串行边界，读取/变化补拉/幂等重放逐次检查当前关系。
- 用户明确无需兼容：直接移除旧 Grant schema，无迁移映射；旧格式本地数据库需重建，不自动删除用户数据库。历史 M03/M04 记录中的逐动作授权说明由本条替代。
- 新回归覆盖无授权配置的创建、无关成员拒绝、参与者评论/编辑隔离、移出后幂等重放拒绝、成员停用、跨租户及默认拒绝。完整规则见[权限说明](docs/permissions.md)。Harness 未涉及。

- 本次实际验证：`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh all`、`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh race`、`python3 scripts/check-plugin-inventory.py`、`ruby scripts/check-design.rb`、界面脚本 `node --check` 与 `git diff --check` 通过。目标依赖图 52 节点/203 条字段目标边，无环且全部根可达；静态图不是运行验收。本轮未提交或推送，未接生产数据库或 Harness。


## Cap 消费者边界收敛（2026-11-18，未提交）

- 公共接口以[cap 清单](docs/cap-interfaces.md)为实现基线：消除旧 Repository/万能 Service；task 只读与完整 Store 区分，commit 保留内聚 Log，不机械逐方法拆分。
- 认证仅查询凭据；主体/凭据管理及离线 bootstrap 能力分开。权限仅读关系及任务，成员写与参与写分开。flow 定义/位置能力分开；FlowRef 归 flow.DefinitionRef，State 返回 Description/Check，Position.Complete 仅表示当前准备完成。
- HTTP 注入 Commands/Queries/ChangeFeed；workspace 共享现有 command.Handler，不再直接注入回执/审计或创建重复 Runner。root 仍 12 实例、两层配置；command-runtime 6 个 Deps、security 8 个、workspace 10 个，保留理由已记录，不隐藏依赖规避计数。
- 删除无生产消费者的通用 Revisions/Advance/版本表；回滚/并发测试改为真实任务事实。通用错误移至 cap/fault，cap 内命令辅助逻辑移回 runtime/command；所有参数名称表达租户、任务、主体、游标与幂等键。
- 不兼容旧接口/配置，无别名或桥接；旧开发数据库可能因移除旧 schema 而需重建，程序不自动删除。未涉及 Harness 或 M05 业务扩展。
- 新增最小只读 provider 测试、明确状态结果及定义冲突测试；现有 SQLite/HTTP、撤销、幂等重放、租户隔离和导入边界继续回归。完整目标图仍是阶段规划，不能当作已实现接口。

- 本轮最终验证通过：`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh all`、`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh race`、`python3 scripts/check-plugin-inventory.py`、`ruby scripts/check-design.rb`、`git diff --check`。新增测试验证最小只读提供者、明确状态结果、定义固定及 BasisChange 保留负责人/租户/ID和拒绝旧版本；既有 SQLite/HTTP、撤权/重放和事务回滚继续通过。目标图 52 节点/193 条字段目标边，无环且根可达；运行图仍 12 实例。本轮未提交、未推送，未验证生产数据库或 Harness。


## M04 提交与 M05 本地闭环（2026-11-18）

- 已按要求先提交当前 M04、权限与 cap 简化：`f71bde2`；未推送。
- M05 交付见[实现与验证范围](docs/m05-workflow.md)：执行分配/认领/保活/结束/证明，文本结果与不可变 commit，State 自有人工验收，取消/退回/重开和管理员隔离恢复。新任务 standard/1，旧 preparation/1 保留。
- 执行、结果、流程控制和只读查询独立装配；flow 的有序 states 与定义目录负责位置写入；人工验收 State 不反向依赖 flow/命令。无人工验收模式仅省略状态，不重复设置模式。
- 当前轮所有已到达步骤先同步再重查；新候选重建当前验收/确认，旧证据拒绝。业务审核等待保留交付，技术失败回滚正文/交付/步骤/事件/回执。相同结果不同请求键不重复创建候选。
- 已增加真实 SQLite/API 的 E01 本地规则、E02 自有记录/人工输入、E03 独立 API 与配置示例验收；旧定义摘要、无环与路由冲突也验证。外部检测或执行进程没有被模拟成“已真实验证”。
- 界面提供分配、认领、结果、验收、取消与安全恢复，不手填轮次/执行代次；本地 CLI 可显式登记绑定实例的 Agent，分别签发 business/runtime 私有凭据。停止协议只信绑定 challenge 的 runtime 凭据或明确受权的隔离记录。
- 首版固定负责人验收策略 PolicyVersion=1，不开放策略编辑/职责分离配置；运行中更新依据拒绝，安全退回新轮后可采用新要求。结构化阻塞问答作为 checkpoint 自有输入示例验证，尚无通用问答工作流 UI，不让评论自动解除阻塞。
- 浏览器实际完成负责人/Agent 身份切换及验收收尾等待，测试 runtime 经本机 HTTP 提交证明后自动补拉显示完成；没有启动真实 Harness 或工具进程。M06 仍未完成。

- M05 最终检查通过：`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh all`、`GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh race`、`ruby scripts/check-design.rb`、`python3 scripts/check-plugin-inventory.py`、界面 `node --check` 和 `git diff --check`。实际默认图 22 实例/93 条字段目标边无环且根可达；52 节点规划图继续单独校验。M05 实现、测试、配置与文档纳入本次提交，未推送；临时本地预览服务已停止。
