# 工作空间与轻量项目

当前工作空间对应认证 Tenant，默认名称为租户标识；管理员在“空间设置”编辑名称和说明，并进入“用户与 Agent”。不提供跨空间账号、匿名租户目录或空间切换。

“项目”可创建项目，填写名称、目标说明和人类负责人；进入项目后可编辑、归档与恢复。有效成员共享项目目录，但不会因加入项目扩大任务访问权限。统计标为“可见任务”，完成、未完成、取消分别计算。项目没有另一套任务状态机。

任务可不属于项目。创建时选择项目，或由任务负责人/管理员在详情中移动归属。归档阻止新任务与移入，保留已有任务和历史。移动只更新任务版本，不改变依据版本、负责人、执行与流程。列表和看板共用搜索、项目和阶段筛选；看板只读展示阶段，不提供绕过流转的拖拽。

## 共用 HTTP API

- GET /v1/space、PATCH /v1/space：读取/更新当前空间，写入 Name、Description、ExpectedVersion。
- GET /v1/projects、POST /v1/projects：目录和创建，Name、Description、Owner；创建 ExpectedVersion 为 0。
- PATCH /v1/projects/{id}：Name、Description、Owner、Archived、ExpectedVersion；路径决定目标 ID。
- GET /v1/project-members：当前有效人类成员可读的候选成员目录，供项目负责人选择。
- POST /v1/tasks：可选 ProjectID。
- POST /v1/tasks/{id}/project：ProjectID、ExpectedVersion；空 ProjectID 移出项目。

写入需要 Idempotency-Key；租户由认证主体决定。新增 catalog/001 迁移只创建空间、项目表，旧任务 JSON 缺少 ProjectID 时按空处理。升级前备份，旧二进制不识别新迁移，回滚须使用升级前数据库。

验证包含真实 SQLite/PluginKit/HTTP 的权限、租户隔离、归档、版本冲突、幂等、撤权重放和移动不改变任务依据/阶段；页面验收使用独立临时库。MySQL/PostgreSQL 与外部 Agent 执行不在此次验收范围。

本轮验证：`scripts/check.sh all` 与 `race` 通过；浏览器在独立回环地址和临时 SQLite 库完成空间保存、项目创建、任务创建与详情、列表/看板、搜索空结果、归档目录、任务移出及统计更新、管理入口验收。浏览器错误日志为空。
