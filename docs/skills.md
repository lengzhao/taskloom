# Skill 交付控制面

skill 模块组负责 Agent Skill 从发布到交到宿主手里为止：发布不可变版本、发现、对确切版本在声明的宿主上做交付判定、提供原样字节、独立审核。它不执行 skill，不保存宿主选用了哪些 skill（消费方清单归宿主，例如 AgentKit 的 lock 文件），与任务领域互不依赖、不共享业务事务。装配见[覆盖示例](../examples/skills/skills.config.yaml)，接口见 [API](api.md#skill)，范围约束见 [AGENTS.md](../AGENTS.md#项目范围与资料入口)。

## 事实与推导

| 概念 | 存储 | 可变性 |
| --- | --- | --- |
| Capability：租户内唯一的语义名、拥有者、可见性（private/tenant）、协作者、归档 | SQL | 拥有者与管理员可改；名字与租户不可改。存储的每个写事务持有全局锁，读改写不需要版本号 |
| Release：严格递增的 semver 版本、摘要、大小、SKILL.md 声明、发布者、提交顺序 | SQL；字节在 `cap/blob`，键即 `release_ref` | 不可变，纠正只能发新版本 |
| Review：approved/rejected 加理由与审核人 | SQL | 只追加 |
| 检索向量（仅配置 embedder 时） | SQL，只存当前版本的，发布提交后写入 | 派生数据：新版本追加时删除旧向量，reindex 补齐缺失 |
| 当前版本、交付判定、trust、诊断 | 不存 | 当前版本是最后追加的版本；判定与 trust 由 Release、最新 Review 和宿主事实推导；诊断由不可变字节确定性重算，预检时给作者看 |

`release_ref` 是不透明的随机 ID，也是字节键；宿主落地后用 `X-Content-SHA256` 自行校验摘要。

## 发布

1. 认证后读入归档（默认上限 10 MiB），由 `domain/skillpkg` 确定性检查：
   - 拦截：不是 zip、条目超量或解压超限、路径穿越、链接与特殊文件、重复条目、不在唯一根目录下、缺 `SKILL.md`、非 UTF-8、frontmatter 缺失或 `name`/`description` 不合规、`metadata.version` 不是 semver、根目录名不等于 `name`。
   - 只提示：缺 `when_to_use`、`not_for`、`success_criteria`、`examples`；联网、shell、破坏性命令、凭据相关的风险标记。正则标记不证明安全与否。
2. 已有同名 capability 走 `skill.release`（拥有者、协作者、管理员），否则走 `skill.create`（任一有效成员）；先写字节，再在命令事务内创建 capability（首次）并追加版本。
3. 同版本同字节返回已有版本；同版本不同字节或更低版本拒绝（`version-not-increasing`）。失败、重放和相同重试都删除新写的字节，补偿失败只留下不可达字节。
4. 配置了 embedder 时，提交后在事务外调用 embedding（`name + description`），失败只让该版本暂不参与语义检索；管理员 `reindex` 补齐当前版本缺失的向量；它只经命令生命周期鉴权与审计，回执不存内容，重放时重新计算缺失项。未配置 embedder 时不生成向量，`reindex` 返回不可用。

预检与发布走同一套检查，只读，不留回执。

## 发现

`GET /skills/capabilities` 的 `match` 决定方式，权限与可见性先过滤，分数只在单次响应内可比。部署在 `skill-query` 的 `modes` 中选择启用哪些方式；留空时启用 name、lexical、bm25，接了 embedder 再加 semantic；未启用的方式返回 400，列出 semantic 却未接 embedder 则启动失败。

| match | 做法 |
| --- | --- |
| 空 | 按名字浏览，不带查询 |
| `name`（模糊） | 前缀命中为 1，否则 trigram 相似度 > 0.3 |
| `lexical`（关键字） | 名字精确/前缀/子串/trigram，其次描述包含或分词覆盖 |
| `bm25` | Okapi BM25（k1 1.2、b 0.75），语料为当前可见的版本，名字计两次；词按字母数字切分，中日韩连续字符切成二元组；按本次最高分归一到 1 |
| `semantic`（需 embedder） | 查询向量与当前版本向量的余弦；距离超过 `maxDistance`（默认 0.65）丢弃，文本含全部查询词加 `lexicalBonus`（默认 0.1）；查询 embedding 失败直接报错，不降级 |

除 semantic 外都不调用外部服务。打分在 Go 内存中完成（`domain/skillmatch`），SQLite、MySQL、PostgreSQL 行为一致，不依赖 FTS5 或 pgvector。两个默认参数是经验值，换模型时须用有代表性的相关与不相关查询重新标定。

## 交付判定

`POST /skills/capability-decisions` 对一个确切 `release_ref` 和宿主声明的事实（契约、能力、已连接应用）返回唯一状态与至多一个动作（`domain/skilldelivery`）：

| 条件（按序） | 状态 | 原因或动作 |
| --- | --- | --- |
| 契约不是 `agent-skill-host@1.x` | unavailable | `bridge-not-declared` |
| 缺 `agent-skills`、`filesystem`，或声明了外连域名却缺 `outbound-network` | unavailable | `host-affordance-missing`，列出缺项 |
| 最新审核为 rejected | blocked | `review-rejected` |
| `required_apps` 中有宿主未连接的应用 | action_required | `connect-apps` |
| 其余 | ready | `materialize-exact-artifact`（ref、摘要、大小） |

trust 为 approved、rejected 或 unreviewed；没有审核是否放行由宿主决定。

## 权限

由 `access/default` 的固定操作判断，见 [API 权限](api.md#权限)：读取与新建要求有效成员；发布新版本要求拥有者、协作者或人类管理员；改可见性、协作者、归档要求拥有者或人类管理员；审核与 reindex 要求人类管理员，审核人不能是该版本的发布者。读取时看不到、已归档和不存在一律 404。

## 未实现

宿主预设（待 AgentKit 同步工具成为消费方再加）、跨租户系统目录、只读授权、按组织可见、MCP Skills 扩展（SEP-2640）、`.well-known` 发现、网页发布与管理（工作台只提供浏览、按审核状态筛选、下载与管理员审核，见[工作台](api.md#工作台)）、Remote MCP producer；宿主侧同步工具在 AgentKit。
