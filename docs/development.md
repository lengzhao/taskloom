# Go 工程与真实装配

## 目录与版本

模块为 `github.com/lengzhao/taskloom`，Go 1.27.3；直接依赖固定为 PluginKit v0.1.6、AgentKit v0.4.45、yaml.v3 v3.0.1；M03 新增 GORM v1.31.1 及 SQLite/MySQL/PostgreSQL 驱动 v1.6.0，见 go.mod/go.sum。

| 位置 | 当前职责 |
| --- | --- |
| cmd/taskloom | 参数、信号取消、进程退出码 |
| cap/lifecycle | Resource、Component、App 的生命周期契约 |
| cap/httpapi | Route、Module |
| runtime/config | 严格两文件输入、调用上游合并与真实 PluginKit build |
| runtime/server | 资源顺序打开、组件并行运行、取消及逆序关闭 |
| plugins/server | 根插件 typed Config/Deps，关闭超时默认 10s |
| plugins/transport/http | 路由挂载、监听及受管模块启停 |
| plugins/api/health | 进程存活检查 |
| plugins/all.go | 当前已实现插件的集中注册入口 |

构造函数不打开连接；根从 resources 顺序 Open 后运行 components，退出先 Stop 组件、再逆序 Close 资源。失败的 Open 自行清理部分状态。HTTP 独占其模块生命周期，模块不能重复列入根 resources。退出时限内组件应配合取消，Go 无法强制终止不合作的 goroutine。

## 业务装配

采用[模块化单体](architecture.md)。根 base/config 挂载 SQL、工作台与 M05 流程模块；不注册空业务 kind。通用加载器不解释业务配置；flow 的验收方式在构造时校验，未知值不得启动业务服务。

## 运行与检查

```sh
go build -o /tmp/taskloom ./cmd/taskloom
/tmp/taskloom -base base.yaml -config config.yaml -check
/tmp/taskloom -base base.yaml -config config.yaml
# 另一终端
curl http://127.0.0.1:8080/v1/healthz
# Ctrl-C 或 SIGTERM 退出
```

healthz 只返回进程存活，不表示 SQL、S3 或业务就绪。

`-check` 只构造并校验，不监听、不调用 Open/Run；成功打印实际实例数。`-config ''` 显式省略 L1；显式指定的文件缺失会失败。配置/运行错误退出 1，参数错误退出 2，帮助及正常信号退出为 0。错误不输出原始配置或构造器错误，仅保留阶段和实例 ID；实例 ID 应使用固定名称，不能包含凭据。

```sh
sh scripts/check.sh all     # 单测（含文档链接/代码块检查）、vet、构建、真实装配
sh scripts/check.sh race    # 竞态检查
```

CI（[.github/workflows/ci.yml](../.github/workflows/ci.yml)）在推送 main 和每个 PR 上于 ubuntu 用 go.mod 指定的 Go 版本并行运行以上两条命令，不另写检查逻辑。

Go 依赖首次需下载，不需要 Ruby。配置图由 PluginKit 真实构建校验（`-check` 与 runtime/config、workflow 扩展测试），文档链接由 `runtime/architecture` 测试校验。HTTP 和 CLI 测试需要本机回环端口。受限沙箱若禁止 bind，应在允许本机监听的环境运行，不能把环境失败算作通过。

## 两层装配实现

实际使用 AgentKit `config.MergeYAML`，不复制其合并算法；使用 PluginKit 注册、typed Config/Deps 和 `build.Build[lifecycle.Component]` 从 server 根构建。未复用 AgentKit 专属 runner 的全套 resolver。

同 ID 深合并、列表整体替换、use 改变整节点替换；顶层 null 移除实例，但不会自动修复引用或级联删除消费者。空文件、注释文件或 `{}` 为有效空 L1。重复键、多 YAML 文档、未知节点字段拒绝；不支持 extends、环境插值或隐式多文件搜索。凭据引用仍是未来凭据适配能力的数据，不由当前加载器解密。

