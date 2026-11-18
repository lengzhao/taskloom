# Go 工程与真实装配

更新：2026 年 11 月 18 日。T06/T07 已落地工程底座，业务闭环尚未实现。

## 目录与版本

模块为 `github.com/lengzhao/taskloom`，Go 1.27.3；直接依赖固定为 PluginKit v0.1.6、AgentKit v0.4.45、yaml.v3 v3.0.1，见 go.mod/go.sum。

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

## M01 业务方向

采用[模块化单体](architecture.md)，尚无业务模块实现。已有 HTTP Module、生命周期及两层加载保留。根 base/config 已切换为真实 health 图，与 examples/minimal 等价；未来适配包装随用例实现，不注册空业务 kind。

旧 runtime/config 中 task/catalog.defaultFlow 的专用检查暂保留为代码遗留，当前默认图不会触发；M02 按流程目录与 FlowRef 契约调整并更新其测试。本次不改运行时业务行为。

## 运行与检查

```sh
go build -o /tmp/taskloom ./cmd/taskloom
/tmp/taskloom -base examples/minimal/base.yaml -config examples/minimal/config.yaml -check
/tmp/taskloom -base examples/minimal/base.yaml -config examples/minimal/config.yaml
# 另一终端
curl http://127.0.0.1:8080/v1/healthz
# Ctrl-C 或 SIGTERM 退出
```

最小图只有 server、transport/http、api/health 三种真实插件。healthz 返回进程存活，不表示 SQL、S3 或业务就绪。当前 HTTP 仅挂载公开存活路由，没有开放业务接口；认证、授权、WebSocket 按 M03—M06 实现，不能据此部署业务系统。

`-check` 只构造并校验，不监听、不调用 Open/Run；成功打印实际实例数。`-config ''` 显式省略 L1；显式指定的文件缺失会失败。配置/运行错误退出 1，参数错误退出 2，帮助及正常信号退出为 0。错误不输出原始配置或构造器错误，仅保留阶段和实例 ID；实例 ID 应使用固定名称，不能包含凭据。

根目录 base/config 可以直接构建当前 health 服务；examples/minimal 是同等的独立两文件示例。均不代表业务闭环。

```sh
sh scripts/check.sh all     # 单测、vet、构建、最小真实装配、设计静态检查
sh scripts/check.sh race    # 竞态检查
sh scripts/check.sh design  # 仅设计配置与文档检查
```

Go 依赖首次需下载；design 使用 Ruby 标准 YAML 库。HTTP 和 CLI 测试需要本机回环端口。受限沙箱若禁止 bind，应在允许本机监听的环境运行，不能把环境失败算作通过。

## 两层装配实现

实际使用 AgentKit `config.MergeYAML`，不复制其合并算法；使用 PluginKit 注册、typed Config/Deps 和 `build.Build[lifecycle.App]` 从 server 根构建。未复用 AgentKit 专属 runner 的全套 resolver。

同 ID 深合并、列表整体替换、use 改变整节点替换；顶层 null 移除实例，但不会自动修复引用或级联删除消费者。空文件、注释文件或 `{}` 为有效空 L1。重复键、多 YAML 文档、未知节点字段拒绝；不支持 extends、环境插值或隐式多文件搜索。凭据引用仍是未来凭据适配能力的数据，不由当前加载器解密。

仅构建根可达实例，未挂载的未知 kind 不会构造。真实类型、缺失引用、依赖环、Config 字段及 Validate 由 PluginKit 校验；暂保留旧 task/catalog.defaultFlow 引用检查（M02 按修订后的目录契约核对）。可配置状态及固定保护语义由未来领域规则和用例测试负责，不把静态引用校验当作流转实现。

测试覆盖合并/替换/禁用、可达与内联实例、配置错误、默认流程引用、脱敏、无监听构造、实际 HTTP、模块失败清理、根取消和资源逆序关闭。事务、身份、SQL/S3、任务流转和真实 Buddy 联调均尚未实现或验证。

## M01 重设计验证记录（2026-11-18）

本轮只修改设计文档、项目规则、配置示例和配套检查/配置测试，没有新增或修改业务运行源码。

- `GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh all`：通过，单测/vet/构建、三实例最小装配、图引用/环/owner、文档链接及 C01—C18/M01—M11 检查通过。配置测试另以真实 PluginKit 构建根默认图。
- `GOCACHE=/tmp/taskloom-go-cache sh scripts/check.sh race`：通过。
- `git diff --check`：通过。

all 中 Go 尝试写模块 stat cache 时出现沙箱 `operation not permitted` 提示，命令最终退出 0，测试及构建成功；未申请扩大权限。SQL、身份、任务业务、WS、S3 及真实 Buddy 仍未实现/验证。原工作区已有未提交修改及未跟踪工程，本轮未提交或推送。

### M01 模块组合语义修订

同日按用户澄清重新梳理：模块接口通过 Deps 转为配置组合，恢复状态/检查/API 业务扩展点；上述首次 M01 记录中的固定流程取舍已被替代。此次仅设计文档、项目规则、开发清单及空示例注释变化，未改 Go 源码或检查脚本。

本次 `sh scripts/check.sh design` 与 `git diff --check` 通过。未重复 all/race，前次通过结果仅证明已有最小工程，不证明新增接口设计或 E01—E04 已实现。未提交或推送。

### 所有流程步骤统一为 State 的简化记录

本轮只更新文档、插件审查 JSON/Mermaid 及静态检查脚本；Go 业务源码和根运行配置未改。移除设计中的独立交付 checks/通用审核协调层，人工验收状态拥有记录与判定，flow 只负责可靠流转和公共安全保护。

`python3 scripts/check-plugin-inventory.py`、`sh scripts/check.sh design`、`git diff --check` 通过：51 个跨阶段候选节点、182 个 Deps 字段、205 条字段目标边，无构造环/悬空引用/不可达节点，完整图和表格一致，三个反向依赖环被检测；额外检查 flow 不直接依赖审核事实服务、交付不依赖 checks/reviews、人工验收不反向依赖 flow/命令。第一阶段 36 节点投影只是设计数据，不是实际 typed 装配。

本轮未运行 all/race：没有改业务运行源码，前次最小工程通过记录不代表新步骤语义已经运行验证。审核 wait/reject 保留合法交付、Enter 对新证据幂等、前序结论失效、旧异步回执拒绝，均作为 M05/M06 实现验收。未提交或推送。
