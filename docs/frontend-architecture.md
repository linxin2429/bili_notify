# 前端架构

本文定义管理台的目标架构、依赖约束和验收门禁。它描述的是长期不变量，而不是某一版组件的代码形状；具体迁移记录应写入 ADR 或提交说明，不能通过向这里添加兼容分支来绕过约束。

## 1. 从问题本质出发

管理台的核心任务不是在浏览器中重建一套业务系统，而是让管理员可靠地观察和操作服务端系统。由此得到三个基本事实：

1. 运行状态、UP、渠道、投递、设置和历史记录都由服务端产生，服务端拥有最终解释权。
2. REST 和 WebSocket 是同一份服务端事实的两种传输路径，不是两个可以独立修改页面状态的数据源。
3. 页面性能主要由首屏需要执行多少 JavaScript、一次状态变化影响多少订阅者以及是否重复传输数据决定，不由组件名称或手写 `memo` 的数量决定。

因此，前端不再维护可独立演化的全局 dashboard 副本。REST 提供可恢复、可验证的资源读取，TanStack Query 保存唯一的浏览器远程状态缓存，WebSocket 只通知缓存失效。React 负责把状态投影成界面，不推导服务端领域事实。

## 2. 技术基线

- React 19 与 React Compiler：保持 React 生态，同时由编译器完成安全的局部记忆化。
- Vite：生产静态资源构建和路由级拆包，产物继续由 Go `embed` 嵌入。
- React Router Data Mode：拥有 URL、路由匹配、懒加载和路由错误边界。
- TanStack Query：唯一的远程状态缓存，负责去重、取消、失效、重试和过期策略。
- OpenAPI：HTTP DTO、错误和 WebSocket envelope 的唯一传输契约来源；生成 TypeScript 传输类型，轻量 Fetch adapter 负责发起请求。
- 语义化受控表单 + Zod：只管理表单草稿、转换、运行时响应校验和客户端即时反馈；服务端仍重复执行所有安全校验。
- React Aria Components + CSS Modules + CSS Variables：提供可访问交互语义和零运行时样式系统。
- Vitest、Testing Library、Playwright：分别覆盖纯逻辑、组件集成和真实浏览器工作流。
- ESLint flat config、typescript-eslint、React Hooks `recommended-latest` 和 boundaries：在提交前阻止 Hooks、Compiler 和依赖方向回退。

不引入 Redux 或 Zustand 保存远程状态，也不为无 SEO 需求的同源管理台引入 SSR、RSC 或 Next.js。

## 3. 状态归属

状态首先按“谁能给出最终答案”分类，再决定存放位置。

| 状态 | 权威 | 浏览器位置 | 规则 |
|---|---|---|---|
| 会话、CSRF、初始化状态 | 服务端 | session query；CSRF 仅内存 | 401 统一失效，不持久化秘密 |
| 运行状态、设置、UP、渠道、投递 | 服务端 | TanStack Query cache | 页面不得复制到 Context 或全局 `useState` |
| 动态、评论、审计查询结果 | 服务端 | 以规范化筛选参数为 key 的 query | 请求可取消，游标属于 URL |
| Tab、筛选、关键字、时间范围、游标 | URL | Router search params | 可分享、可恢复、后退可重放 |
| 表单草稿、Dialog、展开项、灯箱索引 | 当前用户交互 | 最近的模块组件 | 不进入远程缓存 |
| 主题偏好 | 用户设备 | localStorage + 根元素属性 | 只能存外观偏好，不存管理数据 |

浏览器可以格式化时间、选择展示文案、筛选已经取得的视图数据，但不能重新计算 `ready`、投递状态、服务端 `updated_at` 或其他领域结论。

## 4. 应用结构和依赖方向

目标目录如下：

```text
src/
  app/                 # providers、router、shell、根错误边界、主题装配
  pages/*Page.tsx      # 懒加载路由入口和跨模块页面组合
  modules/<domain>/    # query、mutation、表单和领域 UI
  shared/
    api/               # HTTP client、错误、query client、生成代码
    realtime/          # WebSocket 连接和失效映射
    ui/                # 无业务语义的可访问 UI primitives
    lib/               # 时间、URL、展示等纯函数
  styles/              # reset、全局样式和 design tokens
```

唯一合法的层级方向是：

```text
app → pages → modules → shared
```

具体规则：

