# 后端架构评审与演进方向

> 状态：建议方案，尚未实施
>
> 评审范围：Go 后端、SQLite 持久化、B 站采集、通知投递、管理 API 与运行时装配
>
> 基线：`main` 分支，2026-08-10

## 1. 结论

当前后端的技术路线是合理的：单进程、单 SQLite、持久化 Outbox 很适合个人或小团队运行、最多监控 100 个 UP 主的产品边界。现阶段没有必要引入微服务、消息中间件或独立数据库服务。

真正的问题是业务边界还没有成为代码边界。`service.Engine`、`state.Store` 和 `web.Server` 共同承担业务规则，已经形成一个以功能包分层、但内部高度耦合的单体。继续增加采集能力、通知渠道或管理功能，会让一次改动同时穿过 HTTP、运行时状态、数据库、事件推送和协议适配器。

推荐目标是**模块化单体**：继续保留一个二进制和一个 SQLite 数据库，把稳定的业务用例和状态转换放在中心，把 HTTP、调度器、GORM、B 站协议和通知协议放在边缘。

演进优先级如下：

1. 先修复采集和资源预算的语义问题。
2. 建立可复用的应用服务层，让 Web 退出业务编排。
3. 将 `Engine` 拆成独立 worker，并建立统一的 B 站请求闸门。
4. 拆分领域模型、HTTP DTO 和持久化模型，重构 Outbox schema。
5. 最后处理长期数据增长和未来多 worker 能力。

## 2. 从第一性原理理解系统

Bili Notify 的本质不是一个 CRUD 管理后台，而是一条持久化事件处理流水线：

```text
B站返回的外部事实
        ↓
发现、严格解析和分类
        ↓
原子提交：内容档案 + 去重状态 + 投递意图
        ↓
持久化 Outbox
        ↓
带重试和阻塞状态的通知投递
        ↓
管理台查询事实、状态和投递结果
```

从这条流水线可以推出四条核心不变量：

1. **事实采集不依赖订阅者是否存在。**动态已经出现是外部事实；当前有没有启用通知渠道只是内部投递策略。
2. **`archive + seen + outbox` 必须原子提交。**否则可能重复发送、永久漏发，或者出现档案与投递状态不一致。
3. **调度器和协议客户端不是业务核心。**HTTP、B 站 API、SMTP、Webhook、GORM 都应该是可替换边缘实现。
4. **WebSocket 事件不是事实来源。**它只负责提示客户端刷新读模型；断线后必须能通过数据库快照恢复。

