# 成员与任务关系权限

当前权限只表达实际业务关系：租户成员、管理员、任务负责人、参与者。不建设自定义权限平台。认证仍产生可信租户/主体/代理/实例；记录作者不能由请求体伪造。

| 身份/关系 | 能力 |
| --- | --- |
| 有效人类租户成员 | 创建任务并成为负责人 |
| 有效人类租户管理员 | 管理本租户成员，读取和管理本租户任务 |
| 有效人类任务负责人 | 读取、修改任务约定、追加 commit、加入/移出参与者 |
| 有效人工处理人（仍有任务参与关系） | 认领处理、提交结果、查询候选和转交处理；不自动取得负责人或验收资格 |
| 有效任务参与者 | 读取与追加 commit；仅更正自己已有的评论 |
| 停用/不存在的成员 | 无任务访问或管理能力，即使仍保留负责人/参与历史 |
| Agent | 不继承 owner 任何权限；可显式参与任务读取/评论；执行提交须由 M05 的分配及有效写资格再限制 |

验收资格独立来自绑定流程的验收状态及任务策略，参与关系不授予验收资格。只签发 business 用途凭据；旧的 runtime 用途凭据一律拒绝。执行与写入隔离见 [M05](m05-workflow.md)。未知操作默认拒绝；操作名称仅用于命名用例、幂等和审计，不是部署者配置的权限项。

## 接口与存储

`cap/authorization` 保留 Member、Participant，以及 MemberReader/MemberWriter、Relations（含只读 IsAssigned）、ParticipantWriter 和固定业务 Authorizer。Check 的 resource 是明确任务 ID 或管理用例租户 ID；没有通用 Scope、Grant、通配符或动作目录。`access/default` 的 Deps 是 store→成员/参与仓储、tasks→任务事实接口，规则在代码中明确判断。

SQL 保存 memberRow（tenant/subject、role、active）、participantRow（tenant/task/subject）；负责人存于 Task。domain/security.SetMember 只允许有效人类管理员在自己租户操作，Agent 不能成为管理员，管理员不能通过普通命令撤销自己的管理身份。创建新主体默认建立有效 member，bootstrap/recover 建立 admin。离线恢复继续保留操作者、原因及凭据撤销审计。

workspace.SetParticipant 只允许有效管理员/负责人，加入对象必须是同租户有效成员。移出或停用不删除历史 commit，不转移任务负责人。重新激活成员会恢复其仍保留的任务关系；如不需要恢复，管理员/负责人应显式移出任务。

## 当前 API 与界面

- POST /v1/members：Tenant、Subject、Role（admin/member）、Active；更新已登记主体的租户成员关系。
- DELETE /v1/members：同一 DTO，将 Active 设为 false；仍需有效管理员身份。
- GET /v1/tasks/{id}/candidates：仅当前有效人类负责人/管理员或仍参与任务的当前人工处理人可读，返回同租户有效成员的 Subject、Kind、OwnerID；不返回凭据或运行实例信息。
- POST /v1/tasks/{id}/participants：Subject、Present；加入或移出任务参与者，TaskID 由路径绑定。

写入均需 Idempotency-Key，遵守现有 Cookie/Origin/CSRF 规则。旧 /grants 路由不存在，旧 Action/Scope 请求字段会被拒绝。界面去除逐动作授权编辑器；创建者直接拥有任务管理能力，任务详情提供用户/Agent 候选选择框用于参与者加入/移出；处理人可选择用户或 Agent，并标注 Agent 所属人；人工处理人在详情中开始处理、提交结果或转交给其他成员。无有效成员时提示管理员登记并启用后刷新；列表资格不代替写入时重新鉴权。成员登记及角色操作可通过命名 API 完成，不扩展为权限配置页面。

## 隔离与撤销

资源始终按认证租户查询。管理员不能跨租户设置成员，参与关系不能引用另一租户成员/任务，不能通过 owner 或 Agent owner 推导跨租户资格。所有业务事务按当前成员/负责人/参与关系检查；已提交幂等回执返回前也先检查当前关系，不能通过旧 key 取回已撤权内容。

