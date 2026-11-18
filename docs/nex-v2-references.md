> M01（2026-11-18）：本文为参考调研，不作为当前插件装配要求；涉及旧 core/work、D01/D02、flow 或 T 编号的映射均为历史。实际边界及顺序以[架构](architecture.md)和[清单](../todo.md)为准，代码复用仍为最低优先级。

# Nex V2 能力参考与 Taskloom 查缺补漏

整理日期：2026 年 11 月 17 日。来源：本地 Nex V2 `nex` 仓库，核对提交 `8895931a4`。本次是静态源码阅读，没有运行测试、压测或验证部署行为。

## 定位与优先级

本记录用于发现 Taskloom 遗漏的业务条件、异常处理和用户交互，并吸取 Nex V2 已有实现的优点。Taskloom 的职责与公共接口由自身需求决定，不以迁移 Nex V2 代码、采用其数据模型或替换底座为目标。

优先级按以下顺序理解：

1. 高：核对会影响正确性、安全及证据稳定性的缺口，补入 Taskloom 设计和验证场景。
2. 中：参考交互和可维护性实践，按实际场景补充，不提前增加插件或配置。
3. 最低：代码抽取、迁移或引入组件，仅当有独立收益且适配成本合理时再评估。没有具体收益时不安排代码复用。

下表“参考优先级”是查缺补漏顺序，绝不是搬运代码的先后。所有条目的代码复用优先级均为最低、可不采用。Taskloom 已确定复用的 AgentKit/PluginKit 装配机制、已有管理平台及 Agent 层，不因本调研策略而改变。

本机源码根目录为 `/Users/rock.zheng/code/newnexv2/nex`，下表链接用于开发定位，不是跨机器依赖。旧 `daemon/` 已在 NEX 仓库规则中标记不再维护，不作为实现来源；未将旁支 worktree 中的性能修改视为当前主仓实现。部分 CodeGraph 索引已过期，表中结论以当时直接读取的当前文件为依据。

## 能力对照

