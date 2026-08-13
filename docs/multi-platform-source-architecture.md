# 多数据源统一抽象方案

> 状态：已落地（阶段 0–4，第三平台 adapter 待产品选型）
> 日期：2026-08-12
> 实现：v13 中立 Outbox、平台模块注册、中立 B 站采集出口与共享正确性修复
> 相关文档：
>
> - [需求分析与技术设计](requirements-and-design.md)
> - [后端架构评审（第三版）](backend-architecture-review-v3.md)

## 1. 问题

v10 已经把**持久化事实**统一到：

```text
platform_accounts / sources / contents / attachments
comment_nodes / seen_items / sync_targets / outbox
```

但**采集与投递主路径仍是双轨**：

| 层 | B 站 | 知识星球 |
| --- | --- | --- |
| 协议解析 | `model.Dynamic` | `model.Content` |
| 采集 runtime | `service.Engine` 五 loop | `zsxq.Collector` 两 worker |
| 归档入口 | `RecordDynamics` / `RecordFeedDynamics` | `ArchiveContentAndEnqueue` |
| 评论策略 | 最近 N + `CommentTarget` | 全量主题重扫 |
| 媒体 | `media.Downloader`（Dynamic 图片） | `media.AttachmentDownloader` |
| 账号 | QR + Cookie session | Cookie/token 导入 |
| 投递载荷 | Outbox 直接塞 `Dynamic` | Content 先转回 `Dynamic` 再入队 |

结果是：

1. 两个平台内聚差：同一业务语义（归档、baseline、评论通知、系统告警）有两套入口。
2. 互相耦合：ZSXQ 为了投递必须会“假扮 Dynamic”；系统告警也走 `RecordDynamics("system")`。
3. 扩展成本高：第 3 个数据源今天不能只加 adapter，还要改硬编码平台分支、设置字段、账号路由，甚至再复制一条投递转换。

产品目标已经明确：

- **近期会接第 3 个数据源**
- **平台特有能力保留为可选 capability**，不强迫所有平台实现 B 站 feed/relation 或 ZSXQ 全量回补

因此需要一层**足够高、但不过度**的统一抽象：统一事实与核心流水线，保留协议与调度策略的平台特化。

## 2. 设计原则

1. **统一事实，不统一协议。**
   Source/Content/Comment/Outbox 只有一份；登录、分页、风控、发现策略留在 adapter。
2. **统一投递，不统一采集调度。**
   dispatcher/notify 平台无关；每个平台自己的 poll loop / backfill / relation 可特化。
3. **小接口 + 可选 capability。**
   禁止 `PlatformClient` 上帝接口；不支持的能力就是不实现，不做空方法。
4. **adapter 只产出中立领域对象。**
   adapter 不得拼 channel 列表、不得直接理解 SMTP/机器人协议、不得维护第二套 Outbox。
5. **破坏性收敛，不双写兼容。**
   Dynamic 不再作为跨平台投递模型；迁移时一次性转换或明确失败。
6. **不建空包森林。**
   不为 `archive/`、`accounts/`、`history/`、`ports/` 先铺目录。包因代码移动而出现，不因“架构图好看”而出现。
7. **正确性先于扩展骨架。**
   baseline sticky、outbox 可调度性、Gate/媒体预算等共享坑必须先修，避免第 3 源复制缺陷。

## 3. 目标架构

```text
┌──────────────────────────────────────────────────────────┐
│ Platform Adapters（可插拔，允许高度特化）                   │
│                                                          │
│  bilibili/     zsxq/     <newplatform>/                  │
│  - 协议 client / 登录 / 分页 / 风控 / 严格解析              │
│  - 自己的 Runner 与可选 capability                         │
└────────────────────────────┬─────────────────────────────┘
                             │ 只产出 Content / CommentNode /
                             │ Attachment / Source / Account
                             ▼
┌──────────────────────────────────────────────────────────┐
│ Core Pipeline（唯一，平台无关）                            │
│                                                          │
│  Source 管理                                             │
│  Content 归档 + seen                                     │
│  Comment 树同步 + sticky baseline                        │
│  Outbox 中立快照 + Dispatcher                            │
│  Notify adapters（邮件 / Graph / 机器人）                 │
│  Media 目录约定 + 清理                                    │
│  管理查询 API                                            │
└────────────────────────────┬─────────────────────────────┘
                             │
                             ▼
                        SQLite data.db
```