当前实现对第二条保障得很好。后续演进必须保留 [`RecordDynamics`](../state/store.go#L558)、[`RecordCommentNotifications`](../state/store.go#L745) 和 [`RecordFeedDynamics`](../state/follow.go#L153) 中已经形成的事务语义。

## 3. 当前结构

当前代码大致形成以下调用关系：

```text
cmd
 └─ app                         进程装配和生命周期
     ├─ web                     HTTP、认证、WebSocket、部分业务编排
     ├─ service.Engine          调度、采集、投递、登录、状态
     ├─ state.Store             SQLite、事务、加密、部分业务规则
     ├─ bilibili.Client         B站协议适配
     ├─ notify                  通知协议与消息渲染
     ├─ media                   媒体文件适配
     └─ telemetry/logging       可观测性
```

[`app.RunWithDependencies`](../app/app.go#L46) 已经承担了 composition root 的角色，这是正确方向。问题主要发生在 composition root 之下：

- [`service.Engine`](../service/engine.go#L35) 直接依赖 `state.Store`、`bilibili.Client`、`media.Downloader`、`notify` 和 `http.Client`。
- [`web.Server`](../web/server.go#L40) 同时持有具体的 `*service.Engine`、`*state.Store` 和事件总线。
- [`state.Store`](../state/store.go#L46) 同时处理认证、运行设置、UP、关注关系、内容档案、Outbox、审计和加密数据。
- [`model`](../model/types.go) 同时被采集器、数据库、通知协议和 HTTP 响应使用。

因此它是一个“功能分包单体”，还不是“模块化单体”。

## 4. 当前值得保留的能力

演进不应推翻现有基础，以下设计具有明确价值：

- 单 SQLite 配合 WAL、外键检查和单写连接，符合当前部署规模。
- 内容档案、seen 和 Outbox 的事务原子性。
- 严格解析未知 B 站结构，不猜测成功。
- Outbox 支持重启恢复、重试、阻塞和分段投递进度。
- 二维码登录、OAuth、渠道秘密和 Cookie 的加密存储。
- WebSocket 使用快照修复事件遗漏，不要求事件总线持久化。
- 管理 API 的认证、CSRF、审计、安全头和协议大小限制。
- 现有测试覆盖了大量成功、失败、取消、重试和安全路径。

目标是重新划定职责边界，而不是替换这些机制。

## 5. 主要问题

### 5.1 P0：采集依赖通知渠道

动态采集会先读取渠道，并在没有启用渠道时直接结束本轮采集，见 [`collectOnce`](../service/engine.go#L385)。评论采集存在相同行为，见 [`commentOnce`](../service/engine.go#L896)。

这把两个本应独立的问题绑在了一起：

- 采集回答“外部发生了什么”；
- 投递回答“哪些渠道需要收到什么”。

当全部渠道暂时禁用时，内容可能不进入档案，也不会建立可靠的 seen 状态。如果禁用时间超过可扫描分页窗口，内容可能永久丢失。

正确语义应当是：

```text
发现内容
  ├─ 始终 archive + seen
  └─ 提交时存在启用渠道，才为这些渠道创建 delivery
```

启用渠道应当在数据库提交事务中查询，而不是由采集器提前读取后作为 `channelIDs` 传入，以减少渠道状态变化造成的竞态。

### 5.2 P0：配置的“全局并发”并不全局

动态采集和评论采集分别创建 `errgroup` 并设置 `RequestConcurrency`，见 [`collectOnce`](../service/engine.go#L454) 和 [`commentOnce`](../service/engine.go#L942)。关注关系刷新、登录和会话校验也会独立发起请求。

共享的 `rate.Limiter` 只能限制平均请求速率，不能限制同时在途的 HTTP 请求数。因此配置并发为 4 时，动态和评论重叠执行可能产生 8 个并发请求，再叠加其他工作流。

需要建立唯一的 `BiliRequestGate`，统一负责：

- 全局速率限制；
- 全局并发信号量；
- 风控暂停；
- 请求超时；
- 当前登录会话快照；
- B 站错误分类、指标和 trace。

所有 B 站请求都必须经过该闸门，具体 worker 不再自行持有 limiter 或 session 锁。

### 5.3 P1：Web 层直接执行完整业务用例

例如 [`createUPAPI`](../web/api.go#L48) 同时负责解析请求、构造和校验对象、检查冲突、写数据库、唤醒关系刷新、发布事件和补充审计信息。

[`deleteUPAPI`](../web/api.go#L115) 还直接组合数据库删除与媒体目录删除。渠道保存逻辑则在 [`saveChannel`](../web/ws.go#L566) 中处理秘密保留、OAuth 身份变化、领域校验、持久化和解除阻塞。

后果包括：

- 相同业务无法被 CLI、后台任务或另一套 API 安全复用；
- 调用者必须知道一次修改后需要唤醒哪个 worker、发布哪些 Topic；
- HTTP 测试承担了本应属于业务用例的测试责任；
- 文件删除失败等跨资源异常没有统一恢复策略。

Web 层应当只负责认证、DTO 解码、调用应用用例、错误映射和 DTO 编码。业务入口应收敛为 `AdminService`、`AccountService` 等应用服务。

### 5.4 P1：`Engine` 是上帝对象

[`service.Engine`](../service/engine.go#L35) 同时承担：

- 动态轮询；
- 评论轮询；
- 关注关系刷新；
- 会话验证和风控暂停；
- Outbox 调度和投递；
- 系统告警；
- B 站二维码登录；
- Microsoft OAuth；
- 状态聚合；
- 运行设置热更新；
- WebSocket 事件发布。

它包含大量 mutex、atomic、channel、WaitGroup 和进程生命周期上下文。问题不只是文件接近 2000 行，而是这些职责具有不同的变化原因和一致性边界。

建议拆成：

```text
Runner
  ├─ DynamicCollector
  ├─ CommentCollector
  ├─ RelationRefresher
  ├─ SessionValidator
  ├─ DeliveryDispatcher
  ├─ BiliLoginCoordinator
  └─ MicrosoftAuthCoordinator
```

`Runner` 只负责启动、取消和等待，不持有领域状态，也不实现业务用例。

### 5.5 P1：共享 `model` 混合了多个边界

[`model.Channel`](../model/types.go#L67) 使用 `map[string]string` 表示所有渠道设置和秘密。它会导致：

- 配置键拼写错误只能在运行时发现；
- 普通设置、密码、Webhook 和 OAuth Token 没有类型边界；
- Web 层必须维护秘密字段黑名单；
- 新增渠道需要同时修改模型校验、sender factory、Web 过滤规则和 API 表单。

[`model.Dynamic`](../model/types.go#L175) 同时承担采集结果、持久化 JSON、通知输入和管理 API 输出。Web 层因此必须手动移除本地路径、评论坐标等内部字段。

建议拆分为：

- `domain.Content`、`domain.Delivery` 等领域对象；
- `sqlite.*Row` 和持久化 payload；
- `http.*Request`、`http.*Response`；
- 通知适配器使用的不可变 `NotificationPayload`。

渠道应使用显式类型，例如 `EmailConfig + EmailSecrets`、`MicrosoftConfig + MicrosoftCredentials`。HTTP 读模型从类型上就不包含 secret，而不是依赖运行时过滤。

### 5.6 P1：`Store` 同时是适配器和业务服务

[`state.Store`](../state/store.go#L46) 已经包含大量业务不变量，例如最多 100 个 UP、基线推进、关注路由、Outbox 状态机和删除联动。

事务规则放在 SQLite 实现中并非错误，但当前所有消费者都依赖整个具体 Store，导致：

- 无法从依赖关系看出每个用例真正需要哪些能力；
- 测试应用逻辑时必须构造真实 Store 或使用具体实现；
- 数据库实现细节容易继续向 Web 和 worker 泄漏；
- Store 会随着每个新业务模块持续膨胀。

不应创建另一个万能 `Repository` 接口。正确方式是由消费者定义小接口：

```text
DynamicCollector   -> CollectorRepository
CommentCollector   -> CommentRepository
DeliveryDispatcher -> DeliveryQueue
AdminService       -> AdminRepository
DashboardQuery     -> DashboardReader
```

这些接口仍可由同一个 SQLite 连接实现，目的不是替换数据库，而是限制依赖面。

### 5.7 P1：Outbox 数据布局造成线性扫描

[`putDeliveryTx`](../state/store.go#L911) 将完整 `model.Delivery` 编码到 `payload_json`，同时又把 state、attempts、next_at 等字段存储为数据库列。

[`dispatchOnce`](../service/engine.go#L1260) 每秒除读取到期任务外，还会读取并反序列化全部投递以计算积压。[`Status`](../service/engine.go#L1886) 也会加载全部 Outbox。删除 UP 时，[`DeleteUP`](../state/store.go#L303) 会扫描并解码所有 Delivery 才能判断归属。

随着阻塞任务增长，这些路径会从索引查询退化为 O(Outbox 总量) 的 JSON 反序列化。

建议在 delivery 表中增加：

- `up_uid`；
- `content_kind`；
- `content_id`；
- `payload_version`；
- 独立的 `progress_json`；
- 必要时增加 `claimed_at`、`claim_token`。

`payload_json` 只保留不可变的通知快照，调度状态只保存在结构化列中。提供专用查询：

- `OutboxStats()`；
- `ListDeliverySummaries(limit)`；
- `LoadDuePayloads(limit)`；
- `DeleteDeliveriesByUP(uid)`。

### 5.8 P2：跨数据库和文件系统操作没有恢复模型

媒体文件会在数据库提交前下载；数据库提交失败可能留下孤儿文件。删除 UP 时数据库事务和媒体目录删除也无法形成真正的原子事务。

这不是通过扩大 SQLite 事务可以解决的问题。正确模型是：

1. 数据库是资源归属的事实来源；
2. 文件写入和删除必须幂等；
3. 失败后通过持久化清理任务或周期性孤儿扫描恢复；
4. 临时文件必须有明确命名和过期清理规则。

### 5.9 P2：事件发布依赖调用者记忆

Web、Engine 和 SettingsManager 都直接调用 `EventBus.Publish`。调用者需要知道一次变更涉及 Status、UP、Channel、Delivery 还是登录主题。

当前 WebSocket 依靠重连快照能够修复遗漏，因此不需要引入持久化领域事件。但应当把总线明确定位为 `InvalidationBus` 或 `RevisionBus`，并由应用用例在事务提交后统一发布 `ChangeSet`：

```text
ChangeSet{
  StatusChanged,
  UPsChanged,
  ChannelsChanged,
  DeliveriesChanged,
}
```

数据库失败时不发布，成功提交后只发布一次。

## 6. 目标架构

推荐依赖方向：

```text
web/http ───────────────┐
runtime/scheduler ──────┤
                        ▼
                application use cases
                 ├─ admin
                 ├─ collect
                 ├─ delivery
                 ├─ account
                 └─ dashboard
                        │
                        ▼
                     domain
                        ▲
                        │
        ┌───────────────┼────────────────┐
        │               │                │
   sqlite adapter  bilibili adapter  notification adapters

app 只负责装配对象和管理进程生命周期
```

推荐的职责划分如下。

### 6.1 Domain

负责稳定业务概念和纯状态转换：

- Content、CommentThread、WatchTarget；
- Channel、Delivery、RetryPolicy；
- RuntimeSettings；
- 基线、去重和投递状态的合法转换；
- 不依赖 JSON、GORM、HTTP、slog 或 OpenTelemetry。

### 6.2 Application

每个包表示一组具体用例，并在包内定义自己需要的接口：

- `collect`：执行一次动态/评论采集并原子提交结果；
- `delivery`：领取、发送、完成、重试或阻塞投递；
- `admin`：管理 UP、渠道和运行设置；
- `account`：B 站登录、Microsoft 授权和会话切换；
- `dashboard`：构造轻量管理台读模型。

应用层负责事务意图和业务编排，但不包含调度 ticker，也不依赖具体数据库或 HTTP 客户端。

### 6.3 Adapters

- `sqlite`：实现应用层定义的 repository/queue 接口和数据库事务；
- `bilibili`：将 B 站协议响应转换为领域输入；
- `notify`：将不可变通知载荷发送到 SMTP、Graph 或机器人；
- `media`：安全读写媒体文件并执行幂等清理；
- `web`：将 HTTP/WebSocket 转换为应用用例调用。

### 6.4 Runtime

负责周期调度、生命周期和共享资源预算：

- 每个 worker 独立运行；
- 通过应用服务执行一次工作；
- 共享 `BiliRequestGate`；
- 设置变更通过原子快照或订阅通知影响下一次调度；
- 不直接写数据库或发布 UI Topic。

## 7. 必须遵守的依赖规则

1. `web` 不直接依赖 `*state.Store` 或 `*service.Engine`。
2. 应用层不依赖 GORM、`net/http`、具体通知 sender 或 B 站 client。
3. 接口由消费者定义，不创建统一的 `ports` 或万能 Repository 包。
4. SQLite 仍然负责需要原子性的 `archive + seen + outbox` 事务。
5. WebSocket 事件仅作为读模型失效通知，不参与业务一致性。
6. 通知 payload 是创建 Delivery 时的不可变快照；调度状态不重复写入 payload。
7. 所有 secret 使用独立类型，不能出现在 HTTP response 类型中。
8. 所有 B 站调用都经过同一个 RequestGate。

## 8. 分阶段演进计划

### 阶段一：修正语义和资源边界

- 没有启用渠道时仍归档内容并推进 seen/baseline。
- 在提交事务内确定启用渠道并创建 Outbox。
- 明确定义禁用渠道对已有 Delivery 的处理策略。
- 引入统一 `BiliRequestGate`。
- 增加 `OutboxStats`，移除每秒全量反序列化。
- 增加 100 UP 的确定性负载测试，验证请求速率、并发和发现延迟。

这一阶段不要求移动包，只修复当前语义并建立后续边界。

### 阶段二：建立应用服务层

先引入：

- `AdminService`；
- `CollectionService`；
- `DeliveryService`；
- `AccountService`；
- `DashboardQuery`。

让现有 Web handler 改为调用这些服务。完成后，Web 不再直接操作 Store、媒体目录、Engine 唤醒 channel 和 EventBus。

### 阶段三：拆解 Engine

按风险从低到高逐个迁移：

1. `DeliveryDispatcher`；
2. `RelationRefresher`；
3. `SessionValidator`；
4. `CommentCollector`；
5. `DynamicCollector`；
6. 两类登录 Coordinator。

最终删除 `Engine`，或者让它退化为只管理 worker 生命周期的 `Runner`。

### 阶段四：拆分模型和重构 schema

- 拆分领域对象、HTTP DTO、SQLite Row 和通知 payload。
- 将渠道配置改为有类型的 config/secrets。
- 给 Outbox 增加可索引归属字段和 payload version。
- 删除 `Delivery.EffectiveKind` 等运行时兼容逻辑。
- 用一次性数据库 migration 转换已有持久化数据。

允许 API 破坏性更新不等于可以静默丢失已持久化内容或待投递通知。正确策略是迁移数据后删除兼容分支，而不是长期维护双格式读取。

### 阶段五：长期运行能力

- 内容和媒体保留策略；
- SQLite FTS5 搜索；
- 游标分页替换大 offset；
- 媒体孤儿扫描和清理任务；
- 如果未来需要多 dispatcher，再实现原子 claim/lease。

这些工作不应阻塞前四阶段。

## 9. 验收标准

架构演进完成与否，应通过以下可观察结果判断：

- 新增通知渠道时，不修改采集器、运行时调度器和 SQLite 核心事务。
- 新增一种 B 站内容来源时，不修改 Web 和投递调度器。
- 所有 B 站请求的实测并发永远不超过配置值。
- 0 个启用渠道时仍能查询到新采集内容，但不会创建 Delivery。
- 每个写用例只在事务成功后发布一次 ChangeSet。
- Dashboard 状态查询不反序列化 Outbox payload。
- HTTP response 类型从结构上不可能包含密码、Webhook 或 OAuth Token。
- `web` 只依赖应用服务接口，不直接依赖具体 Store 或 Engine。
- 100 UP 的确定性测试能够证明动态发现延迟和请求预算符合设计目标。
- 各 worker 可以在不启动完整 Web Server 的情况下进行应用层测试。

## 10. 明确不做什么

- 不因为代码量增长就拆微服务。
- 不引入 Kafka、RabbitMQ 等外部消息系统；SQLite Outbox 已满足当前可靠性目标。
- 不为未来可能出现的多实例部署提前引入分布式协调。
- 不先做纯目录搬迁；只有依赖方向改变才算架构演进。
- 不创建通用 DAO、万能 Repository 或抽象工厂体系。
- 不在运行路径长期保留旧 API 或旧 payload 兼容逻辑。

## 11. 最终建议

当前架构最有价值的资产是严格协议处理、SQLite 事务和持久化 Outbox；最主要的负担是业务编排散落在 Web、Engine 和 Store 中。

因此演进的核心不是“换技术”，而是让事务性业务用例成为系统中心：

```text
稳定业务规则在中心
HTTP、调度、SQLite、B站和通知协议在边缘
单二进制和单数据库继续保留
```

这条路线能够在不增加部署复杂度的前提下，显著降低新采集能力、新通知渠道和新管理入口的修改范围。
