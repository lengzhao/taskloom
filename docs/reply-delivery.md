# 结果交付：引用不可变 Commit

正文只保存在 commit；Submission 保存 TaskID、RoundID、Basis、ExecutionID/Generation、CommitID、提交者和撤回事实。人工验收 State 保存 Review，绑定精确 Submission/Commit、标准与策略版本。普通评论、进度和主运行结束均不产生交付。

## SubmitResult

命名命令接收新结果内容或已有精确 CommitID，校验可信主体、当前权限、任务版本、轮次、依据及执行代次。新内容在同事务追加结果 commit；已有引用必须属于同任务且作者/执行依据匹配，不得登记他人结果。登记 Submission 并设置当前轮候选，然后调用 flow 核对已到达步骤。

业务事实、commit、交付、步骤记录、审计、outbox 与回执原子提交。技术错误全部回滚；后续事实审核 wait/reject 保留合法结果，返回等待。提交时仍可收尾，完成必须等待安全退出。

## 更正、撤回及历史

Commit 无编辑/修订。更正内容追加新 commit 引用旧 CommitID；旧 Submission/Review 不漂移。要成为新结果必须再次 SubmitResult，重建所有受影响步骤的当前证据组合。Withdraw 清空当前候选并保留历史，不恢复早先结果。旧验收点击、旧轮及旧执行不能覆盖当前事实。

正文不复制到交付或评审表。文件阶段由材料模块固定文件版本及引用保留；链接只固定 URL，不冒充快照。终态拒绝普通提交/撤回改变证据，须显式受权重开。首阶段仅文本，未实现文件时明确拒绝。

该设计保留 C01/C06/C10—C13、重复登记、撤回并发及材料授权约束。当前尚无运行中的提交/验收用例，M05/M07 验证。
