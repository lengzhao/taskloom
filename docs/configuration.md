# 两层配置与实际可运行图

根 base/config 只描述已实现能力，当前为 SQLite 工作台、任务流程与本地目录文件存储图（见 [base.yaml](../base.yaml)）。未来架构只在[设计](architecture.md)描述，不提供未实现的完整业务图。

## 规则

运行时仅 base.yaml + config.yaml：base 由项目维护，config 为部署覆盖。同 ID 且 use 不变递归合并，标量覆盖、列表整体替换；use 改变整节点替换。继续使用 AgentKit MergeYAML 与 PluginKit build，不新增 resolver、容器或第三层。顶层 null 删除节点但不修复消费者引用。

当前加载器拒绝重复键、多文档、未知节点字段；字段类型由真实构造检查。只构造根可达图，未被引用的节点不构造也不报错。不进行隐式环境插值。凭据能力随身份/存储实现时引入，日志不打印敏感值。

## 业务配置边界

未来配置表达连接、凭据引用、限额、保留和服务启用；模块以接口和 Deps 配置组合，涵盖命令、事实能力及路由；内部辅助函数不变成无意义节点。不强制顶层实例，按实际复用选择具名或内联。`flow.config.acceptance`（manual/none，默认 manual）只决定人新建任务且未指定验收方式时的默认值，创建时写入任务位置（Agent 建子任务的默认见[交付与验收](core-contracts.md#交付与验收)）；修改部署配置不能暗改旧任务的验收方式或依据。`workspace.config.agentAssign` 限定 Agent 新建子任务时可分配的处理人：`any`（默认，空间内任一有效成员，人或 Agent）、`self`（只能是自己）、`none`（不能分配）；它是部署级的单项策略，不是逐动作授权配置，只约束之后的创建。

[examples/flows/automatic.config.yaml](../examples/flows/automatic.config.yaml) 把新任务的验收方式改为 none，可真实构建。

## 生命周期与验证

server 以 resources 管 SQL、以 components 管 http，http 管各 API 模块；未来 worker 等按明确 owner 装配。无后台领域模块不登记为组件，HTTP 连接不重复登记。业务能力实现并通过验证后才加入默认图；不创建占位插件。

`sh scripts/check.sh all` 验证当前工程及两层真实构建，`race` 检查竞态；文档链接与代码块由 Go 测试检查。不把构建或文档检查当作 SQL、授权、流程或 Agent 联调通过。

## SQL 连接配置

`storage/sql` 内部使用 GORM；连接字段为 `driver` 和 `dsn`。本地默认 `driver: sqlite`、`dsn: taskloom.db`（相对工作目录），生产通过同一 config.yaml 覆盖。切换到 MySQL/PostgreSQL 时同时选择对应 driver 和原生 DSN；同类型数据库只需改 DSN。业务 Deps、接口及状态配置不变，不新增配置层或通用 DSN 猜测规则。携带凭据的 DSN 不进入日志，当前通过受限部署 config 文件提供，不隐式插值环境变量。

SQL resource 由 server 打开和关闭。支持范围以实际编入并验证的驱动为准；数据库切换与已有数据搬迁是两件事。存储一致性与迁移见[架构](architecture.md#存储与迁移)。

## 文件存储配置

`blobs`（storage/fs）的 `dir` 是文件字节目录，默认 `taskloom-files`（相对工作目录），由 server 作为 resource 打开（创建目录）；Docker 镜像放在 `/data/files`。`files.config.maxBytes` 是单个文件上限（默认 104857600，即 100MiB），`file-api.config.transferTimeout` 是一次上传或下载的读写时限（默认 10m）。一个目录只由一个进程打开，打开时清理上次中断留下的临时文件。与数据库的一致性及备份要求见[文件与链接](core-contracts.md#文件与链接)。改用 S3 兼容存储时以同一 `cap/blob` 接口整节点替换 `blobs`，见[开发说明](development.md#文件存储)。

`http.config.origin`、loginTenant、Cookie 与生产 HTTPS 配置见 [API](api.md#通用约定)。

用户登录默认从主体目录确定唯一的人类成员租户，因此单租户部署无需填写租户。多租户数据库须在 `workspace-api.config.loginTenant` 指定本页面的登录租户；不提供匿名租户目录或跨租户用户名搜索。Agent 凭据仍携带可信租户。
