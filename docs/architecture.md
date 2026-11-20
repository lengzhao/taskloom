# Taskloom 架构

Taskloom 是人与 Agent 协作的任务系统，采用模块化单体：公共能力接口放 `cap/*`，实现放 `plugins/*`，经 typed Deps 与两层配置组合，由 PluginKit 注册和构建；所有写入共享一个 SQL 事务边界。本文描述当前实现；业务不变量见[核心契约](core-contracts.md)，流转见[任务流程](task-flow.md)，接口见 [API 与权限](api.md)。实际装配以 [base.yaml](../base.yaml)、接口以 `cap/*` 代码为准。

## 只存能核实的事实

| 事实 | 保存什么 | 不保存什么 |
| --- | --- | --- |
| Task | 标题、目标、标准、负责人、项目归属及唯一版本 BasisVersion | 聊天上下文、模型运行 |
| Commit | 不可变正文、作者及代理归属、类型、引用、文件 ID 与外部链接、对应依据版本、提交序号 | 可变状态、通用动作、文件字节、链接页面快照 |
| File | 任务上的不可变文件版本：名称、媒体类型、大小、SHA-256、上传者；字节在 blob 存储 | 上传状态（登记即已保存）、文件夹或可变版本链 |
| 领域记录 | 分配、执行（写资格、凭据、进展时间）、交付登记、问题、成员、参与关系、当前父子关系（是否必需） | 正文副本、由自然语言猜测的决定 |
| 流程位置 | 阶段、轮次、创建时固定的验收方式 | 当前步骤与等待原因（每次推导） |

正文只存于 commit，结构化记录只引用 CommitID；验收结论本身就是一条 commit。没有 Conversation/Message 层，也不从日志重放构建数据库。Taskloom 观察不到外部进程，因此不记录"在线""已停止"等无法核实的状态。

## 模块装配