### 3.1 依赖方向

```text
web
  ├─ sources.Admin
  ├─ 账号入口（按 platform 分发到模块）
  ├─ delivery 管理 / 查询
  └─ 不直接编排归档事务

app（composition root）
  ├─ 注册 []PlatformModule{bilibili, zsxq, ...}
  ├─ 启动各 module.Runner
  ├─ 启动共享 delivery.Dispatcher / media.Cleaner / web
  └─ 注入中立 Archive/Store 能力

bilibili ──► Archive（中立）
zsxq     ──► Archive（中立）
newplat  ──► Archive（中立）

notify   ◄── ContentSnapshot / CommentNotification / AI / System
             （不再接收跨平台 Dynamic）
```

### 3.2 明确不统一的部分

| 保留平台特化 | 原因 |
| --- | --- |
| 登录方式 | QR/Cookie/OAuth 完全不同 |
| 风控与 RequestGate | 每平台独立限流/暂停 |
| 分页与水位 | space seen、feed baseline、ZSXQ watermark/backfill |
| 评论坐标与刷新策略 | B 站 type+oid 最近 N；ZSXQ 全主题重扫 |
| 内容发现路由 | B 站关注关系决定 feed/space |
| exclusive / 付费可见性 baseline | B 站产品语义 |
| 媒体类型与预算 | 图片 10MiB vs 多类型大附件 |
| 自动 AI 资格 | 当前仅 B 站顶层视频 + BVID |
| 触发角色文案 | UP主 vs 星球主 vs 未来角色 |

**规则：共享事实与投递，不共享协议、节奏和发现规则。**

## 4. 实施前基线与已完成收敛

本节的“双轨”描述保留为实施前问题清单。当前实现已完成：v13 中立 Outbox 迁移、B 站 adapter 的 `Content/Attachment` 出口、共享附件下载入口、`platform.Module/PlatformMeta` 注册、按模块启动 Runner、按模块分发账号注销，以及平台中立的来源变更回调。

### 4.1 已经统一，应保留

- 身份：`Platform` 作为所有外部 ID 命名空间
- 事实表：`sources/contents/attachments/comment_nodes/seen_items/outbox`
- 写事务：`archiveContentTx`、`ArchiveContentAndEnqueue`、`SyncCommentTree`
- 管理读模型：`/api/v4/sources`、`/api/v4/contents`
- 单 Outbox + 单 dispatcher
- 媒体根路径：`media/{platform}/{source_id}/{content_id}/`
- 来源管理薄服务：`sources.Admin`

### 4.2 实施前双轨（现已收敛）

- `Dynamic` 只保留为 B 站解析 DTO，离开 adapter 前转换为 `Content/Attachment/ContentSnapshot`
- 公共 `RecordDynamics*` 已删除；space/feed/exclusive 使用中立批量归档契约
- `service.Engine` 与 `zsxq.Collector` 继续拥有各自策略，但通过同一 `Module.Runner` 表启动
- 账号事实统一为 `PlatformAccount`；不同登录动作保留在各模块账号路由
- 媒体统一为 `AttachmentDownloader + MediaAuth`，预算和允许类型由平台调用方提供
- 运行设置继续使用具名的平台字段；这是明确保留的特化，不建立空想配置框架
- Outbox 载荷为 `content/comment/ai/system`，运行期无 Dynamic renderer
- 系统告警直接写 `system`，不创建伪内容
- 账号枚举、评论触发角色、Runner 和来源变更从平台注册表/元数据驱动

### 4.3 实施前新增第 3 源的扩展税

实施前至少要改：

1. 新 `foo/` client/collector/account
2. `app.Run` 再挂 runner
3. `model.Platform.Validate` / `SourceID` / source type 映射
4. `ListPlatformAccounts` 双平台循环
5. `SyncCommentTree` 触发角色分支
6. 评论摘要文案分支
7. Web 账号路由硬编码
8. `RuntimeSettings` 再加一组并行字段
9. OpenAPI + 前端 enum / 设置 / 登录页
10. 若走错路径：还要会假扮 `Dynamic` 才能通知

本方案目标：把 5–8 收成注册表/中立契约，消掉 10，使新增成本回到 **adapter 包 + 注册 + 少量配置/UI**。

## 5. 中立领域契约

### 5.1 权威事实类型（已存在）

继续以这些类型为唯一业务事实：

