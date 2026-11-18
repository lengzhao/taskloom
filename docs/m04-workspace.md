# M04：任务与 Commit 工作台

本页记录 M04 提交时的范围；当前根配置已进入 [M05](m05-workflow.md)，执行/结果/验收已实现，Harness 仍未接入。

M04 提供本地 SQLite 可用闭环：受控初始化管理员→登录→显式授权→创建任务→读取目标/标准/准备位置→追加评论/更正→修改依据→刷新与变化补拉。没有执行/结果/验收或 Harness 适配；这些能力按 M05/M06 交付。

## 启动

```sh
go run ./cmd/taskloom-admin -mode bootstrap -tenant example -subject admin \
  -operator local-operator -reason initial-setup \
  -credential-file /private/tmp/taskloom-admin-credential.json
go run ./cmd/taskloom
```

打开 `http://127.0.0.1:8080/v1/app`，使用私有凭据文件中的 Secret 登录。初始化者是有效租户管理员，可直接创建和管理任务。普通有效人类成员也可创建任务，自动成为负责人；参与者可读取/评论，负责人或管理员可管理参与关系。没有逐动作授权步骤或界面。成员角色及停用使用命名成员 API，规则见[成员与任务权限](permissions.md)。

当前本地默认监听 127.0.0.1:8080，SQLite 文件 taskloom.db。生产需配置 driver/DSN、HTTPS，以及 workspace-api.config.origin；设置 localHTTP=false 使 Cookie 使用 Secure。localHTTP=true 仅接受固定 loopback Origin，不能用于公网。所有界面资源内嵌，无外部 CDN 或前端构建链。

## 接口与职责

| 能力 | 实际接口/Deps |
| --- | --- |
| cap/task | Reader.GetTask；Store 组合读取/列表/创建/依据写入；只任务事实 |
| cap/commit | Log.Append/Get/List；精确 ID 和提交序分页，不可编辑 |
| cap/flow | DefinitionStore / PositionStore 分别固定定义/读写位置；Controller.Initialize/Refresh/Read；State 四方法 |
| state/ready | 无 Deps；目标/标准不齐返回明确等待 |
| task/flow | definitions:flow.DefinitionStore→sql、positions:flow.PositionStore→sql、state:flow.State→ready；位置唯一写入者 |
| workspace/tasks | transactions、tasks、commits、flow、identity、authorization、commands、members、participants、events，分别指向能力接口 |
| api/workspace | commands:workspace.Commands、queries:workspace.Queries、changes:workspace.ChangeFeed、identity:identity.Authenticator、security:security.MemberCommands；协议/会话与内嵌薄界面 |

workspace/tasks 的 10 个 Deps（共享命令生命周期，成员只读与参与写入分开） 明确展开：它是当前纵向切片的命令/查询编排边界，复用 runtime/command，并不合并任务、commit 和 flow 的事实所有权。保留原因是小规模切片中授权/事务原子性需显式协调；新增执行/验收时提供独立命名模块，不继续堆入此接口。构造器只初始化，SQL 由根 owner 打开；HTTP owner 管理 API 生命周期并清理内存会话。

完整目标插件图仍为跨阶段设计，不等同根默认图。当前根图 12 个实际实例；examples/minimal 保持 3 实例 health。task/facts 等未来薄封装尚未注册，仓储接口已满足当前消费者；禁止把完整目标表当作已实现。

## 业务与协议

创建只允许人，默认负责人为当前主体；初始版本/BasisVersion=1。创建事务同时保存准备 FlowRef、RoundID、EntryID、位置、初始 commit、审计、outbox 和幂等回执。当前真实定义为 preparation/1，仅准备步骤；信息齐备后显示“准备就绪”，不虚构执行成功。相同 FlowRef ID/版本的行为摘要不可改变；不支持旧定义时拒绝读取/刷新。M05 新执行流程使用新的定义，不能悄悄扩展旧任务绑定。

正式依据修改要求有效成员且为负责人或租户管理员、ExpectedVersion 匹配；更新目标/标准、递增任务与依据版本，追加 task.basis commit，同事务刷新准备原因。当前没有活跃执行；M05 扩展编辑时必须保留退回/采用新依据保护，不能直接套用于运行中任务。

