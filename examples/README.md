# 示例

[automatic](flows/automatic.config.yaml)（无人工验收）是并入根 config.yaml 的流程覆盖示例，不作为第三层加载。任务策略与分期见[架构](../docs/architecture.md)。

[s3](storage/s3.config.yaml) 把文件字节存到 S3 兼容存储（替换 `blobs` 节点），凭据只从环境变量或实例角色读取，见[开发说明](../docs/development.md#文件存储)。

[skills](skills/skills.config.yaml) 加入 skill 交付控制面（发布、审核、检索、交付判定），默认不需要 embedding（name、lexical、bm25）；语义检索可选，需取消注释 embedder 并接 OpenAI 兼容端点，令牌只从 `tokenEnv` 指定的环境变量读取，见 [Skill 交付控制面](../docs/skills.md)。