- `model.Platform`
- `model.PlatformAccount`
- `model.Source`
- `model.Content`
- `model.Attachment`
- `model.CommentNode`
- `model.CommentDigest`

身份规则：

```text
SourceID  = platform-specific stable form
ContentID = {platform}:content:{external_id}
CommentID = {platform}:comment:{external_id}
```

### 5.2 必须新增：中立投递快照

当前最大扩展阻塞是 Outbox 内容通知仍嵌入 `model.Dynamic`。

目标形状：

```go
type ContentSnapshot struct {
    Platform     Platform
    SourceID     string
    SourceName   string
    ContentID    string
    ExternalID   string
    AuthorID     string
    AuthorName   string
    Type         ContentType
    UpstreamType string
    Title        string
    Text         string
    URL          string
    PublishedAt  time.Time
    Stats        map[string]int64
    Links        []SnapshotLink
    Media        []SnapshotMedia // 创建时密封的本地相对路径与展示元数据
    // 可选扩展，仅当 adapter 提供时存在：
    Video        *SnapshotVideoMeta
    ForwardOf    *ContentSnapshot
}

type Delivery struct {
    ID      string
    Kind    DeliveryKind // content | comment | ai | system
    Content *ContentSnapshot
    Comment *CommentNotification
    AI      *AINotification
    System  *SystemAlert
    // schedule columns...
}
```

要求：

- `DeliveryKindDynamic` 破坏性改为 `content`（或等价中立名）
- `CommentNotification` 去掉 `UPUID/UPName` 硬语义，改为 `AuthorID/AuthorName/AuthorRole`
- `AINotification` 以 `ContentID/SourceID` 为主；`BVID` 仅作可选扩展
- Outbox payload 仍是**创建时不可变快照**，发送时不得回读可变 Content 重建

### 5.3 `model.Dynamic` 的归宿

`Dynamic` **降级为 B 站协议解析私有结果**：

- 可以暂时留在 `model`，最终迁到 `bilibili`
- 禁止再作为：
  - Outbox 载荷
  - notify 跨平台输入
  - ZSXQ/第三源归档输入
  - 系统告警载荷

B 站内部可以继续：

```text
ParseDynamicItem → (bilibili Dynamic)
  → bilibiliDynamicContent(...)
  → model.Content + attachments
  → Archive...
```

### 5.4 系统告警

独立 `DeliveryKindSystem`：

- 直接写 Outbox
- 不创建 `contents` 假行
- 不走 `RecordDynamics("system")`

## 6. Adapter 边界

### 6.1 模块注册

由 composition root 使用，放在轻量位置（例如 `platform/module.go` 单文件契约，避免大包）：

```go
type Module struct {
    Platform model.Platform
    Runner   Runner            // 必选：Run(ctx) error
    Accounts AccountRoutes     // 必选：登录/登出/状态投影入口
    // 可选 capability，nil 表示不支持
    SourceSync   SourceSync    // 从账号发现来源（ZSXQ 星球）
    ManualSource ManualSource  // 管理员手动添加（B 站 UP）
    AIBridge     AIEligibility // 哪些内容可自动 AI
    MediaAuth    MediaAuth     // 下载附加鉴权头
}
```

```go
type Runner interface {
    Run(ctx context.Context) error
}
```

app 装配：

```text
modules := []platform.Module{
    bilibili.NewModule(...),
    zsxq.NewModule(...),
    // newplatform.NewModule(...),
}
for _, m := range modules {
    g.Go(m.Runner.Run)
}
```

### 6.2 采集出口：中立 Archive

adapter 只依赖消费方定义的小接口，而不是整个 `*state.Store`：

```go
type Archive interface {
    ListSources(model.Platform) ([]model.Source, error)
    PutSource(model.Source) error
    PlatformAccount(model.Platform) (model.PlatformAccount, error)
    SetPlatformAccountStatus(model.Platform, model.AccountStatus, string) error

    ArchiveContent(content model.Content, attachments []model.Attachment) error
    ArchiveContentAndEnqueue(content model.Content, attachments []model.Attachment, notify bool) error
    MarkContentDeleted(contentID string, deletedAt time.Time) error

    SyncCommentTree(
        content model.Content,
        nodes []model.CommentNode,
        complete bool,
        baseline bool,
        batchID string,
    ) ([]model.CommentDigest, error)

    CommentBaselineReady(platform model.Platform, contentID string) (bool, error)
    // sticky: 一旦 true，不得因 incomplete/error 回转 false
}
```

