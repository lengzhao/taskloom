# 配置示例

运行时只有 [base.yaml](../base.yaml) 与 [config.yaml](../config.yaml) 两层，日常只维护 config。完整默认图由项目维护。

`flows/` 内文件是可复制到 config 的实例新增/覆盖片段，不作为第三层加载：

- [standard.config.yaml](flows/standard.config.yaml)：选择 base 已有的标准验收流程。
- [no-review.config.yaml](flows/no-review.config.yaml)：增加无需独立验收的流程并作为新任务默认值，同时保留旧标准流程。

片段并入 config 时，保留已有部署地址和来源配置；同 ID 字典递归合并，列表完整替换。详见[配置说明](../docs/configuration.md)。目前均为设计示例，插件及加载器尚未实现。
