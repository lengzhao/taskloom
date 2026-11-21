# API 与权限

前端与 Agent 共用同一套业务 HTTP API（前缀 `/v1`），Agent 也可经 [MCP](mcp-adapter.md) 调用同一批命令。API 模块只做协议适配；授权、校验和事务都在命令模块里。不设专用 Agent API、回调或派发接口。行为约束见[核心契约](core-contracts.md)。

## 通用约定

- **认证**：浏览器只能用用户名和密码登录，登录签发的一小时凭据直接放在 HttpOnly、SameSite=Strict Cookie 中，有效与否只由凭据存储判断，进程重启或多实例不影响会话；非浏览器用 `Authorization: Bearer <凭据>`，这类凭据不能换成浏览器会话。
- **浏览器写入**：HTTP 传输层对所有路由统一要求：带 Origin 时必须等于 `http.config.origin`；无 Authorization 的非 GET 请求必须带 `X-Taskloom-Request: 1`。origin 只有回环主机（localhost、127.0.0.1、::1）可用 http，其余须 https；会话 Cookie 除回环主机外都是 Secure。多租户库须设置 `workspace-api.config.loginTenant`。
- **写入**：都需 `Idempotency-Key`；同键同载荷返回原结果，不同载荷 409。回执只存命令固定的引用与一次性返回值（如新建 ID、凭据编号），不存可推导的事实，见[命令生命周期](architecture.md#命令生命周期)。租户和作者来自认证主体，请求体不能指定。
- **载荷**：JSON 使用导出字段名，路径绑定 ID，未知字段拒绝。正文上限 64KiB，业务文本上限 20000 字节。文件上传例外，见[文件](#文件)。
- **错误**：400 输入或游标无效，401 认证，403 授权，404 不存在，409 冲突（版本、幂等、状态不允许），413 文件超过上限，500 技术失败。

## 权限

权限只表达实际业务关系，不建设通用 Scope/Grant 或逐动作授权配置；未知操作默认拒绝。

| 身份/关系 | 能力 |
| --- | --- |
| 有效人类成员 | 创建任务并成为负责人；读空间与项目目录，创建项目 |
| 有效人类管理员 | 管理本租户成员、身份、凭据与空间资料；读取和管理本租户任务；查询审计 |
| 任务负责人（有效人类） | 读写约定、追加 commit、管理参与者、分配、取消、重开、验收、调整子任务 |
| 当前人工处理人（仍参与） | 提交结果并转交；不取得负责人或验收资格 |
| 任务参与者 | 读取、评论（只能更正自己的评论）、提问（可问任一有效成员，被问者随之成为参与者） |
| Agent | 不继承所属人任何权限；须显式参与任务（被分配或被提问）；只能认领分配给自己的任务；可读成员目录 |
| 停用或不存在的成员 | 无任何访问，即使保留负责人/参与历史 |

资源始终按认证租户查询；每次读写和幂等重放都检查当前关系。任务写权限一律包含该任务的读权限，命令不再单独检查读取（由 access 测试固定）。成员或参与关系变更与业务写共用全局写锁，先提交者生效。停用不删除历史，重新启用恢复仍保留的任务关系。

## 会话、身份与管理

| 路径 | 说明 |
| --- | --- |
| POST /session、DELETE /session | Username/Password 登录（带 Bearer 返回 400）；登出吊销本会话的凭据，不影响其他会话 |
| POST /account/password | 本人修改密码（CurrentPassword、Password；首次设置不需要旧密码） |
| POST /users/{id}/password | 管理员重置同租户用户密码；两类改密都撤销目标的全部会话与凭据 |
| GET /me | Tenant、Subject、Kind、CanAdmin |
| GET /members | 管理员：本租户用户与 Agent（含停用），无凭据信息 |
| POST /members、DELETE /members | 管理员：设置或停用成员角色（admin/member）；Agent 不能成为管理员，管理员不能撤销自己 |
| POST /identities | 管理员：创建用户（需 Password，人只能用密码登录）或 Agent（需 OwnerID，为同租户有效用户） |
| POST /credentials | 管理员：为有效 Agent 签发凭据（Subject、ExpiresAt，最长 90 天），由命令本身拒绝给人签发；明文只在首次响应中返回，重放只返回编号 |
| POST /credentials/revoke | 管理员：按 CredentialID 撤销 |
| GET /audit?limit=N | 管理员：本租户最近审计记录（默认 100，最多 500） |

## 空间、项目与任务

当前工作空间即认证租户。项目是可选的组织方式，可见项目不授予任务访问权；归档项目不允许新增或移入任务，已有任务可继续处理和移出。

| 路径 | 说明 |
| --- | --- |
| GET/PATCH /space | 读取；管理员更新 Name、Description、ExpectedVersion，响应为当前空间 |
| GET/POST /projects、PATCH /projects/{id} | 目录、创建（Name、Description、Owner）与编辑/归档（另带 Archived、ExpectedVersion）；管理员或项目负责人可编辑；写入响应为该项目当前状态，回执只存项目 ID |
| GET /people | 有效成员目录（Subject、Kind、OwnerID），任一有效成员（含 Agent）可读；`?q=` 按 Subject 片段搜索、`kind=human\|agent`、按 Subject 分页（`after`，`limit` 默认 50、最多 200），返回 `{Items, Next}`；供选项目负责人、处理人、参与者和被提问人，不按任务过滤 |
| GET/POST /tasks | 当前可读任务列表（不含时间线；可用 `?id=` 限定，见[变化补拉](#变化补拉)）；创建（Title、Goal、Criteria、可选 ProjectID；可选 ParentID 与 Required 直接建为子任务，同一事务内创建并放置，需父任务调整子任务的资格，任一步失败都不留下任务；只传 Required 不传 ParentID 返回 400），响应为调用者此刻读到的任务详情 |
| GET/PATCH /tasks/{id} | 详情与 commit 分页（`?after=<Sequence>`，每页 100）；修改约定（Title、Goal、Criteria、BasisVersion），响应为当前任务详情 |
| POST /tasks/{id}/commits | 评论或更正（Body、可选 RefID，只能引用自己的评论；可选 Files、Links，见[文件](#文件)，三者至少一项）；响应为 `{TaskID, CommitID}`，重放返回同一 CommitID；已失效执行的 Agent 实例返回 409，见[写入隔离](core-contracts.md#执行与写入隔离) |
| POST /tasks/{id}/participants | 负责人或管理员加入/移出参与者（Subject、Present） |
| POST /tasks/{id}/project | 移入/移出项目（ProjectID，空为移出）；不固定版本，并发移动以后写为准；与子任务关系无关 |
| POST /tasks/{id}/parent | 放置子任务 {id}：ParentID（必填，不支持解除；祖先链超过 64 层视为冲突）、Required、SeenParentID、SeenRequired（所见当前关系，命令只固定它；父任务的其他变化不冲突）。挂接、切换必需、重挂需要父任务和子任务的负责人或管理员资格，只切换必需只需父任务资格；规则见[任务拆分](core-contracts.md#任务拆分)。新建子任务用带 ParentID 的 POST /tasks |

## 执行、交付与验收

客户端不传流程版本，每个命令只固定自己的对象（见[任务流程](task-flow.md#命令各自固定什么)）。

| 路径（/tasks/{id} 下） | 说明 |
| --- | --- |
| GET /workflow | 受权快照（见下） |
| POST /execution/assign | Subject；分配给人即开始人工执行 |
| POST /execution/open | BasisVersion；Agent 认领，或人在依据变更后重新开始 |
| POST /results | ExecutionID、Body、可选 Files、Links（三者至少一项，见[文件](#文件)） |
| POST /review | CommitID（当前结果 commit）、Outcome（approved/rejected）、Reason（退回必填） |
| POST /cancel | 无字段 |
| POST /restart | RoundID；重开已取消的任务为新一轮；活跃与已完成任务返回冲突（活跃任务用验收退回、转交或修改约定） |
| POST /questions | RoundID、Body、可选 Addressee（空为负责人；可为任一有效成员，不在任务中时同事务加为参与者，使其可读任务并回答）；已失效执行的 Agent 实例返回 409，同评论 |
| POST /questions/{qid}/answer | Body；仅被提问人回答当前轮问题 |
| POST /questions/{qid}/close | Reason（必填）；提问人撤回或负责人关闭当前轮未答问题，写 `question.closed` commit 并视同已结，不再阻止完成；被提问人用回答，不能关闭 |

以上写命令成功（含幂等重放）后，响应是调用者此刻的受权快照，与 `GET /workflow` 相同；命令本身只返回并回执 `Ref`（TaskID、命令后的 RoundID、新开的 ExecutionID），因此重放看到的是当前事实，不是写入当时的旧状态。

快照包含流程位置（Status、RoundID、Acceptance、推导的 State 与 Waiting）、处理人、当前执行、当前交付及正文、当前结论（CommitID、Reviewer、Outcome、Reason、BasisVersion；待验收时合成 pending）、未答问题，以及：

- `NextActor`：owner / assignee / reviewer / addressee；空表示没有固定角色需要行动。`Position.WaitingOnSubtasks`（等待必需子任务）时为空，除非本轮有未答问题（仍为 addressee）：下一步在子任务上，由子任务各自的 NextActor 承接。
- `Viewer` 与 `Participants`：当前查看者的 subject，以及任务当前参与者（负责人加入、被分配或被提问的成员；负责人本人已有全部权限，分配给自己时不记参与）。
- `Can*` 与 `Blocked`：当前查看者能否认领/提交/验收及不能的原因码（如 `not_assigned`、`execution_in_progress`、`basis_changed`、`not_reviewer`、`required_children_pending`，完整列表见 `cap/workflow`）。
- `Parent`、`Children`、`HiddenRequired`：调用者可读的直接父任务（含本任务是否被它必需）与直接子任务（标题、是否必需、阶段与步骤）；不可读的必需子任务只置 `HiddenRequired`，列表不含其标题或状态（ID 不是秘密，时间线可见，见[任务拆分](core-contracts.md#任务拆分)）。二者出自同一判断函数；它们只是提示，命令写入时重新检查。

`GET /v1/inbox` 列出当前调用者可读且 NextActor 指向自己的任务，其后列出调用者负责或处理、正在阻塞的任务（`WaitingOnSubtasks: true`，无 NextActor）：仍属于调用者，但暂时无事可做；最后一个必需子任务完成后自动回到前一类。与快照共用同一推导，不保存收件箱记录。工作台"等我处理"计数只算前一类，阻塞任务带"阻塞"标记，状态筛选可选"已阻塞"。

`GET /v1/overview` 为调用者可读的每个任务给出列表所需的推导事实：`Assignee`（当前处理人）、`ParentID`（直接父任务；ID 不是秘密，标题只在可读时由调用者自己的列表得到）、`UpdatedAt`（时间线最后一条 commit 的时间）。不保存，每次读取时推导。

`GET /tasks`、`/inbox`、`/overview` 都接受重复的 `?id=<任务ID>`（最多 100 个，空值 400），只返回这些任务中调用者可读的部分；不存在与不可读一样不出现，所以按 ID 读不比读全表多透露任何信息。

## 文件

规则见[核心契约](core-contracts.md#文件与链接)。

| 路径（/tasks/{id} 下） | 说明 |
| --- | --- |
| POST /files?name=<文件名> | 请求体为文件原始字节，`Content-Type` 作为媒体类型（缺省 application/octet-stream）；需要评论资格与 Idempotency-Key，同键同字节返回原文件，同键不同字节 409；超过 `files.config.maxBytes`（默认 100MiB）返回 413。响应为文件元数据 `{ID, Name, MediaType, Size, Digest, Uploader, CreatedAt…}`；只上传不进入时间线 |
| GET /files | 调用者可见的本任务文件元数据（已被引用的，以及自己尚未引用的上传），按上传时间排序 |
| GET /files/{fid} | 下载字节，一律 `Content-Disposition: attachment`，CSP sandbox |

`GET /v1/files/limits`（不在任务下，只需登录）返回 `{MaxBytes, MaxFiles, MaxLinks}`：单个上传上限（即 `files.config.maxBytes`）与每条 commit 的文件、链接数量上限，供客户端在发送前拒绝必然失败的请求；服务端照常校验。

评论与结果的 `Files` 是本任务 FileID 列表（最多 20 个，不重复），`Links` 是 `{URL, Title}` 列表（最多 20 个，URL 为 http/https 且不含用户信息，最长 2048 字节；Title 最长 200 字节）。时间线 commit 原样返回 Files 与 Links，名称与大小由 `GET /files` 对照。上传与下载各自的读写时限由 `file-api.config.transferTimeout`（默认 10m）替代全局 HTTP 时限。

## 变化补拉

1. `GET /v1/changes`（不带 cursor）返回当前游标，不回放历史。
2. 读取任务列表和快照。
3. 之后用 `GET /v1/changes?cursor=<游标>` 补拉，每次返回新游标。

游标就是事件序号。每条事件读取时重新鉴权，因此游标本身不授予任何访问，无需签名或过期；事件当前不清理。Change 只含变更类型与任务 ID：`task.changed`（调用者可读的任务有变化，重读快照）或 `access.changed`（成员、参与者、空间、项目变化，重读列表）。不推送正文或未授权摘要。工作台每 4 秒补拉一次：只有 `task.changed` 时用 `?id=` 重读这些任务（超过 100 个时整表重读），回答里缺席的任务从各列表移除；出现 `access.changed` 时整表重读。重复变化按 ID 重读即可。WebSocket 尚未实现。

## 工作台

`/v1/app` 是内嵌的薄界面，无外部资源和前端构建链。留言与提交结果可在"附件与链接"中选择文件（列出已选文件、可逐个移除；按 `GET /files/limits` 在发送前检查数量、单个大小、空文件和链接格式，再逐个上传后引用，失败提示文件名）和填写链接。它只依据快照的 `Can*`/`Blocked`/`NextActor` 决定可用操作，状态名只用于显示；取消、重开需二次确认；网络结果不明确时沿用同一幂等键。管理员在"用户与 Agent"中管理成员、签发和撤销凭据、查看审计记录；首次初始化与离线恢复只在 CLI 中执行。