- `shared` 不得导入 `modules`、`pages` 或 `app`。
- `modules` 只能导入自身和 `shared`，不能直接导入另一个模块的内部文件。
- 跨模块协作由 page 组合；确实需要共享的领域能力通过模块 `index.ts` 公共入口暴露。
- `pages` 不承载请求生命周期、领域 reducer 或通用 UI 实现，只组合模块和页面布局。
- `app` 只装配 provider、路由、会话边界、导航和全局错误处理。
- 新目录一旦进入目标结构就立即受 ESLint boundaries 约束；迁移完成后开启 unknown-file 严格检查，删除所有旧式顶层文件。

## 5. 路由与页面生命周期

路由表是路径、导航元数据、权限、懒加载和错误边界的唯一来源。业务页面必须使用 route-level lazy import，不能把 History 富媒体、渠道表单或设置表单打进首屏入口。

Router loader 只用于以下工作：

- 解析和规范化 URL；
- 在导航前调用 `queryClient.ensureQueryData` 预热缓存；
- 返回重定向或路由级 HTTP 语义。

Loader 不保存第二份业务数据。组件始终通过相同的 query options 订阅 Query cache，避免 Router 和 Query 成为双重缓存。

每个页面独立提供 pending、empty、error、stale 和 retry 状态。一个不相关资源失败不能阻塞整个管理台。

## 6. REST、Query 与 mutation

Query key 必须由集中式 factory 产生，并包含所有会改变响应的输入：

```ts
session                  ['session']
runtime                  ['runtime']
ups                      ['ups']
channels                 ['channels']
deliveries(filters)      ['deliveries', normalizedFilters]
history(kind, filters)   ['history', kind, normalizedFilters]
auditLogs(filters)       ['audit-logs', normalizedFilters]
settings                 ['settings']
```

页面不得直接调用 `fetch`，不得分别维护 `busy/items/error/total`。请求函数必须消费 `AbortSignal`；筛选变化、路由离开和 query 取消要真正终止网络工作，而不只是禁止晚到的 `setState`。

mutation 默认不做 optimistic update。成功后按服务端返回结果精确替换资源，或使受影响 query 失效；失败保持原缓存并显示结构化错误。只有操作可逆、冲突概率低、具备完整 rollback 且有并发测试时，才能显式采用乐观更新。

mutation 完成前应取消同 key 的旧查询，完成后重新验证服务端事实。这样旧 REST 响应不能覆盖较新的写操作。

## 7. WebSocket invalidation 协议

WebSocket 是低延迟增强，不是首屏数据源。HTTP 正常而 WebSocket 被代理或网络阻断时，所有读取和管理操作仍必须可用。

实时消息只表达：

- 当前 revision；
- 哪些资源 topic 已失效；
- 是否要求重新同步。

客户端不通过 reducer 把 patch 合成全局 snapshot。收到失效 topic 后，它只取消并 invalidate 对应 query；TanStack Query 负责请求去重。突发事件应在服务端合并 topic，客户端也要避免为同一个活跃 query 产生并发请求。

连接状态机：

```text
REST 首屏成功 ──> readable
WebSocket 同步成功 ──> realtime
realtime 断开 ──> 保留最后数据 + degraded polling
协议解析失败/revision 缺口 ──> stale + 关闭连接 + REST 重新同步
重新同步完成 ──> realtime
```

降级期间只轮询需要低延迟的活跃资源；后台页面和未订阅资源不轮询。重连成功后先执行一次同步，再停止轮询。界面必须同时用文字显示“实时”“轮询中”或“数据可能过期”，不能只改变颜色。

## 8. 会话和错误模型

会话显式区分 `loading`、`anonymous`、`authenticated` 和 `error`。启动失败必须展示可重试错误页，不能无限显示 loading。

统一前端错误结构：

```ts
interface ApiError {
  kind: 'http' | 'network' | 'timeout' | 'contract' | 'aborted'
  status?: number
  code?: string
  message: string
  requestId?: string
  fields?: Record<string, string>
  retryable: boolean
}
```

处理规则：