仅构建根可达实例，未挂载的未知 kind 不会构造。真实类型、缺失引用、依赖环、Config 字段及 Validate 由 PluginKit 校验；流转语义由 `plugins/flow` 与 workflow 用例测试负责，不把静态引用校验当作流转实现。

测试覆盖合并/替换/禁用、可达与内联实例、配置错误、默认流程引用、脱敏、无监听构造、实际 HTTP、模块失败清理、根取消和资源逆序关闭。

## 初始化与 Agent 登记

```sh
printf '%s\n' "$ADMIN_PASSWORD" | go run ./cmd/taskloom-admin -mode bootstrap -password-stdin -tenant example -subject admin \
  -operator local-operator -reason initial-setup
go run ./cmd/taskloom-admin -mode enroll-agent -tenant example \
  -subject worker-1 -owner admin -operator local-operator -reason agent-enrollment \
  -credential-file /private/tmp/taskloom-worker.json
go run ./cmd/taskloom
```

打开 `http://127.0.0.1:8080/v1/app`，用 `admin` 和 stdin 给出的密码登录（8–128 字节，不经命令行参数）。人只用密码登录，初始化和恢复不签发凭据、不写任何文件。遗失密码时用 `-mode recover -password-stdin`：撤销该人全部旧凭据与会话并换成新密码。只有 `enroll-agent` 写 `-credential-file`：文件必须不存在、权限 0600，秘密不打印；数据库与文件不共享事务，写文件失败时换新路径重新登记。`-operator` 是审计声明，可信边界是部署机与数据库运维权限。

## 文件存储

