# 后端架构评审（第三版）

> 状态：基于当前实现的代码评审，不是待实现设计稿
>
> 评审范围：Go 后端、SQLite 持久化、B 站与知识星球采集、通知 Outbox、AI 控制面与 Worker 边界、管理 API、媒体生命周期和进程生命周期
>
> 当前基线：`main@9e37e9f`（含 `refactor/backend-architecture-v10@1ce8d8e` 与后续 `feat/zsxq-session-login`，2026-08-12）
>
> 对照文档：[后端架构评审（第二版）](backend-architecture-review.md)

## 1. 结论

当前后端已经完成第二版评审中最紧迫的架构收口：v10 迁移删除旧内容事实表和旧投递表，B 站与知识星球围绕统一的 `sources/contents/comment_nodes/seen_items/outbox` 工作；零通知渠道不再阻止采集；dispatcher 直接消费唯一 Outbox；媒体删除开始使用持久化清理任务；知识星球动态和评论 worker 也已经纳入同一个生命周期等待。

因此，第二版文档中关于“两代事实模型和两套 Outbox 同时在线”“B 站零渠道停止采集”“知识星球并发设置未生效”和“知识星球评论 goroutine 未等待”的描述已经过期，不能继续作为当前问题清单。

当前技术路线仍然正确：一个 Go 管理进程、一个 SQLite、持久化工作流和可选的本机 Python AI Worker，符合项目规模。没有理由引入微服务、外部消息队列、独立数据库服务，或为了目录整齐而预先铺设空包。

尚未完成的主要不是“模块化单体长什么样”，而是若干仍会破坏业务语义的运行时问题：

1. **禁用渠道的到期投递会反复占据 dispatcher 批次**，在 backlog ≥ 50 且存在其他启用渠道时可能饿死投递。
2. **知识星球评论 `baseline_ready` 会在 incomplete/error 同步后被清回 false**，导致后续新评论按 baseline 归档并静默丢通知。
3. **`RequestGate` 在收到响应头后即释放并发额度**；媒体下载还会因 transport 类型断言失败绕过 Gate，并削弱超时与风控 pause 语义。
4. **运行时设置先持久化、再逐个更新进程组件**；Gate `Update` 还要 drain in-flight 请求，失败时形成数据库与运行态分裂。

边界问题仍然存在：`service.Engine`、`state.Store`、`web.Server` 职责偏宽，事件失效通知依赖调用者记忆，`model.Dynamic` 仍跨协议/归档/投递/HTTP。但这些是**维护成本**问题，不应排在上述正确性问题前面，也不应以“先建 `archive/`、`delivery/`、`accounts/` 空包再搬家”的方式推进。

推荐顺序：

```text
先修可调度性、评论 baseline、真实请求预算和设置一致性
                    ↓
只在改写对应用例时抽出薄 Admin/服务
                    ↓
需要时再拆 dispatcher / 登录，而不是一次性拆除 Engine
                    ↓
最后中立化 Outbox snapshot，并按磁盘/运维压力补保留与孤儿扫描
```

## 2. 当前系统的核心不变量

后续演进继续以业务不变量判断，而不是以目录数量或文件长度判断：

1. **每个业务事实只有一个权威持久化表示。**来源、内容、评论和投递不能维护第二份运行时事实。
2. **采集事实不依赖通知渠道。**零启用渠道只表示不创建 Outbox，不表示停止归档、seen、评论同步或水位推进。
3. **`archive + seen + delivery intent` 原子提交。**渠道选择必须在该事务内读取，collector 不能携带过期渠道快照。
4. **自动 dispatcher 只能看到可执行任务。**长期不可执行的任务（渠道禁用、永久失败后的 blocked 等）不得占用 due 批次；blocked 需要人工或显式恢复，不等于“仍应每秒被扫描”。
5. **评论 baseline 一旦建立就必须 sticky。**后续 incomplete 同步或瞬时错误只能记录错误/树不完整，不能重新打开 baseline 并吞掉新评论通知。
6. **配置写入必须具有确定的一致性语义。**API 成功返回前，进程内消费者使用同一快照；API 失败时不能留下“DB 已新、运行态部分旧”的静默分裂，或必须把该语义写成明确错误。
7. **配置项必须对应可验证的运行时约束。**request concurrency 必须约束定义范围内的完整请求生命周期，而不只是等待响应头；平台 pause 也必须作用于纳入该预算的请求。
8. **每个 goroutine 都由拥有它的组件等待退出。**上层返回后不能继续访问即将关闭的 Store、HTTP client 或事件总线。
9. **HTTP、ticker、GORM、外部协议和文件系统是边缘。**业务用例不能由 handler 或协议 client 临时拼装。
10. **Outbox payload 是创建时的不可变通知快照。**调度状态和归属使用结构化列，不能在发送时读取可变业务信息重建 payload。
11. **AI Worker 只是执行适配器。**job、依赖、重试、取消和通知语义继续由 Go 与 SQLite 所有。
12. **单进程假设成立时不做分布式队列设计。**当前 `deliveryLoop` 等待上一轮 `dispatchOnce` 完成后再进入下一 tick，不需要 claim/lease；只有未来改为多轮重叠调度或多实例时才引入。

