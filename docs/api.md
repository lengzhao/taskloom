# API 与权限

前端与 Agent 共用同一套业务 HTTP API（前缀 `/v1`），Agent 也可经 [MCP](mcp-adapter.md) 调用同一批命令。API 模块只做协议适配；授权、校验和事务都在命令模块里。不设专用 Agent API、回调或派发接口。行为约束见[核心契约](core-contracts.md)。

## 通用约定

- **认证**：浏览器只能用用户名和密码登录，登录签发的一小时凭据直接放在 HttpOnly、SameSite=Strict Cookie 中，有效与否只由凭据存储判断，进程重启或多实例不影响会话；非浏览器用 `Authorization: Bearer <凭据>`，这类凭据不能换成浏览器会话。
- **浏览器写入**：HTTP 传输层对所有路由统一要求：带 Origin 时必须等于 `http.config.origin`；无 Authorization 的非 GET 请求必须带 `X-Taskloom-Request: 1`。origin 只有回环主机（localhost、127.0.0.1、::1）可用 http，其余须 https；会话 Cookie 除回环主机外都是 Secure。多租户库须设置 `workspace-api.config.loginTenant`。
- **写入**：都需 `Idempotency-Key`（缺少时 400 `idempotency_key_required`）；同键同载荷返回原结果，不同载荷 409 `idempotency_key_reused`。回执只存命令固定的引用与一次性返回值（如新建 ID、凭据编号），不存可推导的事实，见[命令生命周期](architecture.md#命令生命周期)。租户和作者来自认证主体，请求体不能指定。
- **载荷**：JSON 使用导出字段名，路径绑定 ID，未知字段拒绝。正文上限 64KiB，业务文本上限 20000 字节。文件上传例外，见[文件](#文件)。响应不含租户（由认证决定）与任务创建序号。
- **错误**：响应体一律为 `{Code, Message, Field?}`：`Code` 是稳定的 snake_case 原因，程序据此分支；`Message` 是给人看的中文说明（每个 code 的文字只在 `cap/fault` 维护一份，HTTP 错误、MCP 错误和快照 `Blocked` 共用）；`Field` 指出出错的请求字段（如 `Title`、`ExecutionID`、`Idempotency-Key`）。状态码：400 输入无效，401 认证，403 授权（不可读的任务与不存在的任务同样返回 403，不泄露是否存在），404 不存在（不按任务鉴权的资源，如 skill 与文件），409 当前事实不允许（如 `basis_changed`、`round_changed`、`execution_fenced`、`result_changed`、`required_children_pending`），413 超过上限，429 登录过多，500 技术失败。冲突码与快照 `Blocked` 原因码同一套（见 `cap/workflow`），通用码与说明文字见 `cap/fault`。

## 权限

权限只表达实际业务关系，不建设通用 Scope/Grant 或逐动作授权配置；未知操作默认拒绝。

| 身份/关系 | 能力 |
| --- | --- |
| 有效人类成员 | 创建任务（默认自己为负责人，也可指定其他有效用户）；读空间与项目目录，创建项目；列出可读任务 |
| 有效人类管理员 | 管理本租户成员、身份、凭据、空间资料与标签表；读取和管理本租户任务；查询审计 |
| 任务负责人（有效人类） | 读写约定、追加 commit、管理参与者、分配、取消、重开、验收、调整子任务、移动项目、增删标签、移交负责人 |
| 当前处理人（人或 Agent，仍参与） | 提交结果（Agent 须先认领）并改派给他人；不取得负责人或验收资格 |
| 当前分配的派送人（仍参与） | 改派：做出当前这次分配的人或 Agent 可以再改给别人 |
| 任务参与者 | 读取、评论（只能更正自己的评论）、提问（可问任一有效成员，被问者随之成为参与者） |
| 项目负责人与项目成员（人或 Agent） | 对项目内每个任务等同参与者；项目负责人还可改派处理人；不能管理、移动、移交负责人或验收 |
| Agent | 不继承所属人任何权限；须显式参与任务（被分配或被提问）；只能认领分配给自己的任务；可新建顶层任务（负责人默认为所属人），持有当前执行时可在该任务下新建子任务；新建时按部署配置同时分配处理人（默认任一有效成员）、选择标签；可读成员目录与标签表 |
| Skill（启用时） | 有效成员读取可见目录并新建 skill；拥有者与协作者发布新版本；拥有者管理可见性、协作者与归档；人类管理员管理本租户全部 skill、审核（不审自己发布的版本）与重建索引，见 [Skill](skills.md#权限) |
| 停用或不存在的成员 | 无任何访问，即使保留负责人/参与历史 |

资源始终按认证租户查询；每次读写和幂等重放都检查当前关系。任务写权限一律包含该任务的读权限，命令不再单独检查读取（由 access 测试固定）。成员或参与关系变更与业务写共用全局写锁，先提交者生效。停用不删除历史，重新启用恢复仍保留的任务关系。

## 会话、身份与管理

管理接口统一在 `/admin` 下；`/people` 是任何有效成员可读的成员目录，`participants` 是单个任务的参与者，三者不混用。

| 路径 | 说明 |
| --- | --- |
| POST /session、DELETE /session | Username/Password 登录（带 Bearer 返回 400）；登出吊销本会话的凭据，不影响其他会话 |
| POST /account/password | 本人修改密码（CurrentPassword、Password；首次设置不需要旧密码） |
| GET /me | Tenant、Subject、Kind、CanAdmin |
| GET /admin/members | 管理员：本租户用户与 Agent（含停用），无凭据信息 |
| POST /admin/members | 管理员：设置成员角色与启停（Subject、Role admin/member、Active）；租户取自认证；Agent 不能成为管理员，管理员不能撤销自己 |
| POST /admin/identities | 管理员：创建用户（需 Password，人只能用密码登录）或 Agent（需 OwnerID，为同租户有效用户） |
| POST /admin/users/{id}/password | 管理员重置同租户用户密码；两类改密都撤销目标的全部登录会话，已签发凭据保持有效（见[身份、凭据与审计](architecture.md#身份凭据与审计)） |
| POST /admin/credentials | 管理员：为有效成员（人或 Agent）签发凭据（Subject、ExpiresAt，最长 360 天）；明文只在首次响应中返回，重放只返回编号 |
| POST /admin/credentials/revoke | 管理员：按 CredentialID 撤销 |
| GET /admin/audit?limit=N | 管理员：本租户最近审计记录（默认 100，最多 500） |

## 空间、项目与任务

当前工作空间即认证租户。项目是可选的组织方式，同时授予访问：项目负责人和成员对项目内每个任务等同参与者（见[权限](#权限)）。把任务归入项目等于与项目成员共享，因此只有任务负责人或管理员能移入/移出，且移入或直接建在项目中时，非管理员须是目标项目的负责人或成员；改项目成员或负责人即时改变这些访问；已离开租户的成员在下次保存项目时自动移出名单，不阻止保存，新加入的仍须是有效成员。归档项目不允许新增或移入任务，已有任务可继续处理和移出，项目负责人和成员的访问保持不变。是否可归入项目由 access 的固定操作 `project.file`（资源为项目 ID）判断。

| 路径 | 说明 |
| --- | --- |
| GET/PATCH /space | 读取；管理员更新 Name、Description、ExpectedVersion，响应为当前空间 |
| GET/POST /projects、PATCH /projects/{id} | 目录、创建（Name、Description、Owner、Members）与编辑/归档（另带 Archived、ExpectedVersion），整项替换，省略 Members 即清空成员；Members 为有效成员（人或 Agent），最多 200 个，自动去重并去掉负责人；管理员或项目负责人可编辑；写入响应为该项目当前状态，回执只存项目 ID |
| GET/POST /tags/recommended | 推荐标签：GET 返回配置中推荐、而空间里按名称（忽略大小写，含已归档）还没有的标签 `[{Name, Description}]`，任一有效成员可读；POST 由人类管理员把它们一次导入为普通标签（无请求体），响应为当前标签表；超过空间上限 409，回执只存新建的标签 ID。规则见[层级与标签](core-contracts.md#层级与标签) |
| GET /tags、POST /tags、PATCH /tags/{id} | 标签表：任一有效成员（含 Agent）可读，按名称排序，含已归档的；人类管理员新建（Name 1–40 字、可选 Description）或编辑/归档（另带 Archived、ExpectedVersion，整项替换）；名称在空间内唯一（忽略大小写），重名 409，最多 200 个；写入响应为该标签当前状态。规则见[层级与标签](core-contracts.md#层级与标签) |
| GET /people | 有效成员目录（Subject、Kind、OwnerID），任一有效成员（含 Agent）可读；`?q=` 按 Subject 片段搜索、`kind=human\|agent`、按 Subject 分页（`after`，`limit` 默认 50、最多 200），返回 `{Items, Next}`；供选项目负责人、处理人、参与者和被提问人，不按任务过滤 |
| GET /tasks | 任务列表，每行是推导出的 `{Task, Position, Assignee, ParentID, NextActor, ForMe, UpdatedAt}`（UpdatedAt 为时间线最后一条 commit 的时间），不保存，读取时推导；`?mine=true` 只列调用者的工作：先是 NextActor 指向自己的任务（`ForMe`），再是自己负责或处理、正在等待必需子任务的任务（`Position.WaitingOnSubtasks`）；可重复 `?id=`（最多 100 个，空值 400）只返回这些任务中可读的部分，不存在与不可读一样不出现；`?limit=N`（1–200）分页：响应体仍是数组，还有下一页时响应头 `X-Next-Cursor` 给出游标，用 `?cursor=` 续读，页按任务 ID 排序（`mine` 时页内先 ForMe 后阻塞），`limit` 不与 `id` 同用；不带 `limit` 返回全表（兼容旧客户端） |
| POST /tasks | 创建（Title 必填，Goal、Criteria 可选、可后补；可选 ProjectID；人和 Agent 都可调用，顶层任务与子任务只差是否带 ParentID；可选 ParentID 与 Required 直接建为子任务，同一事务内创建并放置，需父任务调整子任务的资格，Agent 则需持有父任务当前可写执行，任一步失败都不留下任务；只传 Required 不传 ParentID 返回 400；可选 Owner 为空间内有效人类成员，空时人为本人，Agent 建子任务为父任务负责人、建顶层任务为其所属人（所属人已停用时 400 `owner_ambiguous`），Owner 不是有效人类成员返回 400；创建者不是负责人时成为参与者；可选 Acceptance（`manual`/`none`，其他值 400，空时的默认见[核心契约](core-contracts.md#标识与版本)）；可选 Tags（标签 ID，最多 10 个，须是未归档的标签，否则 400）；可选 Assignee 同事务分配，人的资格同分配命令，Agent 可填的范围由部署配置决定，超出返回 403，缺少目标时 409，见[任务拆分](core-contracts.md#任务拆分)） |
| GET /tasks/{id} | 任务视图：任务、调用者的流程快照（见[执行、交付与验收](#执行交付与验收)）、`CanEdit`/`CanComment` 与一页时间线：默认是最新的 100 条（页内按时间正序），`Earlier: true` 表示前面还有，用 `?before=<页内最早一条的 Sequence>` 往前翻 |
| PATCH /tasks/{id} | 修改任务要求（Title、Goal、Criteria、BasisVersion） |
| POST /tasks/{id}/commits | 评论或更正（Body、可选 RefID，只能引用自己的评论；可选 Files、Links，见[文件](#文件)，三者至少一项）；已失效执行的 Agent 实例返回 409，见[写入隔离](core-contracts.md#执行与写入隔离) |
| POST /tasks/{id}/participants | 负责人或管理员加入/移出参与者（Subject、Present） |
| POST /tasks/{id}/project | 任务负责人或管理员移入/移出项目（ProjectID，空为移出）；移入时非管理员须属于目标项目；不固定版本，并发移动以后写为准；与子任务关系无关；移出原项目（含改到别的项目）发 `access.changed`，仅归入项目发 `task.changed` |
| POST /tasks/{id}/tags | 任务负责人或管理员增删标签：Add、Remove（标签 ID 列表）；按差量合并，不固定版本，不改变依据；Add 中未知或已归档的标签 400，结果超过 10 个 400；Remove 中不在任务上的忽略；没有实际变化时不写事件；发 `task.changed` |
| POST /tasks/{id}/owner | 当前负责人或管理员移交负责人：Owner（有效人类成员）、SeenOwner（所见当前负责人，不符即 409）、可选 Reason；任何状态都可移交；写 `task.owner` commit（正文为 `{From, To, Reason}`），原负责人保留为参与者，处理人与执行不变；发 `task.changed`（没有人失去读取）；做出移交的原负责人仍可读任务且所见移交已生效时，可用同一幂等键重试，新请求因所见负责人不符返回 409 `owner_changed` |
| POST /tasks/{id}/parent | 放置子任务 {id}：ParentID（必填，不支持解除；子任务须比父任务新，否则冲突）、Required、SeenParentID、SeenRequired（所见当前关系，命令只固定它；父任务的其他变化不冲突）。挂接、切换必需、重挂需要父任务和子任务的负责人或管理员资格，只切换必需只需父任务资格；规则见[任务拆分](core-contracts.md#任务拆分)。新建子任务用带 ParentID 的 POST /tasks |

本节与下一节的每个任务写入（含幂等重放）成功后，响应都是调用者此刻的任务视图，与 `GET /tasks/{id}` 相同；命令回执只存它固定的引用，所以重放看到的是当前事实，不是写入当时的旧状态。写入后调用者已不能读该任务（如移出自己所在的项目）时返回 204。

## 执行、交付与验收

客户端不传流程版本，每个命令只固定自己的对象（见[任务流程](task-flow.md#命令各自固定什么)）。

| 路径（/tasks/{id} 下） | 说明 |
| --- | --- |
| POST /assign | Subject；分配给人即开始人工执行 |
| POST /start | BasisVersion；Agent 认领，或人在要求变更后重新开始 |
| GET /results/{result_id} | 按稳定结果 commit ID 读取正文分页；可用 `cursor` 续读，见下文 |
| POST /results | ExecutionID、Body、可选 Files、Links（三者至少一项，见[文件](#文件)） |
| POST /review | CommitID（当前结果 commit）、Outcome（approved/rejected）、Reason（退回必填） |
| POST /cancel | 无字段 |
| POST /restart | RoundID；重开已取消的任务为新一轮；活跃与已完成任务返回冲突（活跃任务用验收退回、转交或修改约定） |
| POST /questions | RoundID、Body、可选 Addressee（空为负责人；可为任一有效成员，不在任务中时同事务加为参与者，使其可读任务并回答）；已失效执行的 Agent 实例返回 409，同评论 |
| POST /questions/{qid}/answer | Body；仅被提问人回答当前轮问题 |
| POST /questions/{qid}/close | Reason（必填）；提问人撤回或负责人关闭当前轮未答问题，写 `question.closed` commit 并视同已结，不再阻止完成；被提问人用回答，不能关闭 |

以上写命令的响应同样是任务视图（见上）；命令本身只回执 `Ref`（TaskID、命令后的 RoundID、新开的 ExecutionID、实际提交的 ResultID）。

结果正文按需读取：`GET /v1/tasks/{id}/results/{result_id}?cursor=...` 返回 `{task_id, result_id, text, next_cursor?, files?, links?}`。每页最多 2000 个 Unicode 字符，原样传回 `next_cursor` 直到不再返回；空 cursor 从头开始，非法或越界 cursor 返回 400。结果编号是不可变的 result commit ID，替换当前结果后旧结果仍可读取；非结果 commit 与跨任务引用返回 404。每页重新认证与校验 `task.read`，cursor 不授予权限。正文仍只保存于原 commit；分页不新增存储或游标会话。已有任务视图保持完整正文，供现有工作台兼容使用。

任务视图中的快照字段包含流程位置（Status、RoundID、Acceptance、推导的 State 与 Waiting；活跃任务尚无处理人且无结果时 Waiting 为"等待负责人分配处理人"）、处理人、当前执行、当前交付及正文、当前结论（CommitID、Reviewer、Outcome、Reason、BasisVersion；待验收时合成 pending）、未答问题（带提问人 `Asker`、正文 `Body`、时间 `AskedAt`，不必在时间线里找问题 commit），以及：

- `NextActor`：owner / assignee / reviewer / addressee；空表示没有固定角色需要行动。`Position.WaitingOnSubtasks`（等待必需子任务）时为空，除非本轮有未答问题（仍为 addressee）：下一步在子任务上，由子任务各自的 NextActor 承接。
- `Viewer` 与 `Participants`：当前查看者的 subject，以及任务当前参与者（负责人加入、被分配或被提问的成员；负责人本人已有全部权限，分配给自己时不记参与）。
- `Can*` 与 `Blocked`：当前查看者能否认领/提交/验收；不能时 `Blocked[动作]` 为 `{Code, Message}`（如 `not_assigned`、`execution_in_progress`、`basis_changed`、`not_reviewer`、`required_children_pending`，完整列表见 `cap/workflow`）。
- `Parent`、`Children`、`HiddenRequired`：调用者可读的直接父任务（含本任务是否被它必需）与直接子任务（标题、是否必需、阶段与步骤）；不可读的必需子任务只置 `HiddenRequired`，列表不含其标题或状态（ID 不是秘密，时间线可见，见[任务拆分](core-contracts.md#任务拆分)）。二者出自同一判断函数；它们只是提示，命令写入时重新检查。

工作台"等我处理"计数只算 `ForMe` 的行，等待子任务的行带"阻塞"标记，状态筛选可选"已阻塞"。

## 文件

规则见[核心契约](core-contracts.md#文件与链接)。

| 路径（/tasks/{id} 下） | 说明 |
| --- | --- |
| POST /files?name=<文件名> | 请求体为文件原始字节，`Content-Type` 作为媒体类型（缺省 application/octet-stream）；需要评论资格与 Idempotency-Key，同键同字节返回原文件，同键不同字节 409；超过 `files.config.maxBytes`（默认 100MiB）返回 413。响应为文件元数据 `{ID, Name, MediaType, Size, Digest, Uploader, CreatedAt…}`；只上传不进入时间线 |
| GET /files | 调用者可见的本任务文件元数据（已被引用的，以及自己尚未引用的上传），按上传时间排序 |
| GET /files/{fid} | 下载字节，一律 `Content-Disposition: attachment`，CSP sandbox |

`GET /v1/files/limits`（不在任务下，只需登录）返回 `{MaxBytes, MaxFiles, MaxLinks}`：单个上传上限（即 `files.config.maxBytes`）与每条 commit 的文件、链接数量上限，供客户端在发送前拒绝必然失败的请求；服务端照常校验。

评论与结果的 `Files` 是本任务 FileID 列表（最多 20 个，不重复），`Links` 是 `{URL, Title}` 列表（最多 20 个，URL 为 http/https 且不含用户信息，最长 2048 字节；Title 最长 200 字节）。时间线 commit 原样返回 Files 与 Links，名称与大小由 `GET /files` 对照。上传与下载各自的读写时限由 `file-api.config.transferTimeout`（默认 10m）替代全局 HTTP 时限。

## Skill

只在装配了 [skill 覆盖示例](../examples/skills/skills.config.yaml) 时存在；模型、检查与判定规则见 [Skill 交付控制面](skills.md)。以下路径都在 `/v1/skills` 下，写入都需 Idempotency-Key。

| 路径 | 说明 |
| --- | --- |
| GET /capabilities?match=&q=&page=&size=&min_score=&trust= | 目录与检索：match 为空（浏览）或部署启用的 name、lexical、bm25、semantic（未启用返回 400）；size 默认 24、最多 100；`trust`（approved/rejected/unreviewed）只留当前版本为该审核状态的，`trust=unreviewed` 即待审核队列；返回 `{Items:[{Capability…, Release, Score, Trust}], Total}`，Release 为当前版本，Trust 由其最新审核推导 |
| GET /capabilities/resolve?name= | 语义名 → 当前版本与 trust |
| POST /capabilities | 请求体为 zip；首次发布即创建（private），之后追加更高版本；不通过检查返回 400 `{Diagnostics:[{Code, Message, Blocking}]}`，超过 `skills.config.maxBytes` 返回 413 |
| POST /capabilities/publication-preflight | 同一套检查的只读报告 `{Name, Version, Digest, Size, Publishable, Diagnostics}` |
| PATCH /capabilities/{id} | `{Visibility: private\|tenant}` |
| PUT /capabilities/{id}/collaborators | `{Subjects}` 整体替换（最多 50） |
| POST /capabilities/{id}/archive、/restore | 归档与恢复，不删除历史 |
| GET /releases/{ref} | 确切版本与 trust；旧版本可读 |
| GET /releases/{ref}/artifact | 原样 zip，附 `X-Content-SHA256` |
| POST /capability-decisions | `{ReleaseRef, Host:{Contract, Affordances, Apps}}` → `{State, Reason, Trust, Missing, Action}`；只读 |
| POST /admin/releases/{ref}/reviews | `{Verdict: approved\|rejected, Reason}`，人类管理员，不能审自己发布的版本；重放返回该键写入的那条审核 |
| POST /admin/reindex | 人类管理员，补齐当前版本缺失的检索向量，返回 `{Indexed}` |

## 变化补拉

1. `GET /v1/changes`（不带 cursor）返回当前游标，不回放历史。
2. 读取任务列表和快照。
3. 之后用 `GET /v1/changes?cursor=<游标>` 补拉，每次返回新游标。

游标就是事件序号。每条事件读取时重新鉴权，因此游标本身不授予任何访问，无需签名。事件按 `sql.config.eventRetention`（默认 720h，至少 24h）清理：游标早于已清理范围时返回 409 `resync_required`，客户端须重读列表并用不带 cursor 的请求取新游标；新游标不会落在已清理范围内。Change 只含变更类型与任务 ID：`task.changed`（调用者可读的任务有变化，重读快照）或 `access.changed`（成员、参与者、空间、项目或任务移出项目，可能有人失去访问，重读列表）。不推送正文或未授权摘要。工作台收到 `resync_required` 时自动重取游标并整表重读。工作台每 4 秒补拉一次：只有 `task.changed` 时用 `GET /tasks?id=` 重读这些任务（超过 100 个时整表重读），回答里缺席的任务从各列表移除，正在查看的任务在其中时才重读它；出现 `access.changed` 时整表重读并重读成员目录。没有变化时不发其他请求。重复变化按 ID 重读即可。WebSocket 尚未实现。AgentKit 的 `platform/taskloom` 读取本节与 `/me`、`/tasks?mine=true`、`/tasks/{id}`，见 [MCP 接入](mcp-adapter.md#示例agentkit-常驻执行-agent)。

## 工作台

`/v1/app` 是内嵌的薄界面，无外部资源和前端构建链。留言与提交结果可在"附件与链接"中选择文件（列出已选文件、可逐个移除；按 `GET /files/limits` 在发送前检查数量、单个大小、空文件和链接格式，再逐个上传后引用，失败提示文件名）和填写链接。它只依据快照的 `Can*`/`Blocked`/`NextActor` 决定可用操作，状态名只用于显示；界面不出现依据版本、轮次、执行实例等内部机制名，统一说"任务要求""重新开始"；错误直接显示响应的 `Message` 与 `Field`；取消、重开需二次确认；网络结果不明确时沿用同一幂等键。写入后直接用响应里的任务视图渲染，只按 ID 重读该任务的列表行和文件列表；打开任务只读该任务；成员目录每次登录读一次。已有待验收结果时，重新提交收在"替换已提交的结果"里；结果被退回后，有改派资格的人仍可"转交处理"。任务列表按每页 200 条分页读取，筛选（项目、层级、标签、负责人、处理人、状态、搜索）在已读取的列表上进行；层级按 `ParentID` 推导：目标（已拆出子任务的顶层任务）、顶层任务、子任务。"目标"页列出调用者负责的目标，按整棵可读子树汇总：必需子任务完成数、免验收自动完成数，以及等待调用者处理（验收、回答、安排）的子任务。任务卡片和详情显示标签，负责人和管理员可在详情里增删；新建任务和子任务时可选标签；管理员在"空间设置"里维护标签表，配置了推荐标签且空间还缺时可预览并一键"导入推荐标签"；标签表为空时，标签筛选和新建表单提示管理员去导入，其他人联系管理员。装配了 skill 模块组时出现"Skill"页：浏览可见 skill、按审核状态筛选（`trust`）、下载 zip；人类管理员可对当前版本通过或拒绝（不能审自己发布的）。时间线默认显示最新一页，"加载更早记录"向前翻。管理员在"用户与 Agent"中管理成员、签发和撤销凭据、查看审计记录；首次初始化与离线恢复只在 CLI 中执行。

新待处理提醒不在服务端存任何记录：每次读任务列表（整表或按 `?id=`）时，与上一次相比新进入、或等待角色变化（如由处理人变为被提问人）的任务标"新"、在页面顶部提示，用户在顶栏"开启桌面通知"并允许后还会弹出浏览器桌面通知，点击打开该任务；打开任务即清除"新"。本页刚写过的任务在随后的那次读取中跳过，所以自己的操作（如分配给自己）不提醒。登录或刷新页面后的首次读取只建立基线，不标"新"。因此提醒只在页面打开期间有效；人不在页面时的渠道（飞书、邮件）以及 Agent 推送需要服务端检测，届时再作为新模块加入。`/v1/app?task=<任务ID>` 直接打开该任务。
