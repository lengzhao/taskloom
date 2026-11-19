# 两层配置与实际可运行图

根 base/config 只描述已实现能力，当前为 SQLite 工作台与 M05 流程图（见 [base.yaml](../base.yaml)）；examples/minimal 保留纯 health。未来架构只在[设计](architecture.md)描述，不提供未实现的完整业务图。

## 规则

运行时仅 base.yaml + config.yaml：base 由项目维护，config 为部署覆盖。同 ID 且 use 不变递归合并，标量覆盖、列表整体替换；use 改变整节点替换。继续使用 AgentKit MergeYAML 与 PluginKit build，不新增 resolver、容器或第三层。顶层 null 删除节点但不修复消费者引用。

当前加载器拒绝重复键、多文档、未知节点字段；字段类型由真实构造检查。只构造根可达图，未被引用的节点不构造也不报错。不进行隐式环境插值。凭据能力随身份/存储实现时引入，日志不打印敏感值。

## 业务配置边界

未来配置表达连接、凭据引用、限额、保留和服务启用；模块以接口和 Deps 配置组合，涵盖命令、事实能力、状态步骤及路由；内部辅助函数不变成无意义节点。不强制顶层实例，按实际复用选择具名或内联。默认任务策略仅影响新建任务，创建时持久化策略和 FlowRef；修改部署配置不能暗改旧任务模式或依据。验收要求由绑定 Definition 的步骤描述派生，是否包含人工验收步骤决定是否需要它通过；不再单独配置 acceptanceMode，Policy 仅保存验收人/职责分离。

旧 examples/flows 片段已退役为注释空覆盖，保留路径避免误用。它们暂不装配尚未实现的流程；manual/none 为步骤组合的派生展示信息，不是独立配置字段。新增定义只在流程目录 deps 中维护一份列表，命令/API/worker 依赖目录接口，避免 resources/driver/API 重复清单。实现后再提供可真实构建的流程片段。

## 生命周期与验证

server 以 resources 管 SQL、以 components 管 http，http 管各 API 模块；未来 worker 等按明确 owner 装配。无后台领域模块不登记为组件，HTTP 连接不重复登记。业务能力实现并通过验证后才加入默认图；不创建占位插件。

`sh scripts/check.sh all` 验证当前工程及两层真实构建，`race` 检查竞态；文档链接与代码块由 Go 测试检查。不把构建或文档检查当作 SQL、授权、流程或 Agent 联调通过。

## SQL 连接配置（M03 已实现）

`storage/sql` 内部使用 GORM；连接字段为 `driver` 和 `dsn`。本地默认 `driver: sqlite`、`dsn: taskloom.db`（相对工作目录），生产通过同一 config.yaml 覆盖。切换到 MySQL/PostgreSQL 时同时选择对应 driver 和原生 DSN；同类型数据库只需改 DSN。业务 Deps、接口及状态配置不变，不新增配置层或通用 DSN 猜测规则。携带凭据的 DSN 不进入日志，当前通过受限部署 config 文件提供，不隐式插值环境变量。

SQL resource 由 server 打开和关闭。支持范围以实际编入并验证的驱动为准；数据库切换与已有数据搬迁是两件事。存储一致性与迁移要求见 [M03](m03-storage.md)。

实际接口、数据库支持与运维边界见[M03 实现](m03-storage.md)；origin/localHTTP、Cookie 与生产 HTTPS 配置见[M04 说明](m04-workspace.md)。

用户登录默认从主体目录确定唯一的人类成员租户，因此单租户部署无需填写租户。多租户数据库须在 `workspace-api.config.loginTenant` 指定本页面的登录租户；不提供匿名租户目录或跨租户用户名搜索。Agent 凭据仍携带可信租户。
