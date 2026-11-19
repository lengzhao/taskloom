# 示例

根 base/config 与 [minimal/base.yaml](minimal/base.yaml)、[minimal/config.yaml](minimal/config.yaml) 均可直接运行；minimal 只有 HTTP health，只表示进程存活。每次运行只选择一对文件，不叠加成三层。

[automatic](flows/automatic.config.yaml)（无人工验收）和 [with-checkpoint](flows/with-checkpoint.config.yaml)（新增确认步骤与 API）是并入同一 config.yaml 的流程覆盖示例，均由 `plugins/workflow` 扩展测试真实构建。任务策略与分期见[架构](../docs/architecture.md)。
