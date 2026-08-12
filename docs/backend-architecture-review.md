# 后端架构评审（第二版）

> 状态：基于当前实现的代码评审，不是待实现设计稿
>
> 评审范围：Go 后端、SQLite 持久化、B 站与知识星球采集、通知 Outbox、AI 控制面与 Worker 边界、管理 API、进程生命周期
>
> 当前基线：`main@bdab784`（`v0.4.5`，2026-08-11）
>
> 对照基线：上一版评审提交 `be44131`（2026-08-10）

## 1. 结论

当前技术路线仍然成立：B 站与知识星球协议适配器、单 SQLite、持久化任务、可选 Python AI Worker 和一个 Go 管理进程，符合本项目的部署规模。没有理由引入微服务、外部消息队列或独立数据库服务。

但上一版评审指出的核心问题没有随着功能扩展得到解决。自上一版评审后，仓库新增了跨平台采集、统一内容模型、第二套 Outbox、AI 任务系统和独立 Worker；业务能力明显增强，代码边界却进一步分叉。当前最严重的问题不再只是 `Engine` 较大，而是同一业务事实存在两代持久化模型和两条投递路径：

```text
B 站旧链路：ups/dynamics/comments/seen_*/deliveries
       同步双写 ↓                         ↑ 投递实际仍消费旧表
跨平台新链路：sources/contents/comment_nodes/seen_items/outbox
                                      ↓ promote + payload 转换
                                  deliveries
```

这不是可长期保留的模块化单体，而是一个正在运行的迁移中间态。项目明确不保留向后兼容，因此下一阶段不应继续扩展桥接层，而应尽快选定唯一模型并删除旧实现。

建议优先级：

1. 修复“没有通知渠道就停止 B 站采集”的事实丢失风险。
2. 合并为唯一的内容、seen 和 Outbox 模型，删除双写和 `PromotePlatformOutbox`。
3. 让并发配置成为所有平台请求都必须遵守的真实约束，并修复知识星球 worker 的退出等待。
4. 建立按业务能力划分的应用服务，让 Web、collector 和调度器退出事务编排。
5. 最后拆分 `Engine`、`Store` 和共享 `model`，补齐媒体恢复与长期保留策略。

## 2. 本次代码变化带来的架构判断

从 `be44131` 到 `bdab784` 共涉及 180 个文件，净新增约 1.58 万行。后端的实质变化有三组：

- 新增 AI profile、prompt、持久化 job、自动转写/总结、Unix Socket gRPC Worker 和终态通知。
- 新增知识星球账号、来源、内容、附件、完整评论树、回补与独立轮询器。
- 新增跨平台 `sources/contents/comment_nodes/seen_items/outbox` schema 和 `/api/v3` 资源接口。

这些变化验证了上一版评审的判断：系统已经不是简单 CRUD 后台，而是多个外部协议生产事实、多个持久化工作流消费事实的事件处理系统。新增能力中值得保留的架构决策包括：

