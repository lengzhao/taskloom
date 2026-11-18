# Go 工程与真实装配

## 目录与版本

模块为 `github.com/lengzhao/taskloom`，Go 1.27.3；直接依赖固定为 PluginKit v0.1.6、AgentKit v0.4.45、yaml.v3 v3.0.1；M03 新增 GORM v1.31.1 及 SQLite/MySQL/PostgreSQL 驱动 v1.6.0，见 go.mod/go.sum。

| 位置 | 当前职责 |
| --- | --- |
| cmd/taskloom | 参数、信号取消、进程退出码 |
| cap/lifecycle | Resource、Component、App 的生命周期契约 |
| cap/httpapi | Route、Module 及可选 ModuleLifecycle |
| runtime/config | 严格两文件输入、调用上游合并与真实 PluginKit build |
| runtime/server | 资源顺序打开、组件并行运行、取消及逆序关闭 |
| plugins/server | 根插件 typed Config/Deps，关闭超时默认 10s |
| plugins/transport/http | 路由挂载、监听及受管模块启停 |
| plugins/api/health | 进程存活检查 |
| plugins/all.go | 当前已实现插件的集中注册入口 |

构造函数不打开连接；根从 resources 顺序 Open 后运行 components，退出先 Stop 组件、再逆序 Close 资源。失败的 Open 自行清理部分状态。HTTP 独占其模块生命周期，模块不能重复列入根 resources。退出时限内组件应配合取消，Go 无法强制终止不合作的 goroutine。

## 业务装配

采用[模块化单体](architecture.md)。根 base/config 挂载 SQL、工作台与 M05 流程模块，examples/minimal 保留纯 health；不注册空业务 kind。通用加载器不解释业务配置，流程定义、内联状态及默认 FlowRef 由已挂载的流程目录验证，失败不得启动业务服务。

## 运行与检查

```sh
go build -o /tmp/taskloom ./cmd/taskloom
/tmp/taskloom -base examples/minimal/base.yaml -config examples/minimal/config.yaml -check
/tmp/taskloom -base examples/minimal/base.yaml -config examples/minimal/config.yaml
# 另一终端
curl http://127.0.0.1:8080/v1/healthz
# Ctrl-C 或 SIGTERM 退出
```

最小图只有 server、transport/http、api/health 三种真实插件。healthz 返回进程存活，不表示 SQL、S3 或业务就绪。

`-check` 只构造并校验，不监听、不调用 Open/Run；成功打印实际实例数。`-config ''` 显式省略 L1；显式指定的文件缺失会失败。配置/运行错误退出 1，参数错误退出 2，帮助及正常信号退出为 0。错误不输出原始配置或构造器错误，仅保留阶段和实例 ID；实例 ID 应使用固定名称，不能包含凭据。

```sh
sh scripts/check.sh all     # 单测、vet、构建、最小真实装配、设计静态检查
sh scripts/check.sh race    # 竞态检查
sh scripts/check.sh design  # 仅设计配置与文档检查
```

Go 依赖首次需下载；design 使用 Ruby 标准 YAML 库。HTTP 和 CLI 测试需要本机回环端口。受限沙箱若禁止 bind，应在允许本机监听的环境运行，不能把环境失败算作通过。

## 两层装配实现

实际使用 AgentKit `config.MergeYAML`，不复制其合并算法；使用 PluginKit 注册、typed Config/Deps 和 `build.Build[lifecycle.App]` 从 server 根构建。未复用 AgentKit 专属 runner 的全套 resolver。

同 ID 深合并、列表整体替换、use 改变整节点替换；顶层 null 移除实例，但不会自动修复引用或级联删除消费者。空文件、注释文件或 `{}` 为有效空 L1。重复键、多 YAML 文档、未知节点字段拒绝；不支持 extends、环境插值或隐式多文件搜索。凭据引用仍是未来凭据适配能力的数据，不由当前加载器解密。

仅构建根可达实例，未挂载的未知 kind 不会构造。真实类型、缺失引用、依赖环、Config 字段及 Validate 由 PluginKit 校验；业务 FlowRef 语义校验归未来流程目录 owner（见 integration-contract.md）。可配置状态及固定保护语义由未来领域规则和用例测试负责，不把静态引用校验当作流转实现。

测试覆盖合并/替换/禁用、可达与内联实例、配置错误、默认流程引用、脱敏、无监听构造、实际 HTTP、模块失败清理、根取消和资源逆序关闭。

## 阶段实现

[M03](m03-storage.md) 存储与管理员 CLI（SQLite CGO 驱动需 C 编译器；MySQL/PostgreSQL 已编入但未经真实服务验证）、[M04](m04-workspace.md) 工作台与 HTTP 变化补拉、[M05](m05-workflow.md) 执行/结果/验收闭环的接口与验证见各页。WS 与 Harness 仍属后续阶段。