这与现有 `zsxq.collectorStore` 同方向。B 站采集侧应收敛到同一组方法，而不是继续扩张公共 `RecordDynamics`。

### 6.3 可选 capability

| Capability | 含义 | B 站 | ZSXQ | 第 3 源 |
| --- | --- | --- | --- | --- |
| `ManualSource` | 管理台手动创建来源 | 有 | 无 | 按产品 |
| `SourceSync` | 账号可见来源刷新 | 无 | 有 | 按产品 |
| `SpacePoll` | 按 source 拉内容 | 有 | 有 | 通常有 |
| `AggregateFeed` | 账号综合 feed | 有 | 无 | 少见 |
| `RelationRouting` | 关注关系路由 feed/space | 有 | 无 | 少见 |
| `ExclusiveBaseline` | 付费可见性二次 baseline | 有 | 无 | 少见 |
| `RecentNComments` | 最近 N 条评论跟踪 | 有 | 无 | 可选 |
| `AllContentComments` | 刷新全部已归档评论 | 无 | 有 | 可选 |
| `FullBackfill` | 启源后历史回补不通知 | 无 | 有 | 可选 |
| `MediaAuth` | 下载附加 token/header | 弱 | 有 | 可选 |
| `AIEligibility` | 自动 AI 资格判断 | BVID 视频 | 无 | 可选 |

**core 不为 `AggregateFeed` 建通用状态机。**
这些能力由 adapter 内部消化；core 只认识归档、评论同步、投递、查询。

### 6.4 角色与文案注册

把硬编码 if 收敛为平台元数据：

```go
type PlatformMeta struct {
    Platform        model.Platform
    DisplayName     string // "B站" / "知识星球"
    ContentNoun     string // "动态" / "内容"
    TriggerRoles    []model.AuthorRole
    TriggerLabel    string // "UP主" / "星球主"
    AuthorLabel     string // 内容作者事实标签："UP主" / "作者"
    ManualSource    bool
    SourceSync      bool
}
```

`SyncCommentTree` 用 `TriggerRoles` 判断是否产生评论通知，不再写死 UP/Owner 两个分支。

## 7. 现有平台如何映射

### 7.1 B 站 adapter

**内部保留特化：**

- QR 登录
- relation loop
- aggregate feed + space reconcile
- exclusive baseline
- recent-N comment targets
- Dynamic 解析类型
- BVID AI eligibility

**对 core 的出口：**

```text
ParseDynamicItem
  → 内部 Dynamic
  → Content + Attachments
  → ArchiveContentAndEnqueue / Comment sync
```

`RecordDynamics` / `RecordFeedDynamics` 只作为过渡封装，最终从公共核心 API 中删除。

### 7.2 知识星球 adapter

**已接近目标形态：**

```text
parse → Content + Attachments
  → localize
  → ArchiveContentAndEnqueue

comments → CommentNode[]
  → SyncCommentTree
```

**必须修正：**

1. comment baseline sticky
2. 去掉对 `RecordDynamics` 的系统告警依赖
3. collector 接口不再包含 Dynamic API
4. 触发角色通过 PlatformMeta 表达

### 7.3 共享 core

- source 启停/删除（含媒体 cleanup task）
- content/comment 查询
- Outbox due/dispatch/retry
- channel 管理
- 中立 snapshot 渲染
- 平台无关 metrics 标签

## 8. 第 3 平台接入契约

### 8.1 必须实现

1. `Platform` 常量与校验白名单扩展
2. 稳定 `SourceID` / `ContentID` / `CommentID`
3. 协议 client + 严格 parser → `model.Content`（未知结构失败）
4. 账号接入并写入 `PlatformAccount`
5. `Runner`：自有 poll loop，只调用中立 Archive
6. 若支持评论：产出 `[]CommentNode`，遵守 sticky baseline
7. 媒体走统一目录；如需鉴权实现 `MediaAuth`
8. 管理入口：账号路由 + 来源策略（手动或 sync）
9. 独立 RequestGate 配置（具名字段即可，不必先做通用配置框架）
10. 注册 `PlatformMeta` 与 `Module`

### 8.2 禁止实现

1. 第二套内容表或第二套 Outbox
2. 把协议 JSON 直接塞进 notify
3. 在 web handler 编排归档事务
4. 为实现“统一”而空实现 feed/relation/AI
5. 猜测未知字段推进水位
6. 为投递假扮 `model.Dynamic`

