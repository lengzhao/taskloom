# 不可变任务 Commit

本设计替代“一任务一条 commit 时间线”和commit模型。任务保存当前约定，commit 保存协作事实，时间线只是读取视图。没有独立 Conversation、Message 或 Revision；正文只存于 commit。

Commit 包含稳定 ID、TaskID、可信作者及代理归属、类型、正文、可选 RefID、对应 BasisVersion、服务端时间及提交有序序号。更正追加新 commit 引用旧记录，旧内容不变；引用只允许同租户同任务已存在记录。合规删除作为后续独立受权能力处理，不伪造旧内容。

普通 comment/progress/question 不改变目标、标准、状态或当前结果。正式依据变更、决定、提交结果和验收各自有命名授权命令，在事务内追加相应 commit 并更新自身事实；客户端不能指定任意 kind 来执行这些动作。结果登记引用 CommitID、轮次、依据及执行代次；验收引用精确结果和策略版本。新 commit 不自动替换当前结果。

TaskVersion、BasisVersion、RoundID、EntryID、ExecutionID/Generation 保持各自语义；CommitID 取代 CommitID，不取代版本冲突、幂等或执行隔离。业务事实、commit、审计、outbox 和命令回执同事务提交。普通 commit 不机械递增任务依据版本。

`cap/task.Store` 只保存任务事实；`cap/commit.Log` 提供 Append/Get/List，没有编辑、覆盖或通用 Execute。SQL 内部实现统一事务及租户/任务隔离。对外查询仍必须检查当前任务权限，不能直接暴露仓储。列表按提交序号分页，重复事件由 ID 去重。

M04 已实现仓储、命名评论/依据命令、准备流程初始化、授权 API 和薄界面；结果/验收命令仍归 M05。Harness 不在本轮范围。
