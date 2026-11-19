# 工作空间、轻量项目与任务页面实施计划

已由用户确认：当前空间设置、项目管理、任务导航和详情；测试、提交并更新本地服务。

## 设计与公共契约

工作空间复用 Tenant，只增加名称、描述及乐观版本；缺省空间资料由当前租户派生，首次保存创建。项目属于租户，字段为名称、描述、负责人、归档及版本。任务新增可选 ProjectID，旧 JSON 读取为空，旧任务不迁移归属。项目成员使用现有租户成员，暂不引入私有项目与跨空间账号。

新增 cap/catalog 的空间/项目存储和应用接口，由 SQL 和独立 catalog/projects 插件实现。项目管理不扩大 workspace/tasks 的业务职责；它只增加创建/移动任务时读取项目有效性的依赖。任务移动增加版本但不改 BasisVersion、流程与授权。API 使用 catalog 应用接口。所有写入通过现有 command 生命周期及同一事务，包含审计、事件、幂等和版本检查。新增接口消费者为 SQL、catalog、workspace 和 API；旧任务 JSON 向后可读，新增数据库迁移回滚须恢复备份。

权限：有效成员读取空间和项目目录，人类成员创建项目；管理员或人类项目负责人修改/归档项目，空间资料仅管理员修改。任务归属变更仅原任务负责人/管理员执行，归档项目禁止新增/移入，但允许移出。项目可见不授予任务权限，统计从已授权任务列表派生。

## 实施步骤

1. cap/catalog/catalog.go、plugins/storage/sql/catalog.go：版本化空间和项目存储、独立迁移；cap/task 增加 ProjectID 及显式归属变更。
2. plugins/catalog/service.go：空间和项目命名操作；plugins/workspace/service.go：创建时校验项目、显式移动及变更事件；base.yaml 与注册更新。
3. plugins/api/workspace：新增空间、项目、任务归属 API；app.html 增加导航、项目详情、空间设置、任务搜索与列表/看板。
4. HTTP 集成回归覆盖权限、租户隔离、版本冲突、幂等、归档与统计不泄露；全量 all/race；临时数据库浏览器流程验收。
5. 同步权限/接口/依赖文档，检查差异并提交。重建 Docker 镜像，停写后在原卷内备份数据库，同端口/卷重启，核对健康及页面内容。

## 依赖审视

catalog 使用 transactions、identity、members、store、commands 五项，各自承担事务、身份、负责人资格、事实存储、命令生命周期；保留显式边界。workspace 新增 catalog.ProjectReader 的只读接口，仅校验任务归属；API 新增 catalog 应用接口作为协议适配，不内置业务规则。后续不通过扩展这些接口承载运行时、自动化或上下文管理。