成员/参与关系变更和业务写共用 SQL 协调锁：先提交的业务写有效，撤销先提交则后续写拒绝。列表、详情、commit、历史与变化补拉同样检查当前关系。权限变更发出无正文的 access.changed 通知促使界面重读；不把事件投递当授权依据，也不承诺追回已经读出的内容或已发生的外部副作用。

## 数据格式与验证

按用户决定不兼容旧权限格式：删除 Grant 表定义及操作，不保留迁移映射。旧数据库会因迁移校验和变化明确拒绝打开，需要在开发环境重建；程序不自动删除数据库文件。新数据库仅建立成员/参与 schema。

测试覆盖初始化直接可用、成员创建自动成为负责人、参与者可读/评论但不可改约定/管理参与者、移出后的幂等重放及变化读取拒绝、成员停用、跨租户拒绝、Agent 不继承所属人权限、旧逐动作 API 不存在。Harness 不在此次范围。

## 用户与 Agent 管理入口

页面顶部为当前有效管理员显示“用户与 Agent”，支持登记、查看、启用/停用和凭据签发/撤销。普通用户和 Agent 不显示入口，服务端同样拒绝管理操作。首次管理员初始化和离线恢复仍只在受控 CLI 中执行。

- GET /v1/me：当前有效成员的 Tenant、Subject、Kind、CanAdmin。
- GET /v1/members：仅管理员可读，返回本租户用户与 Agent（含停用成员）的标识、类型、所属人、角色、启用状态，无凭据信息。
- POST /v1/identities：Subject、Kind（human/agent）、OwnerID、Password（仅用户）；创建有效普通成员，Agent 的所属人须为同租户有效用户。
- POST /v1/credentials：Subject、ExpiresAt、InstanceID、Incarnation。仅为有效 Agent 签发，最长 90 天。
- POST /v1/credentials/revoke：CredentialID；管理员撤销本租户凭据。

新增身份和凭据 API 的租户由可信认证主体确定，浏览器不能提交 Tenant 或创建管理员角色。凭据明文只在首次签发响应中返回，可复制或下载；数据库/幂等回执只保存摘要或编号，重试只返回编号。明文关闭窗口即清空，不写浏览器持久化存储。网络结果不明确时页面沿用同一请求及幂等键；重放拿不到明文时可按返回编号撤销，再重新签发。停用不删除历史数据，重新启用恢复仍保留的任务关系。

### 密码登录

管理员在“用户与 Agent”中创建用户时同时填写初始密码；人类用户不再签发页面访问凭据。Agent 保留凭据签发流程。POST /v1/session 接收 Username/Password；POST /v1/account/password 接收 CurrentPassword/Password，用于本人修改密码（首次设置不需要旧密码）；POST /v1/users/{id}/password 仅供管理员重置同租户人类用户密码。两类改密均撤销目标的所有会话和凭据。GET /v1/me 的 PasswordSet 仅报告当前用户是否已设置密码。

POST /v1/identities 创建人类时需要 Password，创建 Agent 时拒绝 Password。POST /v1/credentials 仅接受 Agent。已有管理员可通过旧凭据首次登录设置密码；普通用户由管理员设置初始密码，之后直接用户名/密码登录。

## 空间与项目

有效成员可读本租户空间资料和项目目录。只有人类管理员可改空间资料；人类成员可创建项目，管理员或项目人类负责人可编辑、归档、恢复。负责人必须是同租户有效人类成员。项目目录可见不授予任务访问权；任务列表、计数与详情仍按原任务权限过滤。task.project 仅允许人类任务负责人/管理员。归档项目不允许新增或移入任务，允许原有任务继续处理及移出。所有写入同事务检查版本并重新鉴权，幂等重放也检查当前资格。运行时用途凭据不能读取项目目录。