默认 `blobs` 是本地目录（`storage/fs`），与数据库一起备份。改用 S3 兼容存储时按[示例](../examples/storage/s3.config.yaml)在 config.yaml 中整节点替换 `blobs`（`use: storage/s3`，配置 endpoint、region、bucket、可选 prefix）。凭据只从 `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`（可选 `AWS_SESSION_TOKEN`）或实例 IAM 角色读取，不写进配置；`insecure`（明文 HTTP）只允许回环地址。启动时检查桶可访问；上传由 minio-go 流式分段（小文件单次 PUT），失败即中止，进程崩溃遗留的未完成分段须由桶生命周期规则清理（例如 1 天后中止）。补偿删除失败或崩溃留下的孤立字节不自动清理，见[文件与链接](core-contracts.md#文件与链接)。

S3 集成测试默认跳过，需要可写的 S3 兼容服务：

```bash
AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… TASKLOOM_S3_TEST_ENDPOINT=127.0.0.1:18333 TASKLOOM_S3_TEST_BUCKET=taskloom-test go test ./plugins/storage/s3/ -run TestService -v -count=1
```

桶不存在时测试会创建，对象写在随机前缀下并删除。

## 验证范围与已知限制

已由真实 SQLite、PluginKit 装配与 HTTP 测试覆盖：

- 存储：持久化、跨仓储回滚、嵌套错误/panic 的 rollback-only、迁移校验和拒绝、事件顺序。
- 安全：并发幂等与冲突、撤权后重放、跨租户、未知操作、初始化一次性、离线恢复、凭据签发不重复泄密、Agent 不继承所属人、管理员审计查询、Origin/CSRF、会话失效。
- 任务与流程：信息不足、评论不改依据、更正引用、commit 分页、依据版本冲突、并发认领、换凭据重新认领、取消与旧写入（含旧 Agent 实例留言与提问被拒，人和未执行过的 Agent 不受影响）、执行中改依据、结果替换、取消后重开、活跃与完成任务拒绝重开、未答问题阻止完成、人工处理与转交、无验收配置、变化补拉重新鉴权、任务列表/收件箱/概览按 `?id=` 限定（缺失与不可读同样不出现、超过 100 个或空值拒绝）、写入失败整体回滚、新 API 模块路由冲突启动前拒绝。
- 文件：本地目录存储的原子写入与拒绝覆盖、路径穿越键拒绝、打开时清理残留临时文件；未引用上传只对上传者可见；上传超限 413、幂等重放与同键不同字节冲突且均不留下字节、客户端中途断开不留下文件与字节且同键可重试、移出参与者或停用成员后不能列出/下载历史文件、非参与者不能上传或读取、跨任务读取与引用拒绝、不能引用他人未引用的上传、评论/结果只带文件或链接、引用缺失文件时结果整体回滚、相同交付不重复登记、MCP 上传/提交/读取与下载头。
- 任务拆分（2026-11-21）：更早的任务不能挂到更新的父任务下（成环随之不可能）；执行中的 Agent 经 HTTP 命令与 MCP `create_subtask` 拆分本任务，指定或默认人类负责人、自己成为参与者、新增必需子任务后自己的执行不失效且子任务完成后可提交；未参与、未认领、执行已失效、同一 Agent 的另一凭据、顶层任务、Agent 或不存在的负责人均拒绝；负责人调整计划仍使执行失效。
- 新待处理提醒（2026-11-21，内置浏览器 + 独立临时实例，lead 登录）：自己建任务并分配给自己不提醒；管理员分配任务给 lead 后一次补拉内出现"新"标记、顶部提示与桌面通知；管理员再向 lead 提问（处理人→被提问人）再次提醒；打开任务清除"新"；`?task=` 直达；刷新页面后已有待处理不标"新"。内置浏览器的通知权限固定为 denied，桌面通知以页面内替换的 `Notification` 验证调用，真实系统弹窗未验证。
- 项目可见性与负责人移交（2026-11-21）：`plugins/access/default/access_test.go` 与 `plugins/workflow/visibility_test.go`（真实配置图）覆盖项目负责人与成员（含 Agent）读、留言而不能管理、移动、移交、验收，非成员不可读，非成员不能建入或移入项目，成员可移入自己的任务，参与者不能再移动，移出成员即失去访问；离开租户的成员在保存时自动移出、归档项目保持成员访问、`project.file` 规则；移交的权限、Agent 拒绝、所见负责人冲突（含首尾空格）、原负责人重试已生效移交得到原结果而新请求 409、非读者不能借重试规则、新负责人验收而原负责人只读、执行不变、管理员移交已完成任务。内置浏览器 + 独立临时实例：lead 在工作台建项目并勾选成员、在项目中建任务，HTTP 实测成员读 200、留言 200、分配 403，非成员读 403；工作台移交给 member 后负责人变更、lead 成为参与者且失去任务控制、时间线显示"lead → member：原因"。
- 工作台：内置浏览器在独立临时库中验证过创建、分配、认领、提交、验收、项目与看板等主路径；2026-11-21 在独立实例中验证过按变化增量刷新：外部改名、分配、新建只重读变化的任务，取消后移出"等我处理"，项目变化时整表重读（其他最近界面改动只做了脚本语法检查）。

真实 Agent 联调（以下记录时 Agent 代码仍在本仓库 `harness/agentkit` 与 `deploy/agent`，2026-11-22 原样迁入 AgentKit 的 `platform/taskloom`，见 [MCP 接入](mcp-adapter.md#示例agentkit-常驻执行-agent)；2026-11-20，`taskloom:local` 独立实例 + `deploy/agent` 镜像 + deepseek-v4.1-flash，worker-1 一个实例）：

- 真实通过：分配后约 2 秒唤醒，认领→说明计划→提交→验收完成；验收退回后带退回原因唤醒并重新提交；运行中负责人留言被 steer 进当前 turn 且结果采纳；Agent 提问后负责人普通留言不唤醒、回答后一次唤醒并带上两条新动态；被提问（addressee）时只回答不认领；运行中取消或改派给另一 Agent 后旧执行的写入被拒（2026-11-21 起适配插件不再主动停止 turn，由 Agent 遇到拒绝后自行 finish，此路径尚未重新真实联调）；Taskloom 停机 40 秒期间按 3/6/12/24 秒退避且不调用模型，恢复后沿用原游标；Agent 容器运行中重启后自动唤醒并沿用原执行提交；撤销凭据后报错并按 60 秒退避；空闲时不调用模型；每个任务独立会话（`taskloom:<任务ID>`），Agent 自己的写入不唤醒自己。
- 仅单测（现为 AgentKit `runtime/platform/taskloom`）：任务离开收件箱后不再发送任何消息、阻塞后重新进入收件箱即唤醒、游标被拒后从 inbox 重新同步、人类凭据被拒、上下文取消退出。
- C08 真实验证（2026-11-21，修复后的 `taskloom:local` 独立实例 + 真实 Agent worker-1 认领，旧执行写入用同一凭据经 HTTP 发出）：取消后旧执行留言 409 且不留 commit、负责人仍可留言；改派给 worker-2 后，新处理人认领前后旧执行留言与提问均 409、新处理人留言成功；同一凭据依据变更后重新认领可留言；换新凭据认领后旧凭据留言 409、新凭据留言成功；从未执行的 Agent 参与者与负责人留言不受影响。
- Agent 拆分本任务（2026-11-21，`taskloom:m08` 独立实例 + `taskloom-agent:m08` + deepseek-v4.1-flash，worker-1 一个实例，两个任务）：Agent 认领后按任务说明调用 `create_subtask` 拆出必需子任务、负责人为指定用户 lead，父任务依据 1→2，Agent 的执行保持可写并跟随到依据 2，随后留言说明进度并结束本轮；lead 能读子任务、读不到父任务（关系不授予访问），子任务参与者为 worker-1；lead 把子任务分配给 worker-1，Agent 在独立会话中完成，lead 验收后父任务回到等处理人，Agent 被唤醒后用最初的执行（未重新认领）在依据 2 上提交汇总结果并验收完成。首轮联调发现适配插件只在"本进程首次看到"或"他人新写入"时唤醒，父任务因必需子任务完成重新进入收件箱时没有唤醒；已改为任务停止等待 Agent 后再次等待即视为重新进入并唤醒，第二个任务在不重启 Agent 的情况下真实通过（日志 `first=false entered=true new_commits=0`）。
- 新旧实例并存（同一 Agent 两个凭据同时运行）：被换下的旧实例仍按分配给本 Agent 的任务重新认领，反过来失效新实例并提交结果；隔离本身成立（谁被换下谁的写入 409），但两个实例会互相抢回执行。运行多个实例须各用不同 Agent，或停掉旧实例后再换凭据。
- 未验证：多 Agent 实例并发、慢客户端、长时间运行与会话压缩、Codex/Claude Code 等其他 Harness。

S3 适配（2026-11-21）：`storage/s3` 在本机 SeaweedFS（`chrislusf/seaweedfs` 容器的 S3 网关）上真实通过[文件存储](#文件存储)的集成测试：空/小/多段（40MiB）对象往返、缺失键 404 与删除幂等、读取方拒绝（超限）后不留对象且未完成分段已中止、请求取消后不留对象。取消时 minio-go 在已取消的上下文上中止分段会失败，实测留下未完成分段，由桶生命周期规则清理，与进程崩溃同样处理。MinIO 官方镜像与二进制在本机均无法获取，未用 MinIO；未连真实 AWS S3、未验证 IAM 角色凭据与桶生命周期规则；Taskloom 整体以 S3 为 blobs 的端到端运行未做，文件命令端到端测试使用本地目录存储。

尚未验证：Agent 进程外部工具的清理（本示例 Agent 没有 shell/文件工具）、MySQL/PostgreSQL 真实服务、生产负载、WebSocket、真实 AWS S3。本地测试不能表述为生产可用。
