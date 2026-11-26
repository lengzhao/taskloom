# Go 工程与真实装配

模块为 `github.com/lengzhao/taskloom`，Go 版本与依赖以 go.mod 为准。代码按 AGENTS.md 分层：公共契约 `cap/*`、领域规则 `domain/*`、插件 `plugins/*`（`plugins/all.go` 集中注册）、运行机制 `runtime/*`、入口 `cmd/*`；模块装配与生命周期见[架构](architecture.md#模块装配)。

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

`-check` 只构造并校验，不监听、不调用 Open/Run；成功打印实际实例数。`-log-level`（debug/info/warn/error，默认 info）设定写到 stderr 的诊断日志级别，见[日志](architecture.md#日志)。`-config ''` 显式省略 L1；显式指定的文件缺失会失败。配置/运行错误退出 1，参数错误退出 2，帮助及正常信号退出为 0。错误不输出原始配置或构造器错误，仅保留阶段和实例 ID；实例 ID 应使用固定名称，不能包含凭据。

```sh
sh scripts/check.sh all     # 单测（含文档链接/代码块检查）、vet、构建、真实装配
sh scripts/check.sh race    # 竞态检查
```

CI（[.github/workflows/ci.yml](../.github/workflows/ci.yml)）在推送 main 和每个 PR 上于 ubuntu 用 go.mod 指定的 Go 版本并行运行以上两条命令，不另写检查逻辑。

Go 依赖首次需下载，不需要 Ruby。配置图由 PluginKit 真实构建校验（`-check` 与 runtime/config、workflow 扩展测试），文档链接由 `runtime/architecture` 测试校验。HTTP 和 CLI 测试需要本机回环端口。受限沙箱若禁止 bind，应在允许本机监听的环境运行，不能把环境失败算作通过。

## 两层装配实现

合并规则见[两层配置](configuration.md#规则)。实现直接调用 AgentKit `config.MergeYAML` 与 PluginKit `build.Build[lifecycle.Component]`，不复制合并算法，不复用 AgentKit runner 的 resolver。空文件、注释文件或 `{}` 是有效的空 config；凭据引用由凭据适配能力解释，加载器不解密。流转语义由 `plugins/flow` 与 workflow 用例测试负责，静态引用校验不等于流转实现。

## 初始化与 Agent 登记

```sh
printf '%s\n' "$ADMIN_PASSWORD" | go run ./cmd/taskloom-admin -mode bootstrap -password-stdin -tenant example -subject admin \
  -operator local-operator -reason initial-setup
go run ./cmd/taskloom-admin -mode enroll-agent -tenant example \
  -subject worker-1 -owner admin -operator local-operator -reason agent-enrollment \
  -credential-file /private/tmp/taskloom-worker.json
go run ./cmd/taskloom
```

打开 `http://127.0.0.1:8080/v1/app`，用 `admin` 和 stdin 给出的密码登录（8–128 字节，不经命令行参数）。浏览器只用密码登录；人需要脚本或工具调用 API 时，由管理员在“用户与 Agent”中签发凭据。初始化和恢复不签发凭据、不写任何文件。遗失密码时用 `-mode recover -password-stdin`：撤销该人全部旧凭据与会话并换成新密码。只有 `enroll-agent` 写 `-credential-file`：文件必须不存在、权限 0600，秘密不打印；数据库与文件不共享事务，写文件失败时换新路径重新登记。`-operator` 是审计声明，可信边界是部署机与数据库运维权限。

## 文件存储

默认 `blobs` 是本地目录（`storage/fs`），与数据库一起备份。改用 S3 兼容存储时按[示例](../examples/storage/s3.config.yaml)在 config.yaml 中整节点替换 `blobs`（`use: storage/s3`，配置 endpoint、region、bucket、可选 prefix）。凭据只从 `AWS_ACCESS_KEY_ID`/`AWS_SECRET_ACCESS_KEY`（可选 `AWS_SESSION_TOKEN`）或实例 IAM 角色读取，不写进配置；`insecure`（明文 HTTP）只允许回环地址。启动时检查桶可访问；上传由 minio-go 流式分段（小文件单次 PUT），失败即中止，进程崩溃遗留的未完成分段须由桶生命周期规则清理（例如 1 天后中止）。补偿删除失败或崩溃留下的孤立字节不自动清理，见[文件与链接](core-contracts.md#文件与链接)。

S3 集成测试默认跳过，需要可写的 S3 兼容服务：

```bash
AWS_ACCESS_KEY_ID=… AWS_SECRET_ACCESS_KEY=… TASKLOOM_S3_TEST_ENDPOINT=127.0.0.1:18333 TASKLOOM_S3_TEST_BUCKET=taskloom-test go test ./plugins/storage/s3/ -run TestService -v -count=1
```

桶不存在时测试会创建，对象写在随机前缀下并删除。

## 验证范围与已知限制

已由真实 SQLite、PluginKit 装配与 HTTP 测试覆盖（具体用例见各包测试）：

- 存储：持久化、跨仓储回滚、rollback-only、迁移校验与旧库原地升级（command/002、workflow/002）、事件顺序、只读快照拒绝写入、事件与回执按保留期清理及下限。
- 安全：并发幂等与冲突、撤权后重放、跨租户、初始化一次性、离线恢复、凭据不重复泄密、Agent 不继承所属人、Origin/CSRF、会话失效。
- 任务与流程：依据版本冲突、并发认领、取消与旧执行写入、结果替换、重开限制、未答问题阻止完成、转交与改派资格（处理人、派送人、项目负责人）、列表分页、变化补拉重新鉴权与过期游标重同步、写入失败整体回滚、停机时在途请求完成。
- 文件：原子写入、路径穿越、上传超限与幂等、断开不留字节、移出参与者后不可读、跨任务引用拒绝、MCP 上传与读取。
- 任务拆分、项目可见性与负责人移交：见 `plugins/workflow`、`plugins/access/default` 的真实配置图测试。
- 工作台：内置浏览器在独立实例上验证过主路径、增量刷新与新待处理提醒，以及分页读取、负责人/处理人筛选、退回后转交、Skill 页的审核状态筛选与管理员审核；真实系统通知弹窗、resync 的真实触发（需 24h 以上保留期）未在浏览器中验证。

真实 Agent 联调（AgentKit `platform/taskloom` + preset，单实例）：唤醒、认领、提交、验收与退回重交、运行中 steer、提问与回答、取消或改派后旧执行写入被拒并自行结束、Taskloom 停机退避与游标恢复、Agent 重启续用原执行、撤销凭据、拆分必需子任务后父任务重新唤醒并汇总提交。同一 Agent 的新旧两个凭据并存时会互相抢回执行：多实例须各用不同 Agent，或先停旧实例再换凭据。

S3：`storage/s3` 的集成测试在本机 SeaweedFS S3 网关上通过；取消上传可能遗留未完成分段，由桶生命周期规则清理。

尚未验证：多 Agent 实例并发、慢客户端、长时间运行与会话压缩、其他 Harness、Agent 外部工具的进程清理、MySQL/PostgreSQL 真实服务、真实 AWS S3 与 IAM 角色、以 S3 为 blobs 的端到端运行、生产负载。本地测试不能表述为生产可用。
