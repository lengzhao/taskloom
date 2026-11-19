# Commit 与结果交付

任务保存当前约定，commit 保存不可变协作事实，时间线只是读取视图。没有 Conversation、Message 或 Revision 层；正文只存于 commit。Submission 保存 TaskID、RoundID、Basis、ExecutionID、CommitID、提交者和撤回事实。人工验收 State 保存 Review，绑定精确 Submission/Commit、依据版本与验收人。普通评论、进度和主运行结束均不产生交付。

## Commit

Commit 包含稳定 ID、TaskID、可信作者及代理归属、类型、正文、可选 RefID、对应 BasisVersion、服务端时间及提交序号。引用只允许同租户同任务已有记录。合规删除作为后续独立受权能力处理，不伪造旧内容。

普通 comment/progress/question 不改变目标、标准、状态或当前结果，也不递增依据版本。正式依据变更、决定、提交结果和验收各有命名授权命令，在事务内追加相应 commit 并更新自身事实；客户端不能指定任意 kind 执行这些动作。TaskVersion、BasisVersion、RoundID、ExecutionID 各自保持语义，CommitID 不替代版本冲突、幂等或执行隔离。

`cap/commit` 只提供追加、精确读取与按提交序分页，没有编辑、覆盖或通用 Execute；对外查询仍检查当前任务权限，重复事件按 ID 去重。

## SubmitResult

命名命令接收新结果内容或已有精确 CommitID；M05 首版仅接受新文本。命令校验可信主体、当前权限、任务版本、轮次、依据及执行写资格。新内容在同事务追加结果 commit；已有引用必须属于同任务且作者/执行依据匹配。登记 Submission 并设置当前轮候选，再调用 flow 核对已到达步骤。

业务事实、commit、交付、步骤记录、审计、outbox 与回执原子提交。技术错误全部回滚；后续事实审核 wait/reject 保留合法结果并返回等待。完成不等待执行停止。

## 更正、撤回及历史

更正追加新 commit 引用旧 CommitID，旧 Submission/Review 不漂移；要成为新结果必须再次 SubmitResult，重建所有受影响步骤的当前证据。Withdraw 清空当前候选并保留历史，不恢复早先结果。旧验收点击、旧轮及旧执行不能覆盖当前事实。

正文不复制到交付或评审表。文件阶段（M07）由材料模块固定文件版本及引用；链接只固定 URL，不冒充快照。终态拒绝普通提交/撤回，须显式受权重开。首阶段仅文本，文件明确拒绝。相关约束见核心契约 C01/C06/C10—C13，实际实现见 [M05](m05-workflow.md)。