- 401：停止 WebSocket、清除全部受保护 query 和 CSRF，进入匿名状态；旧管理数据不得短暂回显。
- 403：显示权限或 CSRF 错误，不假装成网络失败。
- 409：保留用户草稿，提示资源冲突并允许重新读取。
- 422：把 `fields` 合并到对应表单字段，同时保留表单级信息。
- 429：展示限流信息和允许重试的时间。
- 网络、超时和 5xx：根据幂等性决定重试；mutation 不自动盲目重放。
- contract：停止消费该响应，显示“服务端响应不符合契约”和 request ID；禁止静默丢弃坏行。
- aborted：属于正常取消，不弹 Toast、不记录为用户可见故障。

全局错误边界处理不可恢复的渲染错误；页面错误边界处理局部路由错误；Query 错误由离数据最近的页面状态呈现。Toast 只用于操作反馈，不替代页面内可恢复错误。

## 9. OpenAPI 契约

OpenAPI 是传输层唯一事实来源。Go 领域模型、数据库模型和浏览器展示模型都不能冒充 HTTP DTO。

- 规范定义请求、响应、分页、错误和 WebSocket envelope。
- TypeScript 传输类型从规范生成，生成目录禁止手工编辑；手写 Fetch adapter 只能消费这些类型，不能再声明一份 DTO。
- 运行时在不可信边界执行 schema 校验，TypeScript 编译期类型不能代替运行时验证。
- `npm run api:generate` 必须是确定性的；`make frontend-contract-check` 在生成后检查工作树，任何 diff 或未跟踪生成文件都失败。
- API 破坏性更新采用一次性切换，删除旧 DTO、旧 client 和兼容 adapter。

生成器落地后，若新增输出目录，必须同步更新 `OPENAPI_GENERATED_PATHS`，否则门禁并没有覆盖完整契约。

## 10. 表单边界

渠道使用 `type` 判别联合，每种渠道拥有独立 schema、默认值和表单组件。SMTP TLS、OAuth 与 webhook 具有不同领域语义，不构建 `Record<string, string>` 驱动的万能表单引擎。

设置表单由一个 schema 完成字符串转换、范围、字段关联和重试顺序校验。输入控件的 `min/max/helperText` 只负责交互提示，不能成为第五份规则来源。服务端 422 字段错误与客户端错误使用同一字段路径显示。

秘密字段永不写入 URL、Query 持久化、localStorage、日志或错误详情。服务端不回显的秘密用“保持不变/替换”语义表达，不能用假值填充输入框。

## 11. 设计系统

样式使用 CSS Variables 表达颜色、间距、圆角、字体、层级和动画；亮色/暗色通过根元素 `data-theme` 切换，不创建运行时主题对象，也不因切换主题重新执行整棵 React 树。

React Aria 负责 Dialog、Select、Tabs、Tooltip、Switch、NumberField 等需要焦点、键盘和 ARIA 状态机的组件。纯布局使用语义 HTML、CSS Grid/Flex 和 CSS Modules，不为了 `Stack`、`Box` 再引入运行时组件层。

只包装行为确实需要统一的 primitives：Button、Dialog、Field、Select、Tabs、Alert、Toast、PageState、StatusBadge。共享组件不接收领域对象，不发业务请求。

最低交互要求：

- 主要触控区域至少 44px；
- 所有核心操作可用键盘完成；
- Dialog 正确锁定焦点，并在关闭后恢复到触发元素；
- 状态不只依靠颜色表达；
- 尊重 `prefers-reduced-motion`；
- 响应式布局优先由 CSS media/container query 完成，JS 只处理真正不同的行为。

## 12. 性能预算

预算约束浏览器实际需要下载和解压的生产 JavaScript：

| 范围 | 最大 gzip |
|---|---:|
| 首屏入口及其静态 imports 总和 | 120 KiB |
| 普通业务路由增量及其非首屏静态 imports | 40 KiB |
| History 路由增量及其非首屏静态 imports | 60 KiB |

`npm run build` 生成 Vite manifest，`npm run bundle:check` 使用 Node `zlib` level 9 对实际 JS 文件重复计算 gzip 大小。它会输出参与每项计算的文件并真实失败；不允许以旧实现超限为由返回成功，也不允许没有数据就提高预算。

所有业务页面必须成为 manifest 中的动态路由入口。共享 chunk 只在首屏或对应路由真正引用时计入。生产 sourcemap 保持关闭，CSS 不包含在 JS 预算中，但仍应通过浏览器指标观察。