普通评论仅允许 Body 和可选 RefID，不接收任意 kind。更正只可引用同任务、同作者的普通评论，追加新 commit；旧正文、当前依据及流程位置不变。task.created/task.basis 类型由正式命令产生，无法通过评论 API 伪造。

业务写需 `Idempotency-Key`。相同 tenant/主体/操作/key 与载荷重放同结果；不同载荷冲突。重放前重新认证并校验当前成员与任务关系。所有作者/租户来自可信主体；任务列表、详情、commit 与变化读取按当前权限检查。API 不暴露原始 SQL、凭据或未授权正文。

| HTTP（/v1 前缀） | 操作 |
| --- | --- |
| POST /session | Bearer 凭据换取 HttpOnly/SameSite=Strict 会话 Cookie |
| DELETE /session | 失效当前服务端会话与 Cookie |
| GET/POST /tasks | 当前授权列表 / 创建 |
| GET/PATCH /tasks/{id} | 详情和 commit 分页 / 正式依据更新 |
| POST /tasks/{id}/participants | 负责人或管理员加入/移出参与者 |
| POST /tasks/{id}/commits | 追加评论/更正引用 |
| GET /changes | 获取起始游标或从游标补拉 |
| POST/DELETE /members | 设置成员角色/停用，限当前租户管理员 |

JSON 使用导出字段名（Title/Goal/Criteria、ExpectedVersion、Body/RefID）；路径绑定 TaskID，未知字段拒绝。正文上限 64KiB，业务文本上限 20000 字节。错误为 400 输入/游标需重同步、401 认证、403 授权、404 不存在、409 幂等/版本冲突、500 技术失败。普通业务写使用 HTTP，非浏览器可继续 Bearer；浏览器 Cookie 写入要求 X-Taskloom-Request:1，并拒绝非配置 Origin。秘密不放 localStorage。

会话期限 1 小时，逐请求重新核验底层凭据；内存会话数量上限 10000，关闭 owner 清理，进程重启需重新登录。浏览器凭据不写进回执/正文。加密 Cookie 同时绑定服务器活跃会话，登出后旧 Cookie 不再可用。

## 分页与同步

详情 commits 每页 100，`?after=<最后一个 Sequence>` 补拉，界面依次读取至末页。序号由同数据库协调写锁分配，顺序和不可变性由存储保证。列表当前返回所有授权任务，尚无大规模搜索/分页，容量优化归后续阶段。

先 GET /changes（不传 cursor）获取 StartCursor，再读任务快照，再用返回的 opaque cursor 补拉。游标 HMAC 绑定租户/主体和过期时间，期限 1 小时，重启或无效/超期游标返回 400，客户端明确要求刷新同步。成员或参与关系变更返回不带私有正文的 access.changed，触发列表重新读取。每批补拉最多扫描 100 个事件；未授权事件不返回 TaskID/内容，但扫描进度继续。UI 每 4 秒补拉，重复变化只触发按 ID 重读，不把 Ack 当业务处理完成。

本阶段使用 HTTP pull，未实现 WebSocket；新建、改依据、评论均有独立 task.changed 事件，不以状态变化作为唯一推送依据。M06 再按实际在线需求接 WS/慢客户端。内部 outbox 仍不对外暴露。

## 验证与边界

SQLite/HTTP 测试覆盖：原子初始化、信息不足、并发创建幂等、评论不改依据、引用更正、跨任务引用拒绝、commit 分页、版本冲突、撤权重放与列表/变化拒绝、跨主体游标、Origin/CSRF、未知 commit 类型、登录/登出会话失效。

内置浏览器在独立临时 SQLite 环境实际操作登录、创建、追加评论与更正，页面同时显示原文和新 commit。未连接生产数据库、未做负载测试；MySQL/PostgreSQL 仍为编入驱动而非真实服务验收。M04 不代表 M05 的执行/交付/验收、结构化阻塞问答或扩展 State 已实现。

当前 cap 已按消费者收敛，实际接口/Deps 及保留理由见[cap 能力清单](cap-interfaces.md)，不保留旧 Repository/Service 别名。State.Check 使用明确状态，Position.Complete 表达准备完成，Waiting 仅保存真实阻塞原因。
