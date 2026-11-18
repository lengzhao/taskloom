# M03：GORM 存储与安全命令基础

本页描述已实现的基础能力。任务、会话、流程、业务 HTTP/WS 和真实 Harness 接入仍未实现。根配置现在打开 SQLite SQL 资源并提供 health；`examples/minimal` 仍是纯 health 三实例图。health 是进程存活，不表示任务系统已就绪。

## 接口、实现和装配

必要性：为 M04 起的领域命令提供跨仓储原子提交、可信身份及可撤销授权。公共契约只包含消费者所需能力，不包含 GORM 类型、SQL 方言或通用业务分发方法；当前没有已发布业务消费者，未来新增仓储沿同一事务机制实现。

| 位置 | 能力与边界 |
| --- | --- |
| cap/unitofwork | Manager.Do；嵌套加入同一事务，失败必须整体回滚 |
| cap/identity | Authenticator、Directory、身份/摘要凭据仓储；人和 Agent 分开，实例绑定属于凭据 |
| cap/authorization | 固定租户成员角色、任务参与关系与业务 Authorizer |
| cap/record | Receipts、AuditWriter、EventWriter/EventLog 独立能力；EventLog.ReadEvents 是内部 outbox 读取，不是已授权用户 feed |
| plugins/storage/sql | GORM、连接与迁移 owner、各能力仓储；无 Deps，不包含授权或任务流转判断 |
| plugins/identity/local | Deps.store: identity.CredentialLookup；仅摘要查询、有效期和撤销认证；目录直接由 SQL 的 identity.Principals 提供 |
| plugins/access/default | Deps.store: authorization.Relations、tasks:task.Reader；按成员/负责人/参与关系判断，无 Agent 所属人权限继承 |
| runtime/command | 六个独立 Deps：Transactions、Requests、Identity、Authorization、Audit、Events；带类型输入的代码级事务包装，不按 action 查找/调用业务服务 |
| domain/security | 创建主体、签发/撤销凭据、授予/撤销权限；bootstrap/recover 仅给离线操作入口 |
| cmd/taskloom-admin | 显式本地初始化/恢复；通过 PluginKit 结果中的能力接口获取 SQL，不依赖存储实现类型 |

`runtime/command` 的六个 Deps 分别属于原子性、幂等、身份、权限及两种持久记录；保留独立接口，不合并为万能 Service。`domain/security` 八个 Deps 是 Commands 与 bootstrap/recover 所需的 Transactions/Principals/Credentials/Bootstrap/Members/Audit/Events；离线操作不伪造在线认证。普通命令统一经过 Commands，内部仓储写不另开事务。这两处超过五字段是有意保留的协调边界，后续 HTTP 仅包装命名方法。

identity/access 已注册并有真实配置注入测试；尚无业务 HTTP 消费者，因此不在根图增加无消费者实例。根图只挂 SQL resource。完整 51 节点清单是跨阶段目标图，与本页的实际接口/Deps 区分；尤其未来 access 的树范围、确认仓储尚未实现。

## 事务、幂等与事件顺序

一个 Store 连接池当前上限为 1；不同 Store/进程通过数据库 coordination 行串行化写事务，不依赖进程锁。SQLite 使用 immediate 事务、外键检查和 5 秒 busy timeout，拒绝 DSN 覆盖这些安全参数。数据库冲突/超时返回失败，框架不自动重放业务回调；客户端可用相同幂等键重试。

Manager.Do 把事务绑定在 context，只允许同 Store 的同步调用；结束后不可复用，跨 Store 加入拒绝。嵌套错误/被外层捕获的 panic、仓储写失败均标记 rollback-only。此语义刻意不采用 GORM 的独立嵌套 savepoint 提交行为。领域消费者只拿接口和 context，不能拿根 GORM 连接绕过事务。

写事务顺序为：协调行锁→当前认证/授权→幂等回执查询→领域 callback（校验资源所属租户/依据版本）→审计/outbox/回执→提交。相同 tenant/subject/operation/key 和规范 JSON 摘要返回原回执；同键不同内容冲突。重放仍检查当前凭据及动作/范围权限。M04 的动态成员/资源读取资格须在命令重放前接入，不能把当前的静态范围检查当成未来所有领域授权。

版本条件更新由任务自身仓储负责，不提供通用 Revisions 接口或独立版本表。事件序号在持有协调行写锁的事务内分配，提交后后续事务才能继续分配，因此不会把自增分配顺序误当提交顺序。序号可有间隙；按 `sequence > cursor` 读取，租户过滤在仓储中执行。此全局写串行化是 M03 的吞吐限制，后续按实测演进，不能直接移除而破坏提交/游标语义。