| 能力与优点 | Taskloom 对应位置 | 源码入口 | 应检查或适配的边界 | 参考优先级 |
| --- | --- | --- | --- | --- |
| 事务内幂等回执：按租户、调用者、操作和键隔离，同键不同内容冲突 | `storage/sql` 的幂等仓储、`runtime/command`、`delivery/reply` 等正式写入 | [idempotency.go](/Users/rock.zheng/code/newnexv2/nex/internal/idempotency/idempotency.go:31) | 保持可信身份、请求摘要和原子提交；重试读取旧回执前仍需处理当前认证与数据读取权限，不能把“旧结果回放”当成撤销权限豁免；不带入多套历史摘要规则 | 高 |
| 业务事件与状态同事务；企业内序号按提交顺序可见 | SQL 事件端口与普通 worker、`storage/sql`、所有正式业务写入 | [event.go](/Users/rock.zheng/code/newnexv2/nex/internal/event/event.go:94) | 参考计数行锁解决晚提交事件漏读；它会串行化同企业的编号写入，需按实际负载判断；Taskloom 自定事件类型、版本与负载，不复制 NEX 的 Types/Pushed 全局白名单 | 高 |
| 通知只唤醒，事实从数据库补读；断线、丢失增量后重同步 | 内联 task-events 模块的 feed/WS 与普通 worker | [hub.go](/Users/rock.zheng/code/newnexv2/nex/internal/event/hub.go:34)、[stream.go](/Users/rock.zheng/code/newnexv2/nex/internal/conversation/stream.go:3) | NEX 浏览器流是 SSE，不能当成现成通用 WebSocket；保留 Taskloom 当前权限复查、动态订阅、游标过期及 HTTP owner 生命周期，去掉运行队列/执行会话专有类型 | 高 |
| 可信入口建立不可任意改写的操作者身份 | `identity/existing`、HTTP 认证及全部 API 模块 | [actor.go](/Users/rock.zheng/code/newnexv2/nex/internal/auth/actor.go:5) | 学习身份来源和日志不携带凭据值；不绑定 NEX 数据库凭据结构、ChatAutomon 或飞书登录；人、代理 Buddy、实例分别审计 | 高 |
| 授权按当前事实判断，写入期间保护授权依据 | `authorization/default`、确认及领域写入 | [policy.go](/Users/rock.zheng/code/newnexv2/nex/internal/policy/policy.go) | 检查撤销与提交竞态、跨任务历史/附件权限、初始化管理员及恢复路径；不引入组织架构、AB Center 和 Automon 特例；不在 SQL 锁内等待远端身份服务 | 高 |
| 文件版本、大小/摘要校验、重复发布与流式读取 | artifacts 材料模块、`storage/blob`、会话材料保留引用 | [artifact.go](/Users/rock.zheng/code/newnexv2/nex/internal/artifact/artifact.go:48)、[upload.go](/Users/rock.zheng/code/newnexv2/nex/internal/artifact/upload.go:119) | Taskloom 的文件归材料插件，正文归会话；NEX 的产物库和业务库分离、Run/Attempt 发布模型不能直接带入；核对 SQL/S3 补偿、引用保留与合规删除 | 高 |
| 对象存储接口、临时文件原子发布及完整性检查 | `blob.Store`、本地测试后端及 `storage/blob` 的行为验证 | [objectstore.go](/Users/rock.zheng/code/newnexv2/nex/internal/artifactstore/objectstore.go:21)、[retention.go](/Users/rock.zheng/code/newnexv2/nex/internal/artifactstore/retention.go) | 已核对具体实现是 FileStore，不据此宣称已有 S3 后端；文件重命名原子性不能直接套到 S3，不能照搬构造时清理的生命周期 | 高 |
| 验收检查统一覆盖多个入口、代理身份、自验收及子任务未完成 | 人工验收状态、事实审核状态、完成状态内部检查 | [review.go](/Users/rock.zheng/code/newnexv2/nex/internal/workitem/review.go:12) | 保留 Taskloom flow 唯一状态写入者，验收绑定回复修订；不复制 closeTx 完成入口。明确人的负责人、执行 Buddy、实际操作者的自验收口径 | 高 |
| 事务、解码、错误和幂等请求头辅助逻辑 | `runtime/*`、SQL 实现及 HTTP 内普通路由包 | [pg.go](/Users/rock.zheng/code/newnexv2/nex/internal/pg/pg.go:17)、[httpjson.go](/Users/rock.zheng/code/newnexv2/nex/internal/httpjson/httpjson.go)、[apierror.go](/Users/rock.zheng/code/newnexv2/nex/internal/apierror/apierror.go) | 重点查统一错误与事务回滚；NEX 的死锁重试要求回调只写 SQL，不能重放带 S3/外部副作用的回调。工具函数无需一律配置成插件 | 高 |
| 阶段稳定标识、顺序、删除在用阶段校验、子任务门槛 | `task/flow`、状态插件、plan 插件 | [stages.go](/Users/rock.zheng/code/newnexv2/nex/internal/workitem/stages.go:16) | NEX 阶段属于项目且子卡无阶段，Taskloom 每个任务绑定版本化状态链；借鉴迁移/在用校验，不照搬看板列与状态映射 | 中 |
| 卡片详情聚合、可用操作、消息与系统事件时间线、附件入口 | 内联 api/query、任务/会话 API、后续前端 | [carddetail.go](/Users/rock.zheng/code/newnexv2/nex/internal/workitem/carddetail.go:17)、[CardTimeline.tsx](/Users/rock.zheng/code/newnexv2/nex/web/src/components/board/CardTimeline.tsx)、[cardTimeline.ts](/Users/rock.zheng/code/newnexv2/nex/web/src/lib/cardTimeline.ts) | 借鉴信息组织及禁止操作原因；过滤跨任务数据，不引入 NEX 运行控制、机器目录、Lark 等页面耦合；客户端提示不替代写入检查 | 中 |
| 新信息继续工作、提及和消息来源影响响应对象 | 已有 Agent 接入端的 steer；任务系统只提供事件与共用 API | [wake.go](/Users/rock.zheng/code/newnexv2/nex/internal/workitem/wake.go:95)、[本地唤醒](/Users/rock.zheng/code/newnexv2/nex/internal/localexec/wake.go:13) | 只吸取去重、自回复抑制、断线及控制信号语义；不把服务端派发/唤醒链引入 Taskloom。机器专用 WebSocket 的认证/Origin 规则不直接用于浏览器共用入口 | 中 |