### 8.3 目标接入成本

**通常只需改：**

- 新 adapter 包
- app 注册一行
- Platform 校验 / Source type 映射
- 管理台账号与设置最小 UI
- OpenAPI enum

**通常不必改：**

- Outbox schema / dispatcher
- notify channel 协议
- Content 查询 API
- 媒体 cleanup
- sources CRUD 主逻辑

## 9. 增量迁移阶段

每一阶段结束后主线可发布。不建空包，不做长期双写。

### 阶段 0：正确性前置（已完成）

先堵住会被第 3 源复制的共享缺陷：

1. 渠道禁用/启用与 outbox 可调度性同事务收敛
2. ZSXQ 评论 baseline sticky
3. Gate 额度绑定 Body.Close
4. 媒体进入平台 Gate，并具备明确 timeout
5. 运行时设置提交一致性

**验收：** 现有双平台行为正确，再开始中立化。

### 阶段 1：投递中立化（已完成）

目标：core 投递不再认识 Dynamic。

1. 引入 `ContentSnapshot` / `DeliveryKindContent`
2. 先改 `contentDeliverySnapshotTx`：Content 路径直接入中立 snapshot
3. B 站 `RecordDynamics` 出站也改为同一 snapshot 形状
4. `notify.ContentMessage(snapshot)`；平台文案走 PlatformMeta
5. Comment/AI 字段中立化
6. 系统告警独立 kind
7. Web/WS preview 改读 snapshot

**载荷迁移策略（破坏性）：**

- 升级前停进程
- 启动/迁移时一次性转换未完成 outbox payload
- 转不了的任务进入 blocked，并留下明确错误
- 之后只认 snapshot，不保留长期 compat renderer

**验收：**

- ZSXQ 不再 Content→Dynamic→notify
- 假想最小平台只要能造 Content 就能发通知
- 相关包测试全绿

### 阶段 2：B 站采集出口收敛到 Content（已完成）

1. B 站解析结果保留内部类型
2. space/feed/exclusive 最终都走 Content 归档 API
3. 公共 `RecordDynamics*` 删除或降为测试辅助
4. 自动 AI 基于 Content + AIEligibility，不再读跨平台 Dynamic

**验收：**

- 生产采集出站无跨平台 Dynamic
- feed/relation/exclusive 行为不变

### 阶段 3：Module 注册（已完成）

1. 落地 `platform.Module` / `PlatformMeta`
2. `bilibili.NewModule` / `zsxq.NewModule`
3. `app.Run` 按模块表启动 Runner
4. Web 账号 API 按 platform 分发
5. `sources.Admin` 回调改为 `OnSourceChanged(platform)`
6. 设置中的平台限流：
   - 若第 3 源已确定，直接加具名字段
   - 不要先做空想通用配置框架

**验收：**

- 新增平台 = 新包 + 注册 + 少量 API/UI
- dispatcher/notify/core archive 无需业务 if 平台

### 阶段 4：媒体与评论策略收口（已完成）

1. 媒体入口统一为 Content/Attachments + 可选 MediaAuth
2. 保留平台差异化预算与允许类型
3. 评论发现策略完全留在 adapter
4. core 只提供 sticky baseline 的 `SyncCommentTree`

### 阶段 5：第 3 平台落地（等待产品平台选型，不属于本次架构落地）

用真实第 3 源验证契约：

1. `client.go` / `parse.go` / `collector.go` / `account.go`
2. 严格解析测试
3. baseline/backfill 策略测试
4. app 注册
5. 最小管理台账号与来源 UI

可选维护性拆分：当 delivery 从 Engine 抽出确有收益时，再移动代码创建 `delivery` 包。
**Engine 拆除不是第 3 源的前置条件。**

## 10. 关键文件