| 节点（use） | 职责 | Deps |
| --- | --- | --- |
| server、http（transport/http） | 资源/组件生命周期；路由挂载与 API 模块启停 | 2、1 |
| sql（storage/sql） | GORM 连接、迁移与全部仓储，不含业务判断 | 0 |
| blobs（storage/fs） | 文件字节的本地目录存储（cap/blob）；S3 适配实现同一接口后替换此节点 | 0 |
| identity（identity/local）、access（access/default） | 凭据认证；按成员/负责人/参与关系授权 | 1、2 |
| command-runtime（commands/runtime） | 命令事务、幂等回执、审计 | 5 |
| flow（task/flow） | 阶段/轮次唯一写入者，步骤由事实推导（含直接必需子任务） | 6 |
| workspace（workspace/tasks） | 任务创建（含原子建为子任务）/依据修改/评论/参与者/项目归属/变化补拉 | 13 |
| execution-commands、result-commands、question-commands、flow-commands | 分配与认领；提交与验收；提问与回答；取消与开新轮 | 8、7、8、5 |
| plan-commands（commands/plan） | 子任务放置：挂接、切换必需、重挂（不解除），同事务递增父依据；同一规则以 Placer 供任务创建在其事务内调用 | 7 |
| workflow-query（query/workflow） | 只读快照（含可读父/子任务）、可用操作原因、收件箱 | 11 |
| security（commands/security）、catalog（catalog/projects） | 身份/成员/凭据/密码管理；空间与项目 | 9、6 |
| files（commands/file） | 文件上传、列出与读取，规则见[文件与链接](core-contracts.md#文件与链接) | 6 |
| workspace-api、workflow-api、plan-api、file-api、mcp-api、audit-api、health | HTTP 与 MCP 协议适配、管理员审计查询、存活检查；Origin/CSRF 由 http 对所有路由统一检查 | 6、5、1、1、8、4、0 |

超过 5 个 Deps 的模块都是同一命令事务内的协调边界：每个字段是一个独立能力接口（命令生命周期、各领域事实、事件），不是服务定位器；拆开只会把同一事务协调挪到别处。workspace 的 13 项是工作台纵向切片的历史边界（第 13 项 Placer 只为让创建与放置成为一个原子命令，放置规则仍只在 plan 模块），新增执行/验收/拆分能力都放入独立命令模块，不再向它堆积。flow 增加的关系读取是同一完成规则的事实输入，提交检查和父任务事件也复用它，命令模块因此无需各自读取关系。files 的 6 项是上传命令（命令生命周期、文件登记、字节存储）与读取（事务、认证、授权）共用的同一授权边界。评论与结果引用文件时不新增依赖：文件属于本任务这一引用完整性由 commit 仓储在追加时检查，与 RefID 相同。

命令模块不依赖查询模块：各模块持有自己声明的 Deps，共享步骤是只接收窄接口的包级函数，不存在跨模块共用的依赖并集。

插件之间只经 Deps 注入接口，不 import 其他插件实现；只读消费者不获得写接口；无消费者不预建能力。

## 命令生命周期

每个写命令由 `runtime/command` 包装，在一个 SQL 事务内依次：

1. 更新协调行取得全局写锁（全库写入串行化，吞吐上限待 M11 评估）。
2. 认证凭据，检查当前成员/任务关系授权。
3. 查幂等回执：命名空间为租户 + 实际主体 + 操作 + 键，载荷为强类型输入的摘要；同键同载荷返回原结果，不同载荷冲突；重放前仍重新鉴权。回执只存命令固定的引用（任务与流程命令为各自的 `Ref`，项目为 ID，空间无需引用），不存快照、任务详情这类可推导事实；API 在命令后按调用者重读当前事实。
4. 执行领域回调：检查客户端固定的对象/版本，写领域事实与 commit，写一条事件（`task.changed` 或 `access.changed`）。
5. 写成功审计与回执，提交。

任一步失败整体回滚，嵌套失败标记 rollback-only；认证成功后的拒绝在回滚后另开短事务写审计。在全局写锁之下，仓储写入是普通更新，不做仓储级比较交换；若将来改为按任务加锁，再按需引入条件更新。事务内不调用 Agent、S3 或远端身份。

事件序号在持锁事务内分配，因此即为提交顺序；变化补拉按序号读取并逐条重新鉴权，详见 [API](api.md#变化补拉)。

## 存储与迁移

GORM v1.31.1，SQLite/MySQL/PostgreSQL 驱动 v1.6.0（SQLite 驱动需 CGO）。默认 `driver: sqlite`、`dsn: taskloom.db`，生产在 config.yaml 覆盖 driver 与原生 DSN；SQLite 使用 immediate 事务、外键检查和 5 秒 busy timeout。MySQL/PostgreSQL 已编入但未连接真实服务验证。

迁移按模块 ID 顺序登记校验和；已应用的未知迁移或校验和不一致时拒绝打开数据库，不做自动破坏性升级。当前处于开发阶段，表结构变更直接修改迁移定义，本地旧库需删除后重新初始化。

## 身份、凭据与审计

- 人和 Agent 是稳定主体；Agent 的所属人只用于归属，不传递权限。
- 凭据是 256 位随机秘密，库中只存 SHA-256 摘要、有效期与撤销状态，每次请求查询当前状态。认证结果带上所用凭据 ID：一张 Agent 凭据即一个运行实例，执行绑定认领时的凭据。
- 人用用户名和密码登录（PBKDF2-SHA256，600000 次，随机盐），登录得到一小时会话凭据，HttpOnly、SameSite Cookie 直接持有它，登出即吊销；浏览器不能用其他凭据登录，管理员初始密码由 `taskloom-admin` 设置；按来源 IP 每分钟限 20 次尝试。修改或重置密码撤销该用户全部凭据与会话。
- 首个管理员初始化、离线恢复和 Agent 登记只能用本地 `taskloom-admin` CLI，记录操作者与原因；没有匿名初始化 API。人只有密码、Agent 只有签发凭据，均由命令层保证：初始化与恢复只设置密码，不签发凭据。
- 每个命令（成功与被拒绝）写审计；只有有效人类管理员可查询（`GET /v1/audit`）。

## 生命周期 owner

构造函数只做轻量初始化。server 先按顺序打开 resources（sql、blobs），再运行 components（http）；http 挂载各 API 模块的路由，模块本身无启停。退出时先停组件再逆序关闭资源。没有后台扫描器或无 owner 的 goroutine。