## 3. 第二版评审落实状态

| 第二版问题或验收项 | 当前状态 | 判断 |
| --- | --- | --- |
| 两代内容事实模型同时在线 | 已完成 | v10 删除 `ups/dynamics/comments/seen_dynamics/seen_comments`，运行时只写统一事实表 |
| 两套 Outbox 与 `PromotePlatformOutbox` | 已完成 | 旧 `deliveries` 已删除，dispatcher 直接查询 `outbox` |
| B 站采集依赖启用渠道 | 已完成 | 采集循环不再因零渠道返回，事务内读取启用渠道 |
| `archive + seen + outbox` 原子提交 | 已完成 | B 站空间、聚合 feed、知识星球内容和评论均在 SQLite 事务内提交 |
| B 站全局请求并发 | 部分完成 | 已有平台级 Gate，但并发释放时机和媒体 transport 绕过仍需修正 |
| `ZSXQRequestConcurrency` 未生效 | 部分完成 | ZSXQ API client 已接入 Gate；附件请求因 safe transport 重建而绕过 |
| 知识星球评论 goroutine 未等待 | 已完成 | 动态与评论 loop 都由 collector 的 errgroup 等待 |
| 知识星球登录模型 | 已切换 | SMS 登录已替换为 session/cookie 导入；账号入口为 `zsxq.AccountManager` |
| Web 直接编排业务 | 部分完成 | 已提取 `sources.Admin`，账号、渠道、AI、查询和事件仍在 Web 编排 |
| `Engine` 是上帝对象 | 未完成 | 仍负责五类 worker、两类登录、投递、状态、事件和 sender 构造 |
| `Store` 是万能具体依赖 | 未完成 | 同一具体类型仍暴露认证、来源、内容、评论、Outbox、AI、审计、媒体清理等全部能力 |
| 共享模型跨越边界 | 部分完成 | AI job 已改为显式输入/结果；`Dynamic`、`Delivery`、JSON tag 与本地路径仍跨多个边界 |
| Outbox 全量扫描 | 已完成 | 调度、统计、来源删除和摘要分页均使用结构化列与索引 |
| 数据库与媒体缺少恢复协议 | 部分完成 | 已有临时文件、原子 move 和持久化删除任务；仍无孤儿核对与保留策略 |
| 事件发布依赖调用者记忆 | 未完成 | Web、Engine、AIEngine 和设置管理器继续直接组合并发布 Topic |
| HTTP response 可能包含秘密或本地路径 | 基本完成 | AI profile、渠道和附件已有显式 View；继续按资源推广即可 |

## 4. 已完成且应保留的架构能力

### 4.1 唯一事实模型和破坏性 v10 迁移

