# 示例

根 base/config 与 [minimal/base.yaml](minimal/base.yaml)、[minimal/config.yaml](minimal/config.yaml) 均为当前可运行 HTTP health 图，只表示进程存活。每次运行只选择一对文件，不叠加成三层。

[standard](flows/standard.config.yaml) 和 [no-review](flows/no-review.config.yaml) 旧流程片段已退役为注释空覆盖，暂不装配尚未实现的状态链，也不启用验收业务；实现后按新接口恢复真实示例。任务策略与分期见[架构](../docs/architecture.md)。
