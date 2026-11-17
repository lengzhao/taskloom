# 开源参考项目

整理日期：2026-11-17。

本文仅围绕任务系统设计查缺补漏：吸取已有项目在任务关系、会话、权限、验收、事件和版本方面的优点，检验 Taskloom 是否遗漏必要条件及异常场景。代码复用、引入组件和采用底座的优先级均为最低，不以寻找可搬运项目为研究目标，也不按调研对象的数据模型反向决定本项目接口。

软件分发与管理平台、Agent 执行层复用已有能力；已确认的 AgentKit/PluginKit 装配和两层配置方向保持不变。这里的“参考优先级”仅表示查缺补漏顺序，不是代码复用顺序。Nex V2 的本地源码对照另见[Nex V2 参考清单](nex-v2-references.md)。

下列外部项目能力、链接和许可沿用 2026-11-17 此前调研记录，本次只调整研究定位，没有重新联网核验或完成部署、压测和源码审计；实际采用前须核对所选版本、依赖及使用条件。记录不构成组件选型结论。

## Paperclip：任务协作模型参考

- 仓库：https://github.com/paperclipai/paperclip
- 文档：https://docs.paperclip.ing/
- 许可：MIT，采用前核对所选版本及依赖。
- 定位：用于检查任务分工、执行归属、验收及权限的设计完整性；代码复用优先级最低，不预设采用其底座。

### 参考能力

目标与父子任务关系、人与 Agent 分工、任务会话和材料、执行记录与原子 checkout、执行锁、可选审查和批准、退回修改、多组织身份和操作归属。

### 重要参考入口

- [任务 API](https://docs.paperclip.ing/reference/api/issues/)
- [任务使用说明](https://docs.paperclip.ing/guides/day-to-day/issues/)
- [执行策略与审查验收](https://docs.paperclip.ing/guides/power/execution-policy/)
- [权限参考](https://docs.paperclip.ing/administration/roles-and-permissions/)
- [服务端业务服务目录](https://github.com/paperclipai/paperclip/tree/master/server/src/services)
- [数据模型目录](https://github.com/paperclipai/paperclip/tree/master/packages/db/src/schema)

### 与 Taskloom 的差异

1. Paperclip 同时包含运行时、适配器和唤醒控制；Taskloom 只建设任务系统，软件分发和 Agent 层复用已有能力，应借鉴职责划分，避免将其运行时和唤醒控制带入本项目。
2. 文档描述标准信任 Agent 对可见任务存在默认开放的写操作；Taskloom 需要按职责分别授权创建、拆解、转交、目标修改和验收。
3. 文档修订和配置历史不等于完整任务树版本管理，任务树快照、交付依据及验收版本绑定仍需核验或自建。
4. 与已有个人电脑及云端 Buddy 的共用 HTTP API、WebSocket 推送、steer、离线重连和执行所有权对接需要验证；执行上下文迁移能力由已有 Agent 层负责。

### 查缺补漏问题

- 任务、权限与运行时的分工有哪些可取之处，Taskloom 是否有职责遗漏或隐式依赖？
- checkout、过期执行处理和结果提交的并发边界是否满足需求？
- 验收如何区分负责人、执行者、审查者，能否提示 Taskloom 补齐代理及自验收检查？
- 是否支持按任务树细粒度授权及撤销？
- 任务变更与事件记录能否支持一致的历史快照和版本差异？

## 其他参考

| 项目 | 仓库 | 参考部分 | 主要限制 |
| --- | --- | --- | --- |
| Huly | https://github.com/hcengineering/platform | 任务聊天、活动流、属性侧栏 | 整体部署较重，Buddy 协议需建设 |
| Beads | https://github.com/gastownhall/beads | Agent 任务图、依赖、原子领取、持久上下文 | 企业会话、权限和验收产品不完整 |
| OpenFGA | https://github.com/openfga/openfga | 关系授权、作用范围、条件与 Agent 绑定 | 不负责业务审批、任务状态和审计界面 |
| Dolt | https://github.com/dolthub/dolt | 数据版本、历史快照、差异、分支合并 | 不理解任务版本、验收及变更影响 |
| Zulip | https://github.com/zulip/zulip | 主题对话、Bot、消息事件同步 | 缺少任务树、正式交付和验收模型 |

### Huly

主仓库 EPL-2.0。优先借鉴同一个任务对象的聊天与结构化详情两种视图，不建议直接照搬整个平台。

- [任务活动与聊天](https://docs.huly.io/communication/chat-activity/)
- [自托管架构](https://github.com/hcengineering/huly-selfhost/blob/main/ARCHITECTURE_OVERVIEW.md)

### Beads

主仓库 MIT。当前基于 Dolt，参考依赖图、ready 查询、claim 和面向 Agent 的结构化输出，不把 Coding Agent 的 CLI 工作流直接作为产品交互。

### OpenFGA

主仓库 Apache-2.0。先借鉴授权建模、关系和撤销边界，不默认引入组件；“建议、确认、执行”的业务流程仍由任务系统管理。

- [Agent 任务授权模型](https://openfga.dev/docs/modeling/agents/task-based-authorization)

### Dolt

主仓库 Apache-2.0。SQL 事务提交默认不自动形成 Dolt 版本提交；业务版本边界仍需应用定义。先参考机制以检查历史与差异的完整性，不默认采用该存储；仅有明确缺口和收益时再作最低优先级的组件评估。

- [历史查询](https://www.dolthub.com/docs/sql-reference/version-control/querying-history/)

### Zulip

主仓库 Apache-2.0。参考按主题持续对话、Bot 和事件队列，任务模型和版本语义需要独立设计。

- [主题说明](https://zulip.com/help/introduction-to-topics)
- [事件队列](https://zulip.com/api/register-queue)

## 按 Taskloom 模块查缺补漏

| 调研对象 | Taskloom 对应能力 | 主要学习点与检查项 | 代码复用优先级 |
| --- | --- | --- | --- |
| Paperclip | task、plan、execution、acceptance、authorization | 分工、领取及过期执行、验收角色、权限边界 | 最低，有明确收益时才评估 |
| Huly | conversation、query、后续前端 | 会话与结构化详情一致展示、活动关联、未读及操作反馈 | 最低，有明确收益时才评估 |
| Beads | plan、execution、通用业务 API | 依赖满足、ready 判断、领取冲突、面向 Agent 的清晰查询 | 最低，有明确收益时才评估 |
| OpenFGA | authorization、identity 适配 | 关系授权、作用范围、条件、撤销及历史读取边界 | 最低，不预设引入组件 |
| Dolt | revision、SQL 版本语义 | 版本边界、历史快照、差异及恢复；不等同于回滚外部副作用 | 最低，不预设采用存储 |
| Zulip | conversation、task/feed、通知、WebSocket | 主题协作、消息事件、未读、重连与增量读取 | 最低，有明确收益时才评估 |
| Nex V2 | 事务/幂等、事件、身份授权、材料、验收及 UI | 已有业务边界和异常处理的完整性，具体源码与适配边界见[清单](nex-v2-references.md) | 最低，不迁移整个业务模块 |

调研按正在设计的模块和具体疑问开展，不再按“先选一个整体底座”的路线推进。先记录优点、映射 Taskloom 缺口及验证场景，再决定是否需要设计变更；组件引入或代码抽取放在最后。

此前候选 Kaneo、Vikunja 保留为 UI 和基础任务能力的补充观察对象，不作为选型或迁移优先项。尚未核验的能力不直接变成本项目需求或已完成能力。