失败命令不保存回执，业务写/审计/outbox 一起回滚；认证成功后的拒绝审计由外层显式开启独立短事务追加，失败返回错误。未认证请求不冒用请求体身份写审计。请求体、明文凭据、数据库原始错误不进入日志/审计；成功审计保留实际主体及 Agent 归属/实例依据。

## 身份与离线管理员

凭据为 256 位随机秘密的十六进制表示，库中只存 SHA-256 摘要、有效期、撤销及主体绑定。每请求查询当前状态，不做授权缓存。默认管理员凭据期限 24 小时，上限 90 天；正式写入的认证和撤销通过同一写协调边界串行化。初始化直接建立有效租户管理员；普通成员、负责人、参与者按固定业务规则访问，未知操作拒绝。

人、Agent 有稳定主体 ID；Agent owner 只用于归属。每张 Agent 凭据绑定 instance/incarnation 和 business/runtime 用途，更换实例不改主体目录。runtime 凭据只允许未来的 execution.stopEvidence 动作且仍需显式授权，business 凭据不能提交该证明；这不表示执行/停止业务已实现。新增 incarnation 不自动撤销旧凭据，调用者必须显式撤销旧凭据，执行代次保护仍归 M05。

管理员初始化是数据库级一次性原子操作；恢复必须显式调用本地 CLI，记录操作者和原因，撤销指定人的全部旧凭据并签发新凭据。没有匿名初始化 API。操作者的可信边界是部署机/数据库运维权限，`-operator` 是审计声明，不冒充操作系统认证。

```sh
go build -o /tmp/taskloom-admin ./cmd/taskloom-admin
/tmp/taskloom-admin -mode bootstrap -tenant example -subject admin \
  -operator local-operator -reason initial-setup \
  -credential-file /private/tmp/taskloom-admin-credential.json
# 遗失凭据时显式使用 -mode recover 和新的 -credential-file 路径。
```

输出文件必须不存在，权限 0600；秘密不打印到 stdout，不允许覆盖旧文件。数据库与文件系统不共享事务：提交后若保存文件失败，CLI 明确报错，使用 recover 和新路径恢复。正常签发的命令回执只保存凭据 ID；幂等重试不会再次返回秘密，需要新签发/撤销流程。

## 迁移与数据库支持

GORM v1.31.1；SQLite/MySQL/PostgreSQL 驱动 v1.6.0。SQLite 驱动使用 CGO，本机/CI 需要 C 编译器。默认 `driver: sqlite`、`dsn: taskloom.db`；生产在 config.yaml 设置 `driver: mysql` 或 `postgres` 及相应原生 DSN。MySQL DSN 应配置 `parseTime=true`，凭据配置文件限制访问，不提交到版本库；加载器仍不支持隐式环境插值。

迁移有 identity/001、command/001 的模块 ID、顺序、定义校验和及完成记录；当前只包含身份/成员/参与关系/命令基础表。已应用未知版本或校验和不匹配拒绝打开资源；失败的幂等建表可重试。SQLite 迁移处于 immediate 事务；MySQL/PostgreSQL 在专用连接上持有 session 迁移锁，避免 MySQL DDL 隐式提交释放事务锁。无自动破坏性升级，不假定所有数据库 DDL 可回滚。

当前迁移清单在 SQL 实现内显式维护，尚未建设外部 State 仓储/迁移装配；该扩展随 M05 的实际步骤实现，不宣称只新增一个 State 即自动建表。支持方言不是生产认证：本轮仅真实 SQLite 验证，MySQL/PostgreSQL 已编入驱动但未连接真实服务。生产启用前必须跑该方言的迁移、幂等、并发及失败恢复测试；修改 DSN 不自动迁移已有数据。

## 本轮验证范围

测试覆盖 SQLite 文件持久化、同库双连接版本竞争/事件顺序、跨仓储回滚、嵌套错误/panic、取消、跨事务句柄限制、外键失败、迁移校验和拒绝与重新打开；安全命令覆盖并发幂等、冲突、撤权重放、跨租户、未知动作、初始化一次性、离线恢复撤销、签发重试不泄密及 Agent 凭据用途隔离。PluginKit 测试验证真实 identity/access Deps 组合；CLI 验证受控入口及输出文件保护。

C13/C15—C18 在本阶段验证基础机制；真实任务回复/交付、历史动态读取权限及停止证据仍需 M04—M06 的用例验收。没有运行 MySQL/PostgreSQL、业务 HTTP/WS、S3、Harness 联调或 E01—E04 扩展验收。

权限模型已按用户要求简化，详见[成员与任务关系](permissions.md)；旧 Grant 数据格式不兼容，需开发环境重建数据库。本文已同步当前接口，不再提供动作目录/逐项授权。

本轮删除了无生产消费者的通用版本仓储，事务回滚/并发测试使用真实任务事实。共享错误移至 cap/fault，事务包只保留事务生命周期错误；详见[能力审查](cap-interfaces.md)。