[`migrateV10`](../state/migrate_v10.go#L21) 在一个不可逆事务中完成旧数据转换，并删除：

- `auth_session`
- `deliveries`
- `comments`
- `dynamics`
- `seen_comments`
- `seen_dynamics`
- `comment_targets`
- `ups`

运行时的 `ListUPs`、`PutUP`、`Seen`、`RecordDynamics` 和评论目标操作已经变成统一表之上的 B 站适配视图，不再维护第二份表。这符合项目“不保留向后兼容”的原则。

旧数据库内容仍通过一次确定性迁移转换；迁移完成后旧路径立即删除，没有长期双读或双写。这个策略应继续保持。

对 B 站适配视图的取舍：

- **短期保留** `ListUPs` / `PutUP` / `RecordDynamics` 作为 bilibili 采集门面是可接受的。
- 它们不是第二份事实模型，而是统一表上的平台视图。
- 只有当 collector 改为直接使用 Source/Content API，且没有调用者时，才删除这些入口。

### 4.2 唯一结构化 Outbox

v10 Outbox 将 `platform/source_id/content_id/channel_id/state/attempts/next_at` 等调度与归属信息保存为列，并为到期查询、来源删除、内容关联、游标分页和渠道状态建立索引。

当前 dispatcher 直接调用 [`DueDeliveries`](../state/store.go#L907)，队列统计使用 [`DeliveryStats`](../state/store.go#L925)，管理 API 使用 [`QueryDeliverySummaries`](../state/store.go#L869)。这些路径不再为了统计或分页反序列化全量 payload。

来源删除也通过 `source_id` 删除 Outbox，不再扫描整个队列，见 [`deleteSource`](../state/platform.go#L304)。

当前投递状态只有：

- `pending`：可被 due 查询选中
- `blocked`：永久/配置/鉴权失败，需显式恢复

没有 `suspended` / `sending` / `completed`。成功投递直接删除行。

### 4.3 事务内读取渠道并创建投递意图

以下写入都在自己的 SQLite 事务内调用 `enabledChannelIDsTx`：

- B 站空间动态：[`RecordDynamics`](../state/store.go#L601)
- B 站聚合 feed：[`RecordFeedDynamics`](../state/follow.go#L156)
- 跨平台内容：[`ArchiveContentAndEnqueue`](../state/platform.go#L515)
- 评论树通知：[`SyncCommentTree`](../state/platform.go#L757)

collector 方法中旧的 `channelIDs` 参数已经不参与决策，这证明事务所有权已经正确回到持久化用例。下一步应删除这些未使用参数，而不是继续保留旧 API 形状。

### 4.4 采集与投递分离

B 站 `collectOnce` 和 `commentOnce` 不再因为没有启用渠道提前返回。零渠道期间仍会：

- 归档内容
- 写入 seen
- 推进动态 baseline 和 feed baseline
- 更新评论树、编辑和删除状态
- 更新同步目标

事务只是不创建 Outbox。知识星球链路采用相同语义。

### 4.5 生命周期与媒体删除恢复

知识星球 collector 使用两个由同一 errgroup 管理的独立 loop，`Run` 返回前会等待动态和评论工作结束，见 [`Collector.Run`](../zsxq/collector.go#L76)。

来源删除时，Store 会在同一数据库事务中记录附件清理任务，再删除来源事实；独立 `media.Cleaner` 幂等执行文件删除并持久化重试。文件下载也使用临时文件和最终 move，避免把不完整文件暴露为正式资产。

这已经建立了恢复协议的主体，不应退回到 Web handler 直接删除目录。

### 4.6 AI 控制面保持在 Go 与 SQLite

AI job 输入和结果已经从 `any` 改为显式的 transcription/summary 类型；job 配置和目标渠道在创建时密封快照，条件更新继续保护 claim、完成、失败和取消状态转换。Python Worker 只执行外部工具和模型调用，不拥有持久业务状态。

### 4.7 知识星球登录已收口为 session 导入

当前入口是 [`zsxq.AccountManager`](../zsxq/account.go)，支持 cookie/session 导入与来源同步，不再以 SMS/验证码登录为主路径。管理 API 与 DTO 不应再按旧短信登录模型设计。

## 5. 当前主要问题

### 5.1 P1 / 条件 P0：禁用渠道可能造成 Outbox 队头批次饥饿

[`dispatchOnce`](../service/engine.go#L1334) 每秒按 `next_at` 读取最早的 50 条 pending 投递。发送前如果发现渠道已禁用，[`deliver`](../service/engine.go#L1395) 直接返回 `changed=false`，不修改投递状态、`next_at` 或其他可调度字段。

直接后果是：

- 同一批禁用渠道任务每秒被重新读取
- 达到 50 条后可能长期占满整个批次
- 其他启用渠道的新任务即使已经到期，也可能得不到调度
- 队列深度和最老年龄持续报警，但系统无法自行前进
- 渠道也无法删除，因为 [`DeleteChannel`](../state/store.go#L493) 在存在任何关联 Outbox 时拒绝删除

触发条件比“禁用渠道”更窄：

1. 某渠道被禁用
2. 该渠道仍有到期且 `next_at` 最靠前的 pending
3. 这类任务数量 ≥ 50
4. 同时存在其他启用渠道的到期任务

单渠道、空 backlog 或 backlog < 50 时影响有限。因此默认按 **P1** 处理；只有多渠道 + 大 backlog + 频繁禁用是真实运维模式时，才升为条件 P0。

现有测试“disabled channel leaves delivery alone”把“静默跳过且保持 pending”固定为预期，但它没有覆盖超过批次大小时的跨渠道公平性。

根因不在 dispatcher 少写一个 if，而在**渠道启停与 outbox 生命周期脱节**：

- [`saveChannel`](../web/ws.go#L386) / [`PutChannel`](../state/store.go#L406) 只更新 `channels.enabled`
- 禁用时不迁移 outbox
- 启用时 [`UnblockChannel`](../state/store.go#L990) 只恢复 `blocked`，不处理“因禁用而停住的 pending”

需要显式选择渠道禁用语义。推荐最小可前进模型：

```text
enabled channel   -> pending -> sending outcome
disable channel   -> 将关联 pending 移出 due 集合
enable channel    -> 恢复为可调度 pending
delete channel    -> 无关联任务时允许；或提供明确的 discard 删除
```

两种足够短的实现都可以：

1. **状态方案**：增加 `suspended`；禁用/启用与状态迁移同事务；`DueDeliveries` 只读 `pending`
2. **停车方案**：禁用时把关联 pending 的 `next_at` 推到远期或等价不可达点；启用时恢复 `next_at=now`

不建议继续保留“仍是 pending，但 dispatcher 静默跳过”的隐式暂停。

配套必须一起定：

- `DeleteChannel` 是拒绝、要求先清空，还是显式 discard
- backlog 的 oldest 统计不能继续把 disabled/blocked 永久任务算进“活队列年龄”
- 渠道更新后的 `UnblockChannel` 应避免“只改了名字也重放全部 blocked 风暴”；至少文档化，最好限定到配置/密钥实质变化

### 5.2 P0 / P1：知识星球评论 baseline 非 sticky，可能静默丢通知

B 站评论同步在 [`updateCommentSyncTargetTx`](../state/platform.go#L949) 中传入非 nil `target`，并强制 `BaselineReady = true`。
知识星球路径则传入 `target == nil`：

```text
zsxq collector
  -> CommentSyncState(...)
  -> SyncCommentTree(..., complete, !baselineReady, ..., nil)
  -> updateCommentSyncTargetTx(..., complete, ..., nil)
  -> BaselineReady = complete
```

因此：

1. 首次完整同步后 `baseline_ready=1`
2. 后续任一 incomplete 分页/部分抓取把 `complete=false`
3. 同一事务把 `baseline_ready` 写回 `0`
4. 下一轮 `!baselineReady == true`，新评论按 baseline 归档，不创建通知
5. 即使之后再次 complete，reset 窗口内首次出现的评论已经永久丢失通知

错误路径同样危险：`PutCommentSyncState(..., false, ..., err)` 也会把 baseline 清掉。

这与产品语义冲突：baseline 表示“首次成功同步之前的历史不通知”，不是“任意一次不完整同步后重新进入历史模式”。

最小修复：

- 一旦 `baseline_ready=true`，incomplete 或瞬时错误只更新 `last_error` / `tree_incomplete` / `last_synced_at`
- 不得把 baseline 清回 false
- 只有首次从 false 到 true 的成功同步能建立 baseline
- 增加回归测试：baselined 之后的 incomplete 同步，新 owner 评论仍必须通知

### 5.3 P1：RequestGate 没有约束完整请求生命周期

[`Gate.RoundTrip`](../internal/requestgate/gate.go#L42) 在进入底层 transport 前获取额度，但函数返回响应头时就执行 `defer g.release()`。响应 Body 可能仍在网络读取，此时另一个请求已经可以获得额度。

因此当前 concurrency 的精确定义是“同时等待响应头的 RoundTrip 数量”，不是“同时在途的完整 HTTP 请求数量”。大响应、流式响应或服务端慢速 Body 都能使真实连接并发超过配置值。

Gate 已经用 `cancelBody` 将超时取消绑定到 Body.Close；并发额度也应采用相同所有权：

- RoundTrip 失败时立即 release
- RoundTrip 成功时把 release 包装进响应 Body
- Body.Close 只执行一次 release
- 调用者必须关闭 Body，现有 client 路径继续遵守该规则

需要增加一个确定性测试：底层 transport 立即返回响应头，但响应体阻塞；在前一个 Body 关闭前，新请求不能进入底层 transport。

### 5.4 P1：媒体下载会绕过平台 RequestGate，并削弱 timeout / pause / 存活语义

应用装配时，B 站和知识星球 API client 都使用 Gate 包装 transport。但媒体下载器为执行 SSRF 安全拨号，会在以下位置重新构造 `http.Transport`：

- B 站媒体：[`redirectSafeClient`](../media/media.go#L195)
- 知识星球附件：[`safeClient`](../media/asset.go#L171)

传入 client 的 transport 是 `*requestgate.Gate`，不是 `*http.Transport`，因此类型断言失败，下载器退回克隆默认 transport。结果是媒体请求不会经过原平台 Gate。

这不只是“并发计数不准”：

- 平台 `PauseUntil` 对媒体失效，风控暂停时附件下载仍可能继续打上游
- 媒体路径的整体 timeout 语义变模糊
- 知识星球 [`EnsureAttachments`](../media/asset.go#L40) 持有进程级互斥，并在锁内对整棵平台媒体目录做 `directorySize`，再同步下载；慢 CDN 或大附件会卡住 collector 本地化

应先明确预算语义：

- 默认：媒体请求属于对应平台预算；安全 Dial/redirect policy 作为底层 transport，Gate 包在最外层
- 只有观测证明采集延迟不可接受时，再拆出明确命名的媒体预算/超时

最短修法是装配辅助函数，而不是 `PlatformClient`：

```text
safeTransport = SSRF dial + redirect policy
platformTransport = Gate(safeTransport)
API client / media downloader 都使用 platformTransport
```

同时应：

- 给媒体请求明确 timeout
- 缩小附件本地化锁范围，避免“扫整树 + 下载”都在同一把全局锁里

### 5.5 P1：运行时设置更新可能产生持久化与运行态分裂

[`runtimeSettingsManager.UpdateSettings`](../app/runtime_settings.go#L43) 当前按以下顺序执行：

1. 持久化新设置
2. 修改日志级别
3. 等待并更新 B 站 Gate
4. 等待并更新知识星球 Gate
5. 更新 Engine 内存快照
6. 发布设置事件

如果任一 Gate 因 in-flight 请求未能在 30 秒内排空，API 返回失败，但数据库已经保存新值，日志或前一个 Gate 也可能已经更新。此时同一个进程中可能同时存在：

- Store 中的新设置
- Engine 中的旧设置
- 已更新的 B 站 Gate
- 未更新的知识星球 Gate
- 已变化的日志级别

重启后，进程又会突然全部采用数据库中的新值。这是配置控制面的 split-brain，不只是错误提示问题。

注意：风险 pause 本身不占用 Gate `active`；真正拖住 `Update` drain 的是 in-flight 请求。若并发额度改为绑定 Body.Close，drain 只会更慢。因此 5.3 与 5.5 应一起改 Gate 更新语义。

明确取舍后的最小提交模型：

1. 所有输入先完成纯校验和 AI 配置不变量校验
2. Gate 更新改为原子配置快照：新请求读新快照，已有请求自然完成，不再 stop-the-world drain
3. 进程内消费者先切换到同一新快照，或采用“全部切换成功后才持久化”
4. 成功返回表示进程内已一致；失败表示未切换，或返回明确的“已持久化但未完全应用，请重试/重启”
5. 不通过失败后逐项补偿回滚来堆状态机

需要同步修改设计文档中“Gate 热更新先停 admission 再 drain”的旧表述。

### 5.6 P2：Web 仍然承担大部分应用服务职责

`sources.Admin` 已经让来源创建、更新和删除退出 handler，这是正确方向。但 [`web.Server`](../web/server.go#L48) 仍直接依赖：

- `*service.Engine`
- `*service.AIEngine`
- `*zsxq.AccountManager`
- `*state.Store`
- `*service.EventBus`

Web handler 仍然直接管理：

- B 站和知识星球登录、登出和来源同步
- 渠道保存、禁用、删除、测试、Microsoft OAuth 和投递重试
- AI profile、prompt、job 提交、取消、重试和 secret 保留
- 内容、评论树、附件和投递查询
- Topic 组合与失效事件发布

这些行为不是 HTTP 职责。但对本项目，正确反应不是先铺 `accounts/`、`delivery/`、`aijobs/`、`history/` 包树，而是：

- 在改写渠道/投递时抽出薄的 `delivery` 写用例
- 在改写 AI 管理时抽出薄的 `aijobs` 写用例
- 读查询可以继续直接打 Store
- 账号入口可继续组合 `Engine` 登录能力与 `zsxq.AccountManager`，不必为对称而造 `accounts.Service`

不要增加通用 `application`、`ports`、`repository` 或 controller/service/repository 分层。

### 5.7 P2：`service.Engine` 仍是多个业务能力的共同所有者

当前 `Engine` 仍同时拥有：

- B 站动态、评论、关系和会话验证 loop
- B 站二维码登录
- Microsoft OAuth
- delivery dispatcher 与通知 sender 构造
- 风控暂停、运行状态、指标和事件发布

`Run` 内部启动五个长期 worker，但这些 worker 共享整个 Engine 的全部依赖和锁。文件大约 2.1k 行，维护成本真实存在。

拆分目标不是把 `Engine` 改名为 `Runner`，也不是立刻建成：

```text
bilibili.DynamicWorker
bilibili.CommentWorker
bilibili.RelationWorker
bilibili.SessionWorker
delivery.Dispatcher
```

因为 collect/comment/relation/session 仍然共享 session、risk pause、settings 和 bilibili client。过早拆成多个包只会把共享运行时再做成 glue。

更合理的粒度：

1. 先抽 `delivery.Dispatcher`，它与 B 站协议无关
2. 再抽登录 / Microsoft OAuth coordinator
3. 采集相关 loop 可暂时留在同一个 bilibili runtime / Engine
4. 只有出现真实独立演化需求时，再拆 worker

验收不应写死“必须删除 `service.Engine`”。

### 5.8 P2：`state.Store` 仍是全系统万能具体依赖

Store 当前在多个文件中暴露约 100+ 方法，覆盖：

- 管理员认证和运行设置
- 平台账号、来源和 B 站关系同步
- 内容、评论树、seen 和同步目标
- 渠道与 Outbox
- AI profile、prompt 和 job 状态机
- 审计
- 媒体清理任务

底层继续使用同一个 SQLite 连接是正确的；问题是消费者普遍获得全部能力。例如 Engine 和 Web 都直接依赖 `*state.Store`。

正确做法是**随真实消费者边界**定义最小接口，如现有 `sources.Repository` 和 `zsxq` collector store。
错误做法是预先发明：

```text
SourceReader + DynamicArchive + BiliFeedState
CommentTargetReader + CommentArchive
DeliveryQueue + ChannelReader
...
```

这些接口不用于模拟多数据库实现，只用于限制依赖；因此没有消费者时不应先存在。

### 5.9 P2：共享模型仍跨越协议、归档、投递和 HTTP

`model.Dynamic` 当前同时承担：

- B 站协议解析结果
- 媒体下载输入
- B 站归档转换输入
- Outbox 内容快照
- 通知 renderer 输入
- WebSocket delivery preview 来源
- 系统告警载荷

统一事实表已经使用 `model.Content`，但 Outbox 仍通过 [`contentDeliverySnapshotTx`](../state/platform.go#L1005) 把内容重新组装为 `model.Dynamic`。因此持久化层仍需要理解 B 站命名，例如 `UID`、`UPName` 和 `DynamicID`，知识星球内容也被转换成 Dynamic 才能投递。

另外，系统告警走：

```text
enqueueSystem -> RecordDynamics("system", Type=SYSTEM) -> 普通 outbox
```

这会污染内容归档、和业务通知抢队列，并继续加固 Dynamic 万能载荷。短期可保留，但模型收口时应把它移出“假装成 UP 动态”的路径。

增量目标，而不是大爆炸：

1. Outbox / notify 使用平台中立 content/comment/AI snapshot
2. bilibili 解析类型继续留在 `bilibili`
3. HTTP 继续用显式 DTO
4. 系统告警改为独立通知路径或独立 kind

### 5.10 P2：媒体恢复协议缺少孤儿核对和长期保留

当前已解决“来源删除提交后直接删目录失败且不可恢复”的主要问题，但仍存在两种不一致：

1. 文件下载并 move 成功，随后内容归档事务失败，磁盘留下没有数据库归属的文件
2. 数据库保存 `local_path`，但文件被人工删除、卷损坏或清理异常，API 只能在读取时发现缺失

下一步不是扩大 SQLite 事务，而是在磁盘或 DB 体积成为真实压力时增加周期性、幂等的核对任务：

- 从数据库建立所有受管 `local_path` 集合
- 扫描受控媒体根目录
- 无数据库归属的文件进入延迟清理
- 数据库引用但文件缺失时清空本地状态或记录可见错误
- 临时文件按年龄清理
- 任何路径都继续经过 `media.Resolve` 和 symlink 检查

孤儿扫描不阻塞前面的正确性工作。对单机长期运行，Outbox / AI job / content / 媒体保留策略可能比再拆一个包更有实际价值。

### 5.11 P2：事件发布仍依赖调用者记忆

Web、Engine、AIEngine 和运行时设置管理器继续直接调用 `EventBus.Publish`，Topic 组合散落在各处。当前 EventBus 只是读模型失效提示，WebSocket 会通过快照恢复，所以不需要升级为持久化领域事件。

当写用例被抽出时，让该用例在成功提交后发布一次即可。返回一个本地 `Topic` 位图或极小的 `ChangeSet` 足够。不要单独做事件框架项目。

### 5.12 P2：v10 后仍保留已经删除架构的代码形状

v10 已删除 `ups` 和 `auth_session` 表，但 `state/models.go` 仍保留 `upRow`、`authSessionRow`、`tableAuthSession` 和相关转换；多个方法仍保留未使用的 `_ []string` 渠道参数；`ListDeliveries` 和 `QueryDeliveries` 等完整 payload 查询在生产代码中已经没有调用者。

项目明确不保留向后兼容，这些残留应直接删除：

- 删除只服务旧表的 row 和转换
- 删除已经无效的 collector 渠道参数
- 删除没有生产调用者的旧查询入口
- 将迁移专用 legacy 类型留在 `migrate_v10.go` 内，不进入当前运行时模型

这项清理规模小，应与下一次相关修改一起完成。

### 5.13 P2：WebSocket 关闭 goroutine 不受生命周期等待

`closeTokenConnections` 和 `closeAllConnections` 会为每个连接启动一个未等待的 goroutine 执行 `connection.Close`。它们通常很快结束，但仍违反“启动者负责等待”的规则，也使 Server 返回不代表全部关闭工作已经结束。

连接数量受管理台规模限制，不需要并发关闭。最小实现是直接顺序关闭。

### 5.14 P2：运维信号不足以暴露上述正确性 bug

当前容易误判：

| 缺口 | 后果 |
| --- | --- |
| 没有 disabled skip 计数 | 渠道饥饿只表现为 pending backlog |
| `Oldest` 含 blocked/disabled 永久任务 | 假积压告警 |
| 没有 in-flight delivery 可视化 | 慢发送与队列深度不好区分 |
| 没有 incomplete comment tree / baseline 异常计数 | ZSXQ 静默丢通知不可见 |
| ZSXQ 媒体下载耗时/失败不够一等公民 | collector 卡住只能看到“慢” |

这些指标成本低，应随 5.1–5.4 一起补，不必等“可观测性阶段”。

### 5.15 非问题：当前单进程 Outbox 不需要 claim/lease

`DueDeliveries` 确实没有 claim，也没有 `sending` 状态。但当前 `deliveryLoop` 的结构是：

```text
tick -> dispatchOnce -> 并发发送 -> g.Wait() 返回 -> 下一轮 select
```

同进程不会在上一轮发送未完成时再次拉取同一批任务。`MaxOpenConns=1` 只串行化 SQLite 写，不引入双发。

仍存在的至少一次语义：

- 发送成功后 `CompleteDelivery` 失败，会再次投递
- 进程崩溃发生在外部通知已成功、本地尚未删行时，会再次投递

这是 outbox 常见权衡，不是当前 P0。
只有未来改为“多轮重叠 dispatch”或“多实例消费”时，才需要 claim / `next_at` park / lease。

## 6. 目标形态

目标仍然是按业务能力组织的模块化单体，但**目录不是目标本身**。

当前应保持的形状：

```text
cmd/app                 composition root 与进程生命周期
web                     HTTP / WebSocket adapter
bilibili                协议 client；采集 runtime 可暂留 service 或逐步迁入
zsxq                    协议 client、AccountManager、collector
sources                 已存在的来源写用例
state                   唯一 SQLite 适配器
notify                  渠道 adapter
media                   下载、清理；后续可加孤儿扫描
service                 现阶段仍承载 Engine / AIEngine / EventBus / metrics
```

允许在改写用例时自然长出薄服务，例如：

- 渠道/投递写路径旁的 delivery admin
- AI 管理写路径旁的 aijobs admin

不允许：

- 预先创建并填充空的 `archive/`、`accounts/`、`history/`、`changes/`、`runtimecfg/`、`platform/` 包
- 为每张表建立 DAO
- 为还没拆开的消费者预建 ports 矩阵
- 把 EventBus 升级成领域事件系统

关键依赖规则：

1. Web 写路径逐步退出多步骤事务编排；只读查询可以直接依赖 Store
2. collector 不接收通知渠道列表，也不构造通知 sender
3. dispatcher 不依赖任一平台 client 或协议类型
4. 应用服务不依赖 Cobra、Viper、GORM 或 HTTP DTO
5. 接口由消费方定义，且只在边界真实出现时定义
6. SQLite 继续拥有需要原子性的归档、seen、Outbox 和状态条件更新
7. 平台 Gate 位于完整 transport 链最外层，预算范围通过测试证明
8. EventBus 只表示读模型失效，不参与业务一致性

## 7. 可行演进方向

### 阶段 A：修复运行时正确性与运维信号

只改行为，不搬家：

1. 渠道禁用/启用与 outbox 可调度性同事务收敛；dispatcher 不再静默跳过
2. 知识星球评论 baseline sticky；incomplete/error 不得清 baseline
3. Gate 额度释放绑定到响应 Body.Close；增加慢 Body 并发测试
4. 媒体 safe transport 作为 Gate 内层；媒体遵守平台 pause，并具备明确 timeout
5. 缩小 ZSXQ 附件本地化的锁与目录扫描范围
6. 设置提交与 Gate 原子快照更新；消除 drain 导致的部分应用
7. `DeliveryStats` oldest 改为反映活队列，而不是永久 blocked/disabled 任务
8. 删除 v10 后遗留 row、未使用参数和无调用查询入口
9. 顺序关闭 WebSocket
10. 补少量计数：disabled skip、incomplete comment sync、媒体下载失败/耗时

这一阶段优先级最高。

### 阶段 B：只在改写时抽出写用例

1. 改渠道/投递时抽出 delivery 写用例，收口启停、删除、测试、OAuth、重试
2. 改 AI 管理时抽出 aijobs 写用例
3. 写用例成功后统一发布失效通知
4. Web 保留 DTO、认证、审计上下文、错误映射和响应编码
5. 不专门为 history/query 建包

### 阶段 C：按需拆运行时

1. 抽 `delivery.Dispatcher`
2. 抽 B 站 / Microsoft 登录 coordinator
3. 采集 loop 继续共享 runtime，直到有真实拆分收益
4. 不把“删除 Engine”写成硬验收

### 阶段 D：模型与长期运行

1. Outbox 使用平台中立 snapshot，去掉 ZSXQ/Content 回转 Dynamic
2. 系统告警脱离 `RecordDynamics("system")`
3. 媒体孤儿扫描和缺失引用修复
4. Outbox、AI job、content、审计与媒体保留策略
5. 内容全文搜索索引、AI job 游标分页等产品增强
6. 只有确认需要重叠多轮 dispatch 或多实例时，才增加 claim/lease

## 8. 验收标准

### 现在必须过

- 禁用渠道的任意数量投递都不会阻塞启用渠道的 due 批次
- 渠道禁用、启用和删除对已有投递具有文档化、确定性和事务性语义
- 知识星球评论 baseline 一旦建立，后续 incomplete/error 同步不会吞掉新评论通知
- 平台并发测试在响应 Body 未关闭时仍不超过配置值
- 媒体请求明确纳入平台 Gate，或由显式独立媒体预算/超时控制，并遵守 pause
- 设置 API 失败不会留下未说明的 DB/运行态分裂；成功返回后进程内消费者一致
- backlog oldest 不再被永久 blocked/disabled 任务长期污染
- 代码中不存在旧表运行时 row、旧投递 promotion、双写或 legacy payload fallback
- 应用返回前，collector、dispatcher、AI coordinator、媒体 cleaner 和登录任务结束；WebSocket 关闭不再泄漏未等待 goroutine

### 以后才要过

- Web 写路径不再直接拼装 Store + Engine + EventBus
- dispatcher 独立于 bilibili/zsxq 协议包
- Outbox/notify 不再依赖 `model.Dynamic` 作为跨平台快照
- 孤儿扫描能够处理“文件无数据库归属”和“数据库引用文件缺失”
- 保留策略使单机长期运行的 DB/磁盘增长可控

### 明确不验收

- 微服务、外部 MQ、多数据库
- 通用 Repository / ports-and-adapters 目录树
- 预先建好的空业务包
- 当前单进程下的 delivery claim/lease
- “Web 完全不能依赖 Store”的教条

## 9. 验证结果

基于 v10 收口后的主线执行过：

- `make test`：通过
- `make vet`：通过
- `go test -race -shuffle=on ./...`：通过

这些结果说明当前分支具备继续演进的稳定基线，但不能证明上述跨组件语义已经满足。至少还缺：

- 禁用渠道超过批次大小时的跨渠道公平性
- baselined 之后 incomplete 评论同步仍通知
- 慢响应 Body 时的并发上限
- 媒体请求计入 Gate / 遵守 PauseUntil
- 设置更新失败路径不留部分应用
- oldest 统计不被 blocked/disabled 永久污染

## 10. 最终建议

v10 已经完成最危险的数据模型迁移，下一步不应再次大规模调整 schema，也不应把“目录形状”当成下一阶段主线。

当前最短路径：

```text
渠道可调度性 + ZSXQ baseline sticky
        + 真实请求预算/媒体 timeout
        + 设置一致性
        + 活队列统计与少量指标
                    ↓
只在改写用例时抽薄服务
                    ↓
需要时拆 dispatcher / 登录
                    ↓
最后中立 Outbox snapshot 并补长期恢复
```

保留一个二进制、一个 SQLite 和一个可选本机 AI Worker。通过更小的真实边界、明确的事务入口和可验证的运行时语义获得模块化，而不是通过微服务、通用 Repository、空包脚手架或额外配置层获得表面分层。