只有测量证明存在瓶颈时才引入虚拟列表、Worker 或其他复杂优化。每页几十条记录时，先解决全量 snapshot、无关订阅和运行时 CSS，而不是提前虚拟化。

## 13. 测试矩阵

| 层次 | 必测内容 |
|---|---|
| 纯逻辑 | URL schema、query key、错误映射、表单转换、主题解析、日期展示 |
| Query 集成 | 请求去重、AbortSignal、mutation 失效、401 清理、字段错误、旧响应竞态 |
| Realtime | topic 映射、突发合并、断线轮询、revision 缺口、非法协议、重连同步 |
| 组件 | pending/empty/error/stale、表单键盘操作、Dialog 焦点、移动布局 |
| 契约 | REST 和 WebSocket 样例均通过生成 schema，坏响应整项失败而非丢行 |
| E2E | 初始化/登录、资源管理、历史筛选、会话过期、WebSocket 阻断与恢复、明暗主题 |
| 可访问性 | axe、键盘完整路径、焦点恢复、屏幕阅读器名称、减少动画 |
| 性能 | manifest gzip 预算、首屏无 History/MUI 代码、页面只请求自身资源 |

特别需要长期保留四个竞态场景：旧 GET 不能覆盖 mutation 后数据；WebSocket 永久失败时 REST 仍完整可用；非法实时消息触发 stale 和全量修复；任意受保护请求 401 后旧缓存不再可见。

前端四项覆盖率保持不低于 80%。覆盖率是遗漏风险的下限，不代替行为断言；不通过排除核心文件来提高数字。

Vitest 的测试转换不启用 React Compiler，生产构建和开发模式仍启用。Compiler 会把一个作者函数改写成 memo cache 状态机；V8 再把这些生成分支映射回源文件，会把编译器实现细节误算为业务分支，并随编译器版本变化而漂移。单元覆盖率因此衡量作者代码，Compiler 的接入正确性分别由 `react-hooks` 的 Compiler 规则与生产构建门禁验证。这不排除任何生产模块，也不改变运行语义的断言范围。

## 14. 工程门禁顺序

最终 CI 的前端门禁固定为：

```text
npm ci
OpenAPI generate + clean-tree check
typecheck
ESLint (typed + Hooks/Compiler + boundaries)
unit coverage
production build
bundle budget
Playwright
```

`make frontend-quality` 实现前七步的严格顺序。OpenAPI 生成器尚未落地或入口仍超过预算时，该命令应明确失败；不得添加静默 skip。迁移完成并且两个严格 target 都通过后，把 `ci-check` 现有的独立前端依赖替换为 `frontend-quality`，Playwright 继续消费同一份生产构建产物。

## 15. 明确禁止

- 用 Context、Redux、Zustand 或顶层 `useState` 复制服务端资源。
- 让 REST、WebSocket 和 mutation reducer 共同写一份 dashboard 对象。
- 在浏览器推导服务端领域事实或伪造服务端时间。
- 让 WebSocket 成为登录后首屏的硬依赖。
- 静默丢弃契约不合法的列表项。
- 页面直接 `fetch`，或用 `cancelled` 布尔值代替请求取消。
- 跨模块导入内部文件、从下层反向导入上层，或建立全局万能 utils。
- 构建通用低代码表单引擎来抹平不同渠道的领域差异。
- 新增 MUI、Emotion 或其他运行时 CSS-in-JS。
- 引入微前端、SSR/RSC、GraphQL、离线业务缓存、WASM 或默认乐观更新。
- 为让 CI 变绿而提高 bundle 预算、关闭 Compiler 规则或排除核心覆盖文件。
- 保留旧 API、旧页面或旧状态模型的兼容适配层。

## 16. 完成定义

演进只有在以下条件同时成立时完成：

- 没有 `DashboardSnapshot`、全局 snapshot reducer 或页面级手写请求生命周期；
- 页面只订阅自己需要的 query，WebSocket 断开不影响管理功能；
- 401、网络、超时、契约、冲突和字段错误统一处理；
- API client 和 DTO 由 OpenAPI 确定性生成，生成后工作树无 diff；
- 目标目录全部受依赖边界约束，旧式顶层文件已删除；
- MUI 与 Emotion 完全移除，主题只切换 CSS Variables；
- 所有业务页面路由级拆包并满足 gzip 预算；
- 本文测试矩阵、现有黄金 E2E 和仓库完整门禁全部通过。