- [`app.RunWithDependencies`](../app/app.go#L46) 仍是清晰的 composition root。
- AI 使用持久化控制面和独立执行面；Python/yt-dlp/FFmpeg 的生态差异足以支持独立 Worker，Unix Socket 也把边界限制在本机。
- AI job 在创建时密封模型、提示词和目标渠道快照，状态更新使用条件写入，见 [`CreateAIJob`](../state/ai.go#L274) 与 [`ClaimAIJob`](../state/ai.go#L559)。
- 跨平台 ID 把 platform 纳入身份，`contents`、`comment_nodes` 和附件 schema 比旧 JSON payload 更适合作为长期事实模型。
- 知识星球动态在零启用渠道时仍会归档，说明“采集与投递分离”在新链路中已经可行。
- 评论树只在完整快照下标记删除，自动 AI job 与来源动态同事务创建，这些一致性方向正确。

因此本次建议不是回退新功能，而是完成已经开始但尚未收口的架构迁移。

## 3. 核心不变量

后续重构应以以下不变量判断，而不是以目录是否整齐判断：

1. **一个业务事实只有一个权威持久化表示。**同一来源、内容、评论和投递不能同时依赖 legacy row 与 v3 row。
2. **采集事实不依赖通知渠道。**零渠道只表示不创建投递，不表示停止归档、seen 或水位推进。
3. **`archive + seen + delivery intent` 在一个 SQLite 事务中提交。**投递器直接消费该事务写入的唯一队列。
4. **配置必须对应可验证的运行时约束。**名为 concurrency 的设置不能未使用，也不能只约束某一条并行支路。
5. **每个 goroutine 都由拥有它的组件等待退出。**上层返回后不能再访问即将关闭的 Store、HTTP client 或事件总线。
6. **HTTP、调度 ticker、SQLite/GORM、外部协议和文件系统是边缘。**业务用例不能由 Web handler 或协议 client 临时拼装。
7. **AI Worker 是执行适配器，不是业务状态所有者。**job、依赖、重试、取消和通知语义继续由 Go + SQLite 控制。

## 4. 上一版评审落实状态

| 上一版问题 | 当前状态 | 判断 |
| --- | --- | --- |
| B 站采集依赖启用渠道 | 未修复 | 动态和评论仍在零渠道时提前返回 |
| B 站“全局并发”不全局 | 未修复 | 动态、评论、关系与登录仍无统一并发闸门 |
| Web 直接编排业务 | 加重 | 新增来源双写、知识星球登录、AI profile/job 编排 |
| `Engine` 是上帝对象 | 部分改善 | AI 与知识星球有独立 runner；B 站 Engine 增至 2173 行，仍含投递和两类登录 |
| 共享 `model` 混合边界 | 加重 | 新旧 Dynamic/Content 并存，AI job 使用 `any` 输入和结果 |
| `Store` 是万能依赖 | 加重 | 文件拆开了，但 `*state.Store` 新增 platform 与 AI 全部职责 |
| Outbox 全量扫描 | 部分尝试 | 新表增加索引列，但投递仍转入旧表并每秒全量读取旧队列 |
| 数据库与媒体缺少恢复模型 | 未修复 | 删除来源仍由 Web 在事务后直接删除目录 |
| 事件发布依赖调用者记忆 | 未修复 | Web、Engine、AIEngine 和设置管理器继续手工选择 Topic |

## 5. 主要发现

### 5.1 P0：两代事实模型和两套 Outbox 同时在线

[`00008_cross_platform.sql`](../state/migrations/00008_cross_platform.sql#L1) 把旧数据复制到 `sources`、`contents`、`comment_nodes`、`seen_items` 和 `outbox`，但没有删除旧的 `ups`、`dynamics`、`comments`、`seen_*` 和 `deliveries`。运行时随后持续维护两边：

- [`archiveDynamicsTx`](../state/content.go#L88) 对每条 B 站动态同时写 `dynamics` 与 `contents`。
- [`PutUP`](../state/store.go#L289) 同时维护 `ups` 与 `sources`，[`SetUPResult`](../state/store.go#L414) 也继续双写运行状态。
- B 站评论先通过 [`SyncCommentTree`](../state/platform.go#L657) 写新评论树，再通过 [`RecordCommentNotifications`](../state/store.go#L866) 保留旧列表投影；这两个写入不是同一个事务。
- 新 [`outbox`](../state/platform.go#L389) 不由 dispatcher 直接消费，而是经 [`PromotePlatformOutbox`](../state/platform.go#L826) 反序列化、查询当前来源和附件、转换成旧 `model.Delivery`，再写入 `deliveries`。

直接后果：

- B 站的每次模型变化都要同时维护两套 schema、两套 ID 和两套查询语义。
- 新 Outbox 中的结构化 `platform/source_id/content_id` 无法惠及实际调度、状态统计和失败处理。
- 评论链路存在多个连续事务，任一步失败都会暴露部分提交状态。
- 新 Outbox 本应保存不可变通知快照，promotion 却读取“当前”的来源名称和附件，队列语义会随数据库后续修改变化。
- 任何新平台都被迫先写新模型，再适配回只理解 `Dynamic`/`CommentNotification` 的旧通知模型。

目标必须是唯一链路：

```text
platform collector
      ↓
sources + contents/comment_nodes + seen_items + outbox（同一事务）
                                              ↓
                                   DeliveryDispatcher 直接消费
```

删除 `PromotePlatformOutbox`、旧 `deliveries` 以及只为旧读模型服务的双写。B 站专属的 feed cursor、关注关系和评论坐标可以继续使用专属表；它们是协议同步状态，不是第二份通用内容事实。

项目不保留向后兼容，因此这里不应增加双读、版本 fallback 或长期兼容 adapter。是否保留现有数据库内容是一个需要显式确认的产品决策：若必须保留，只执行一次确定性数据转换并立即删除旧路径；若不要求保留，则直接建立新 schema，不能继续把迁移期结构当成正式架构。

### 5.2 P0：B 站采集仍由通知渠道决定是否运行

[`collectOnce`](../service/engine.go#L406) 在读取渠道后，如果没有启用渠道就在 [`service/engine.go`](../service/engine.go#L443) 返回；[`commentOnce`](../service/engine.go#L921) 在 [`service/engine.go`](../service/engine.go#L950) 做同样处理。

这仍然违反系统最基本的事实语义。渠道全部禁用期间：

- 新动态不归档；
- seen 和 baseline 不推进；
- 评论树不刷新，编辑和删除也不会同步；
- 超出有限扫描窗口后可能永久丢失事实。

`RecordDynamics`、`SyncCommentTree` 和知识星球链路已经能够接受空 `channelIDs`，所以不需要新的抽象才能修复。采集必须始终执行；事务在提交时读取启用渠道，仅决定是否创建 Outbox 行。

### 5.3 P1：请求并发配置不是全局约束，知识星球配置甚至没有生效

B 站动态和评论分别创建 `errgroup` 并各自调用 `SetLimit`，见 [`service/engine.go`](../service/engine.go#L475) 与 [`service/engine.go`](../service/engine.go#L967)。共享的 `rate.Limiter` 只限制平均速率，不限制同时在途请求。动态、评论、关系刷新、会话验证和登录重叠时，总并发仍可超过 `BilibiliRequestConcurrency`。

`ZSXQRequestConcurrency` 只出现在配置、校验和 API 中，生产运行路径没有读取它。知识星球动态同步在主循环执行，评论同步由 [`Collector.Run`](../zsxq/collector.go#L60) 另起 goroutine，两者可以重叠，但只有 rate limiter，没有并发门。

每个平台需要一个自己的 `RequestGate`：

- 平台级 rate limiter；
- 平台级 `semaphore.Weighted` 或等价并发限制；
- 统一请求超时、风险暂停、账号 session 快照、错误分类和指标；
- 该平台的采集、评论、登录和校验全部经过同一个 gate。

不要做一个强行统一 B 站和知识星球协议的 `PlatformClient`。两者只共享资源预算模式，不共享分页、鉴权、风控和基线语义。

### 5.4 P1：Web 继续承担应用服务职责

新代码进一步确认 Web 边界没有收敛：

- [`createSourceV3`](../web/api_v3.go#L174) 先写 `Source`，再写 `UP`，失败时手工补偿删除。
- [`updateSourceV3`](../web/api_v3.go#L219) 先提交 Source，再提交 UP；第二步失败时无法回滚第一步。
- [`deleteSourceV3`](../web/api_v3.go#L264) 选择不同数据库删除路径，再直接删除媒体目录并发布 Topic。
- [`web/ai.go`](../web/ai.go#L91) 直接管理 profile、prompt、job、secret 保留、重试、取消和事件发布。
- [`web/api_v3.go`](../web/api_v3.go#L59) 直接调用具体的 `*zsxq.LoginManager` 并把协议错误映射、账号状态和事件组合在 handler 中。

这些都不是 HTTP 职责。Web 应只做认证、DTO 解码、调用用例、错误到 HTTP 的映射和 DTO 编码。

建议按业务能力建立具体服务，而不是增加一个泛化的 `application`、`ports` 或 `repository` 层：

- `sources.Admin`：来源创建、修改、删除、数据库与媒体清理意图；
- `accounts.Service`：B 站二维码、知识星球短信登录和账号切换；
- `delivery.Admin`：渠道、测试、重试和阻塞解除；
- `aijobs.Admin`：profile、prompt、提交、取消和重试；
- `history.Query`：内容、评论树和附件读模型。

服务返回明确的 `ChangeSet`，由统一边界在成功提交后发布失效通知，handler 不再记忆 Topic 组合。

### 5.5 P1：知识星球评论 goroutine 没有纳入生命周期等待

[`Collector.Run`](../zsxq/collector.go#L60) 在评论 timer 触发时启动匿名 goroutine，但 `Run` 收到 context 取消后直接返回，没有等待该 goroutine 结束。`app` 的 errgroup 因而可能认为 collector 已停止，随后执行 `store.Close()`，而评论 goroutine 仍在数据库调用、附件写入或错误收尾中。

这违反“启动者负责等待”的并发规则。最小修复是由 collector 自己使用 `errgroup` 或显式 `WaitGroup` 管理动态与评论 worker，并在 `Run` 返回前等待。更清晰的最终结构是两个独立 loop：

```text
ZSXQDynamicWorker.Run(ctx)
ZSXQCommentWorker.Run(ctx)
```

两者共享同一个 ZSXQ `RequestGate`，但分别拥有调度周期和单轮用例。

### 5.6 P1：`state.Store` 仍是全系统的万能具体依赖

代码虽然拆为 `store.go`、`platform.go` 和 `ai.go`，但它们都继续给同一个 `*state.Store` 增加方法。目前仅这三个实现文件就约 2900 行，并同时负责：

- 管理员认证、运行设置和加密；
- B 站旧模型、跨平台新模型和双写同步；
- 两套 Outbox 与投递状态；
- AI profile、prompt、job 状态机和通知；
- 审计、来源生命周期、评论树与媒体元数据。

问题不在文件长度本身，而在所有消费者都能调用全部能力。`service.Engine`、`AIEngine`、`zsxq.Collector`、`zsxq.LoginManager` 和 `web.Server` 都依赖具体 Store，依赖图不能表达最小权限和事务所有权。

接口应由消费方按用例定义，例如：

```text
BilibiliCollector -> SourceReader + ArchiveWriter + BiliSyncState
ZSXQCollector      -> SourceReader + ArchiveWriter + ZSXQSyncState
Dispatcher         -> DeliveryQueue + ChannelReader
AIEngine           -> AIJobQueue + BiliSessionReader
sources.Admin      -> SourceStore + CleanupQueue
```

SQLite 可以继续由一个连接实现全部接口。目标是限制依赖和事务入口，不是制造可替换数据库的虚假抽象。

### 5.7 P1：共享模型仍跨越协议、领域、持久化和 HTTP

新 `model.Content` 比旧 `model.Dynamic` 更接近稳定领域事实，但当前二者并存并互相转换。典型证据是 [`platformDelivery`](../state/platform.go#L857)：新 `Content` 被重新组装成旧 `Dynamic`，才能进入通知系统。

同时：

- `model` 类型普遍带 JSON tag，被直接用作数据库 payload 和 HTTP response。
- `AIProfile` 用 `json:"-"` 隐藏 API Key，秘密安全依赖序列化标签而不是 response 类型边界。
- `AIJob.Input` 与 `AIJob.Result` 是 `any`，Store 和 Engine 依赖运行时 type switch 恢复具体含义。
- 通知 sender 接收包含持久化、媒体和 UI 字段的宽模型。

完成唯一模型迁移后，应按拥有者拆分类型：协议响应留在 `bilibili`/`zsxq`，归档事实留在 `archive`，队列快照留在 `delivery`，AI job 使用显式的 transcription/summary payload，HTTP 使用不可能包含 secret 或本地路径的 DTO。

### 5.8 P1：新 Outbox 的结构化收益被旧 dispatcher 抵消

[`dispatchOnce`](../service/engine.go#L1375) 每秒先 promote 新 Outbox，再读取到期旧 Delivery，随后调用 [`ListDeliveries(0)`](../service/engine.go#L1389) 反序列化全部队列来计算深度和最老年龄。[`DeleteUP`](../state/store.go#L341) 与 [`DeleteSource`](../state/platform.go#L255) 也扫描并解码全部旧 Delivery 判断归属。

这使队列成本继续随总积压量线性增长，并且新表已有的 `source_id/content_id/state/next_at` 索引无法用于真实调度和清理。

唯一 Outbox 应提供直接的结构化操作：

- `ClaimDue(limit, lease)`；
- `Complete(id)`；
- `Retry(id, nextAt, error, progress)`；
- `Block(id, reason)`；
- `Stats()`；
- `DeleteBySource(sourceID)`；
- `ListSummaries(cursor, limit)`。

payload 只保存创建时的不可变通知快照；state、attempt、next time、progress 和归属只保存在列中。

### 5.9 P2：数据库和文件系统仍没有持久化恢复协议

附件下载发生在数据库提交前；来源删除由 [`deleteSourceV3`](../web/api_v3.go#L264) 先提交数据库删除，再调用 `media.RemoveSource`。失败被忽略，数据库和磁盘无法原子提交。

正确模型不是扩大 SQLite 事务，而是：数据库记录资源归属和待清理意图；文件写入使用临时文件加原子 rename；删除与孤儿扫描幂等；失败清理任务可重试。这个问题不阻塞唯一模型收口，但在知识星球附件和 AI 缓存加入后，长期运行成本已经上升。

### 5.10 P2：事件总线仍由调用者手工维护

Web、Engine、AIEngine 和设置管理器都直接调用 `EventBus.Publish`。当前 WebSocket 仍以快照修复遗漏，因此不需要持久化领域事件；但内存总线应明确只是读模型失效通知。

写用例应返回 `ChangeSet`，统一在事务成功后发布一次。这样不会把 Topic 选择散落到 handler、collector 和 Store 调用者中，也不会让数据库失败后误发刷新事件。

## 6. 目标架构

目标仍是模块化单体，但按业务能力组织，而不是增加传统分层目录：

```text
cmd/app                         只装配与管理进程生命周期
   │
   ├─ web                       HTTP / WebSocket adapter
   │    └─ 调用具体业务服务与查询接口
   │
   ├─ bilibili                  协议 client + 平台 worker
   ├─ zsxq                      协议 client + 平台 worker
   │       两者共享模式，不强行共享协议接口
   │
   ├─ archive                   Source / Content / CommentTree 用例与小接口
   ├─ delivery                  唯一 Outbox、dispatcher、channel 管理
   ├─ aijobs                    profile/job 状态机与 Worker coordinator
   ├─ accounts                  登录与账号生命周期
   │
   ├─ sqlite                    实现各消费方定义的持久化接口
   ├─ notify                    SMTP / Graph / robot adapters
   └─ media                     文件适配与清理 worker

Python AI Worker               仅执行下载、转写和总结；不拥有持久状态
```

关键边界：

- 平台差异留在各平台包；跨平台统一只发生在 `Source/Content/CommentNode` 事实和 Outbox 之后。
- archive 事务决定 seen 与 delivery intent，不由 collector 传递一串预先读取的渠道 ID。
- dispatcher 直接消费唯一 Outbox，不再经过模型转换队列。
- Web 不依赖 `*state.Store`、`*service.Engine`、`*zsxq.LoginManager` 或 EventBus。
- runtime worker 只负责 ticker、取消和调用一次用例，不包含 HTTP DTO、GORM 或通知 sender 构造。
- AI 的 profile、job 和依赖状态机归 `aijobs`；gRPC 只是执行端口。

## 7. 实施顺序

### 阶段一：先修阻塞性正确性问题

1. 删除 B 站动态与评论的零渠道提前返回，增加零渠道归档/seen/baseline 测试。
2. 为 B 站和知识星球各建立唯一 RequestGate，覆盖所有请求路径。
3. 让 `ZSXQRequestConcurrency` 真正生效；如果产品不需要并发，则删除该设置，不能保留无效配置。
4. 让知识星球 collector 等待所有 goroutine 退出，增加取消期间阻塞 HTTP 与数据库收尾测试。

### 阶段二：完成唯一数据模型迁移

1. 明确现有数据库是否需要保留；这是实施前唯一必须确认的产品决策。
2. 以 `sources/contents/comment_nodes/seen_items/outbox` 为唯一事实模型。
3. 让 B 站 collector 直接写统一模型，删除 `dynamics/comments/seen_*` 双写。
4. 让 dispatcher 直接消费 `outbox`，删除 `PromotePlatformOutbox` 和旧 `deliveries`。
5. 删除旧查询 DTO、`EffectiveKind`、legacy payload 转换和只为旧 UI 投影存在的写入。
6. 用索引查询实现统计、来源删除和队列分页。

这一阶段应作为一个完整的破坏性变更完成，不接受长期双读或双写。

### 阶段三：收敛业务入口

1. 先提取 `sources.Admin`、`delivery.Admin` 和 `aijobs.Admin`，因为当前 Web 编排最集中。
2. 将账号登录与渠道 OAuth 从 `Engine` 移到 `accounts`。
3. 写用例返回 `ChangeSet`，统一发布 WebSocket 失效通知。
4. Web 只保留 DTO、认证、错误映射和响应编码。

### 阶段四：拆分运行时和依赖面

1. 把 B 站动态、评论、关系、会话验证和 delivery 分成独立 worker。
2. 让每个 worker 依赖消费方定义的小接口。
3. 删除当前 `service.Engine`；不要保留一个改名后的万能 Runner。
4. 把 `state` 收敛为 SQLite adapter；业务状态转换移到拥有该能力的包，事务性条件更新留在 adapter 实现。
5. 拆除共享 `model` 中的 legacy 类型和 `any` payload。

### 阶段五：长期运行能力

- Outbox/AI job/content/媒体保留策略；
- 媒体清理任务与孤儿扫描；
- 内容搜索索引；
- AI job 游标分页；
- 如确有多 dispatcher 需求，再增加 claim lease；当前单进程不预先设计分布式协调。

## 8. 验收标准

- 数据库中每个来源、内容、评论和投递只有一份权威记录。
- 代码中不存在 `PromotePlatformOutbox`、B 站内容双写和 legacy payload fallback。
- 零启用渠道时，两个平台都继续归档、同步删除/编辑并推进 seen/baseline，但不创建 Outbox。
- 任一平台所有请求的实测同时在途数不超过该平台配置。
- `ZSXQRequestConcurrency` 有确定性测试证明生效，或该配置已删除。
- 应用退出前所有 collector、dispatcher 和 AI coordinator goroutine 均已结束。
- Dashboard/健康检查不反序列化全量 Outbox payload。
- 删除来源通过索引删除 Outbox，不扫描整个队列。
- HTTP response 类型从结构上不包含 API Key、Cookie、Webhook、Token 或本地绝对路径。
- Web 不直接依赖具体 Store、Engine、平台 LoginManager 或 EventBus。
- 新增通知渠道不修改任一平台 collector；新增平台不修改 dispatcher。
- AI Worker 不可用时 job 保持可解释的持久状态，Go 主进程的采集、投递和管理功能不受影响。

## 9. 最终建议

当前最值得保留的是严格协议解析、SQLite 事务、跨平台 ID、完整评论树、持久化 AI job 和本地 Worker 隔离。当前最需要删除的是迁移中间态：两代模型、两套 Outbox、Web 双写编排和无效的并发配置。

下一轮不应继续增加平台或通知功能。先把已经建立的跨平台 schema 变成唯一事实源，让采集、投递、AI 和管理 API 都围绕同一组明确用例运行。完成这一点后，系统仍然可以保持部署简单，但代码改动范围、故障恢复路径和长期数据成本会显著可控。