| 区域 | 文件 | 变化 |
| --- | --- | --- |
| 领域 | `model/platform.go`, `model/types.go` | ContentSnapshot；Delivery 去 Dynamic；PlatformMeta |
| 归档 | `state/platform.go`, `state/content.go`, `state/store.go` | 统一出站 snapshot；收敛 RecordDynamics |
| 投递/渲染 | `service/engine.go` deliver 部分, `notify/notify.go` | 中立渲染 |
| B 站 | `bilibili/*`, `service/engine.go` 采集部分 | 出口转 Content；特化保留 |
| ZSXQ | `zsxq/collector.go`, `zsxq/parse.go`, `zsxq/account.go` | baseline sticky；去 Dynamic 依赖 |
| 契约 | 新增轻量 `platform` 契约文件 | Module / Meta / Archive 接口 |
| 装配 | `app/app.go` | 模块注册 |
| 来源 | `sources/admin.go` | 平台中立回调 |
| Web | `web/api_v4.go`, `web/ws.go` | snapshot/DTO；账号分发 |
| 媒体 | `media/media.go`, `media/asset.go` | 统一入口 + 可选鉴权 |
| 文档 | `requirements-and-design.md`, 本文件 | 保持同步 |

## 11. 风险

1. **Outbox payload 形状变更是破坏性的。**
   必须停机转换，不做长期双读。
2. **B 站 feed/relation 与归档交织深。**
   阶段 2 需要 space/feed/exclusive 回归。
3. **AI 当前绑 BVID。**
   中立化后应明确为 bilibili AIEligibility，而不是假装所有平台都有自动 AI。
4. **若跳过阶段 1 直接接第 3 源。**
   新源仍会被逼假扮 Dynamic，或复制投递转换层，扩展收益落空。
5. **过早拆 Engine / 铺空包。**
   会把“可扩展”做成搬家项目，而不是边界收敛。

## 12. 非目标

- 微服务 / 外部消息队列 / 多数据库
- 统一登录 UX
- 统一所有 poll 算法
- 通用 Plugin 热加载 / 反射 DSL
- Clean Architecture 目录树
- 多实例 claim/lease（当前单进程不需要）
- 为还没出现的平台预造配置框架

## 13. 验收标准

### 对扩展性

1. 新增数据源的主工作量在 `newplatform/` adapter
2. core 流水线（archive/outbox/dispatch/notify/query）无新的业务性 `switch platform`
3. 展示文案/触发角色通过 PlatformMeta 扩展，不复制通知链路
4. 不存在 Content 与 Dynamic 双写主路径

### 对内聚与解耦

1. adapter 不依赖 notify channel 协议
2. dispatcher 不依赖 bilibili/zsxq client
3. web 不编排 archive+seen+outbox 事务
4. B 站 feed/relation/exclusive/recent-N/AI 仍隔离在 bilibili
5. ZSXQ 回补/全量评论仍隔离在 zsxq

### 对正确性

1. 评论 baseline sticky
2. 禁用渠道不占满 due 批次
3. 平台 Gate 约束完整请求生命周期，媒体不绕过
4. 设置更新不出现未说明的 DB/运行态分裂

## 14. 验证计划

### 自动化

- `make test` / `make test-race` / `make vet`
- 新增测试：
  - Content → ContentSnapshot → 各 channel render
  - B 站解析 → Content 归档 → Outbox 无 Dynamic
  - ZSXQ baseline sticky
  - PlatformMeta 触发角色
  - in-process fake platform module：最小 Content poll 可出通知
  - Module 注册生命周期

### 手动

- B 站 UP + ZSXQ 星球同时启用，验证内容/评论/图片通知
- 禁用渠道后其他渠道仍前进
- fake/第 3 源：创建来源 → 归档 → 邮件/机器人收到中立文案

## 15. 执行顺序

```text
阶段0 正确性修复
    ↓
阶段1 Outbox/Notify 中立化     ← 扩展最大杠杆
    ↓
阶段2 B站采集出口转 Content
    ↓
阶段3 Module / PlatformMeta 注册
    ↓
接入第3源 adapter
```

若第 3 源时间极紧：
**最少完成阶段 0 + 阶段 1**。
否则新源仍会落入 Dynamic 假扮或双投递转换，统一抽象名存实亡。

## 16. 结论

多数据源可扩展性的关键不是“再写一个通用爬虫框架”，而是：

1. **事实模型已经统一，必须守住**
2. **投递载荷必须中立，立刻去掉 Dynamic 重力井**
3. **采集 adapter 用小接口 + 可选 capability 接入**
4. **平台特化策略留在 adapter，不渗入 core**
5. **用模块注册而不是硬编码 if 增长平台**

这样可以得到：

- 更高内聚：每个平台包消化自己的协议与调度
- 更低耦合：core 只依赖 Content/Comment/Outbox 契约
- 可扩展：第 3 源主要是加 adapter，而不是改整条流水线
