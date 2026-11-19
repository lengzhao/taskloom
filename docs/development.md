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

Go 依赖首次需下载，不需要 Ruby。配置图由 PluginKit 真实构建校验（`-check` 与 runtime/config、workflow 扩展测试），文档链接由 `runtime/architecture` 测试校验。HTTP 和 CLI 测试需要本机回环端口。受限沙箱若禁止 bind，应在允许本机监听的环境运行，不能把环境失败算作通过。

## 两层装配实现

实际使用 AgentKit `config.MergeYAML`，不复制其合并算法；使用 PluginKit 注册、typed Config/Deps 和 `build.Build[lifecycle.App]` 从 server 根构建。未复用 AgentKit 专属 runner 的全套 resolver。

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

打开 `http://127.0.0.1:8080/v1/app`，用 `admin` 和 stdin 给出的密码登录（8–128 字节，不经命令行参数）。人只用密码登录，初始化和恢复不签发凭据、不写任何文件；脚本同样用密码登录（如 `TASKLOOM_ADMIN_PASSWORD=... python3 scripts/seed-demo.py`）。遗失密码时用 `-mode recover -password-stdin`：撤销该人全部旧凭据与会话并换成新密码。只有 `enroll-agent` 写 `-credential-file`：文件必须不存在、权限 0600，秘密不打印；数据库与文件不共享事务，写文件失败时换新路径重新登记。`-operator` 是审计声明，可信边界是部署机与数据库运维权限。

## 验证范围与已知限制

已由真实 SQLite、PluginKit 装配与 HTTP 测试覆盖：

- 存储：持久化、跨仓储回滚、嵌套错误/panic 的 rollback-only、迁移校验和拒绝、事件顺序。
- 安全：并发幂等与冲突、撤权后重放、跨租户、未知操作、初始化一次性、离线恢复、凭据签发不重复泄密、Agent 不继承所属人、管理员审计查询、Origin/CSRF、会话失效。
- 任务与流程：信息不足、评论不改依据、更正引用、commit 分页、依据版本冲突、并发认领、换凭据重新认领、取消与旧写入、执行中改依据、结果替换、退回/重开、未答问题阻止完成、人工处理与转交、无验收配置、变化补拉重新鉴权、写入失败整体回滚、新 API 模块路由冲突启动前拒绝。
- 工作台：内置浏览器在独立临时库中验证过创建、分配、认领、提交、验收、项目与看板等主路径（最近几轮界面改动只做了脚本语法检查）。

尚未验证：真实 Agent/Harness 联调与进程清理（M06）、MySQL/PostgreSQL 真实服务、生产负载、WebSocket、文件/S3。本地测试不能表述为生产可用。