## 不照搬的差异，也是检查清单

| Nex V2 实现或约束 | 对 Taskloom 的提醒 |
| --- | --- |
| [运行结束复制最后回答为卡片评论](/Users/rock.zheng/code/newnexv2/nex/internal/workitem/turnend.go:19) | 默认交付仍为 SubmitReply 原子创建/引用消息修订；不按运行结束推断交付，不复制第二份正文，不把短语 done 当作完成判据 |
| [评论编辑原地更新，已 steer 的评论禁止修改](/Users/rock.zheng/code/newnexv2/nex/internal/workitem/commentedit.go:108) | 消息修订不可变，编辑不改变已提交结果/验收依据；Agent 处理编辑事件时知道新旧版本，而非撤销已发生的外部动作 |
| 工单、项目看板、Run/Attempt、Automon 和 IM 相互关联 | 不迁移整个 workitem 或运行调度；按 Taskloom 的领域与 Deps 隔离，保留负责人为人、执行者为 Buddy 的约定 |
| internal 包及专有表结构 | 不能直接跨 Go 模块导入 internal 包；真有复用收益时再评估共享包或最小代码迁移，不先改变插件接口去迎合旧代码 |
| NEX 运行结果也可发布成 Artifact | Taskloom 正文以会话为唯一来源，S3 存文件；不为普通回复增加另一套产物发布体系 |

## 应反馈到 Taskloom 的缺口

以下为原调研核对项，其中 T01—T05 的身份/保护/验收/公共写入及首版归属已在[核心契约](core-contracts.md)和[覆盖表](requirements-coverage.md)收敛；实现、测试及真实联调仍未完成。清单用于后续行为验证，不自动扩展范围：

- 公共写入的幂等仓储与事务归属、认证/授权和回执读取顺序是否明确。
- 事件提交顺序是否安全；序号锁争用、重试及锁顺序是否可解释。
- 退回/重开如何保护旧执行；工作轮次与状态 EntryID 是否清晰区分，避免普通推进使结果错误失效。
- 任务级验收策略与状态链、完成检查是否有唯一有效口径。
- 当前执行认领、续用、结束、失效与停止确认是否闭合；连接心跳是否被错误当作任务执行心跳。
- Hook 对确认、权限、停止、材料等事实变化的覆盖和低频补偿是否完整。
- 结果消息修订、材料版本保留、撤回不回退旧结果及外部链接可变性是否得到验证。
- 可用操作、异常原因、结果入口和等待项是否能从共用 API 完整读取；前端不硬编码流程状态。
- 项目组织、提醒/超时、系统事件时间线和历史恢复是否有明确承接方，或明确后置。

## 代码复用的最后一步

只有当上述检查发现明确缺口、现有 Taskloom/AgentKit 能力组合不能解决，而且已有实现确实降低总体成本时，才评估最小代码抽取。评估至少说明：源文件及版本、适配与依赖成本、许可/使用条件、事务与授权差异、需要保留的行为测试。没有验证过的实现不得标记为“可直接复用”。

本记录不决定迁移 NEX 数据、采用 NEX 数据库 schema 或部署 NEX 服务；也不改变已确认的 PluginKit、SQL/S3、共用 API、WebSocket、状态子插件、结果回复交付与两层配置边界。
