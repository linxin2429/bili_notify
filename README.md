# Bili Notify

除动态采集与通知外，AI 工作台可把 B 站视频音频转成带时间位置的文字，并异步总结直接输入或转写得到的文本。耗时、依赖多且处理不可信媒体的工作由独立 Python Worker 执行；Go 主进程只负责任务持久化、鉴权、调度和管理 API，因此 Worker 暂停时普通采集与通知仍然可用，待处理 AI 任务保留在 SQLite 队列中。

Bili Notify 是一个单实例 Go 服务，通过已登录的平台账号归档 B 站 UP 主和知识星球内容，并可靠投递到 SMTP 邮件、Microsoft Outlook/Microsoft 365、钉钉、飞书和企业微信群机器人。统一领域主线是“平台账号 → 采集源 → 内容 → 完整评论树 → 持久 Outbox → 通知”；两个平台使用独立调度、限流、风控暂停和登录生命周期。React 管理台与 Go 后端通过 `/api/v4` 和同源 WebSocket 通信，设置、待投递通知、内容、附件及评论树保存在单一 SQLite 数据库。

> B 站没有为本项目提供稳定的官方采集接口。本项目使用登录后的网页接口，可能因接口变化、风控或平台规则而不可用；它不会绕过验证码、限流或风控。知识星球当前登录链路仍要求管理员从自己已登录的网页会话中导入 access token，但知识星球现已通过业务码 `1059` 拒绝 Cookie 驱动的非官方工具，并要求改用官方 OAuth Skill。当前版本会明确展示这一原因；要稳定恢复受影响账号的采集，后续必须把登录和采集迁移到官方 OAuth/MCP。请只归档你有权访问和保存的内容。

## 快速启动

不需要预先生成主密钥、管理员密码哈希或 TLS 私钥：

```bash
docker compose pull
docker compose up -d
docker compose logs bili-notify
```

或直接使用已发布镜像：

```bash
docker pull dengxinlin/bili-notify:latest
# 钉选版本更稳妥，例如 1.2.3 / 1.2 / 1
docker pull dengxinlin/bili-notify:1.2.3

docker run -d --name bili-notify \
  -e TZ=Asia/Shanghai \
  -p 8443:8443 \
  -v bili-notify-data:/data \
  --read-only --tmpfs /tmp:size=16m,mode=1777 \
  --security-opt no-new-privileges \
  --cap-drop ALL \
  dengxinlin/bili-notify:latest
```

镜像内嵌 IANA 时区数据（`time/tzdata`）。通过环境变量 `TZ` 选择服务器本地时区（Compose 默认 `Asia/Shanghai`）；未设置时回退为 UTC。通知文案、管理台时间展示与结构化日志中的时间字段均使用该时区。

主服务和 AI Worker 的镜像标签由 main 历史上的正式 git tag `vMAJOR.MINOR.PATCH` 同步发布：`MAJOR.MINOR.PATCH`、`MAJOR.MINOR`、`MAJOR` 与 `latest`。完整版本标签不可覆盖；发布工作流不使用构建缓存，重新执行全部门禁并分别冒烟测试两个本地镜像，经 `dockerhub-production` Environment 批准后才登录 Docker Hub。两个最终 digest 分别附带 SPDX SBOM、keyless Cosign 签名和 GitHub build provenance。合并到 `main` 不重复执行已由 PR 门禁完成的常规 CI，也不推送镜像。

日志会输出一次性 `setup_code`。浏览器访问 `https://localhost:8443`，接受首次自签名证书警告，然后输入初始化码并设置至少 12 字节的管理员密码。初始化完成后代码立即失效。

服务首次启动会在 `/data` 自动创建：

- `data.db`：运行状态、Outbox 与已采集内容档案（SQLite，启动时自动执行版本化迁移）；
- `master.key`：随机 AES-256 主密钥；
- `tls.pem`：十年有效的本地自签名 ECDSA 证书和私钥。

若数据目录中仍存在旧版 `state.db` 或 `content.db`，服务会拒绝启动（不自动导入）；请备份后换用全新数据目录。

这些文件只保存在 Docker 数据卷中。主密钥与数据库同卷可以实现无人值守重启，但不能防护整个数据卷同时被窃取的情况。生产环境需要可信证书时，可在服务前部署终止 TLS 的反向代理；应用自身始终使用 HTTPS/WSS。

首次登录后：

1. 在“概览”生成二维码并使用哔哩哔哩 App 扫码。
2. 添加至少一个通知渠道并发送测试通知。
3. 在“采集源”添加 B 站 UID。首次拉取只建立基线，不通知历史动态；基线内容仍会写入“历史”页。
4. 如需知识星球，在独立登录页导入网页请求中的 Session，再从账号可见列表添加星球。每个星球可独立采集全部主题，或填写一个或多个知识星球用户 ID、仅采集这些作者的主题；用户 ID 可从知识星球网页开发工具中主题响应的 `owner.user_id` 查看，作者名称只是便于辨认的备注。账号凭证与来源相互独立，更换账号或 token 不会删除或停用已有来源；已启用来源始终使用最新 token 采集。
5. 在“历史”中按平台、采集源、时间与关键字浏览统一内容，并展开完整嵌套评论树。所有用户评论都会归档；只有 B 站 UP 本人或知识星球星球主本人的新增节点生成通知，同一轮同一内容合并为一条摘要。
6. 如需 AI 功能，在“AI 设置”创建转写/总结模型配置档和总结提示词，然后在“AI 工作台”提交 BVID 或文本。若要让 B 站新视频动态自动按“转写 → 总结”处理，请把三个配置分别设为默认且保持模型启用，再到“设置”开启“自动处理新视频动态”；该开关默认关闭，知识星球音视频、首次基线和转发中嵌套的视频不会触发。结果可在 AI 工作台查看。配置档支持任意 OpenAI 兼容的 HTTPS Base URL、API Key 和模型名；API Key 加密保存且不会回传浏览器。

“设置”页可热更新 B 站、知识星球、附件预算、投递与日志参数。保存的是一份完整运行设置：后续任务立即读取新策略，正在执行的任务和已经排定的重试不会被取消或改写。环境变量只在空库首次启动时播种默认值，之后以 `data.db` 为准。B 站种子使用 `BILI_NOTIFY_BILIBILI_DYNAMIC_INTERVAL`、`BILI_NOTIFY_BILIBILI_REQUEST_RATE`、`BILI_NOTIFY_BILIBILI_REQUEST_CONCURRENCY` 等 `BILI_NOTIFY_BILIBILI_*` 名称；知识星球使用 `BILI_NOTIFY_ZSXQ_DYNAMIC_INTERVAL`、`BILI_NOTIFY_ZSXQ_COMMENT_INTERVAL`、`BILI_NOTIFY_ZSXQ_REQUEST_RATE`、`BILI_NOTIFY_ZSXQ_REQUEST_CONCURRENCY`、`BILI_NOTIFY_ZSXQ_RISK_PAUSE`、`BILI_NOTIFY_ZSXQ_ASSET_MAX_FILE_SIZE` 和 `BILI_NOTIFY_ZSXQ_ASSET_TOTAL_BUDGET`。旧变量名不会读取。

知识星球作者范围过滤发生在主题进入本地档案之前：未命中的主题不会保存、下载附件或通知，已归档主题的评论仍会完整同步。修改范围不会删除已有档案、重置水位或重新补采已经越过的历史页；未完成的历史回补从现有游标开始立即使用新范围。

观测接口默认监听容器内 `:9090`，只包含 `/healthz` 和 `/readyz`，Compose 默认不发布到宿主机。Metrics 通过 OTLP 发送到 OpenTelemetry Collector，由 Collector 在 `:9464/metrics` 上转换为 Prometheus/OpenMetrics。

## 通知渠道

管理台按渠道类型提供结构化表单。密码、Webhook、签名密钥及 OAuth 令牌只写入后端；浏览器只会收到“已配置”标记，不会读回秘密值。

### SMTP 邮件

填写主机、端口、TLS 模式、发件人和收件人；用户名和密码按 SMTP 服务要求填写。TLS 模式只允许 `tls`（465 隐式 TLS）或 `starttls`，不支持明文 SMTP。

### Microsoft Outlook / Microsoft 365

Microsoft 渠道通过 Microsoft Graph 与 OAuth 2.0 设备码授权发送邮件：

1. 在 Microsoft Entra 创建应用注册；个人 Outlook 账号需允许个人 Microsoft 账户。
2. 启用“允许公共客户端流”，添加委托权限 `Mail.Send` 和 `Mail.ReadWrite`。
3. 在控制台填写客户端 ID、租户和收件人，保存后点击“开始授权”。

`tenant` 可填写 `common`、`consumers`、`organizations`、租户 UUID 或租户域名。访问令牌和刷新令牌会加密保存并自动刷新。由于附件通过草稿和上传会话投递，从旧版本升级后必须重新授权 Microsoft 渠道。

### 群机器人

钉钉填写 HTTPS Webhook 与签名密钥，企业微信填写带机器人 key 的 HTTPS Webhook。飞书使用应用机器人，填写 App ID、App Secret 和目标群 Chat ID；应用需启用机器人、加入目标群，至少开通“以应用的身份发消息”（`im:message:send_as_bot`）权限，并在变更权限后发布新版本；图片和文件投递还需对应的上传权限。Webhook、签名密钥和 App Secret 都加密保存。

知识星球的图片继续按各渠道的图片能力展示；`file`、`audio`、`video` 附件作为普通文件投递。SMTP 不设置应用侧硬上限，Microsoft 支持至 150 MiB，飞书至 30 MiB，企业微信支持 5 B–20 MiB，钉钉暂不上传文件。文件未归档、丢失、为空或超限时，正文会列出跳过原因，其余正文和附件仍会发送。

## 运维

```bash
# 查看日志和初始化码
docker compose logs -f bili-notify

# 查看版本和命令
docker compose run --rm bili-notify --help

# 健康检查
docker compose exec bili-notify /bili-notify healthcheck
```

服务始终把结构化 JSON 写到 stdout，启用 OpenTelemetry 时同时通过 OTLP 发送。`category=system` 是系统运行日志，`category=audit` 是已成功写入 SQLite 的管理员操作日志；管理台“操作日志”页面可按操作、结果、时间、来源和请求 ID 查询。审计日志默认保留 180 天，Loki 中的系统日志保留 30 天。`setup_code` 仅保留在 stdout，不会发送到 Collector。

需要集中收集和查询系统日志时，设置 Grafana 密码并启动可选观测配置：

```bash
export GRAFANA_ADMIN_PASSWORD='使用独立的强密码'
# 单个完整 Compose 文件，启动应用和完整可观测栈
docker compose -f compose.full.yaml up -d

# 或者在基础 Compose 上叠加可观测配置
docker compose -f compose.yaml -f compose.observability.yaml --profile observability up -d

# Grafana 仅监听本机回环地址
ssh -L 3000:127.0.0.1:3000 your-server
# 浏览器打开 http://127.0.0.1:3000
```

主服务和 AI Worker 都使用 `BILI_NOTIFY_OTEL_*` 配置把 logs、metrics 和 100% 采样的 traces 发送到 OpenTelemetry Collector。Worker 在完整 Compose 中使用 OTLP HTTP/protobuf 和独立的 `service.name=bili-notify-ai-worker`；基础 Compose 默认禁用两个进程的 SDK。Collector 把日志写入 Loki、在 `:9464` 导出 Prometheus metrics、把 trace 写入 Tempo；Prometheus 和 Loki 保留 30 天，Tempo 保留 7 天。Collector 出口使用持久化队列与无限时重试，后端短暂故障不阻塞业务。Grafana 预置了 Prometheus、Loki、Tempo 数据源、两个面板和 metrics/logs/traces 关联。

Trace 在这个项目中有必要：一次自动视频处理会跨越 B 站动态采集、SQLite 队列、Go 调度、Unix Socket gRPC、Worker、模型供应商 HTTP 和多渠道通知，仅靠日志和指标无法稳定定位慢点与失败链路。自动任务持久化采集时的 W3C Trace Context，Go 调度恢复后经 gRPC 注入 Worker，总结继承转写执行上下文，终态通知继续同一 Trace；AI 工作台创建的任务则从用户提交任务的后端 HTTP span 开始。Tempo 因而可以在一条 waterfall 中查看完整因果链。本服务流量低，100% 采样的成本可控；浏览器、空闲 Outbox 轮询、探针、静态资源和 WebSocket 长连接生命周期不采集，只记录 WebSocket 握手。

在 Explore 中可使用：

```logql
{service_name="bili-notify"} | category="system"
{service_name="bili-notify"} | category="system" | severity_text=~"WARN|ERROR"
{service_name="bili-notify"} | trace_id="Trace ID"
{service_name="bili-notify-ai-worker"} | trace_id="Trace ID"
```

基础 `compose.yaml` 显式设置 `BILI_NOTIFY_OTEL_SDK_DISABLED=true`，不依赖观测栈。若手工部署 Collector，至少设置 `BILI_NOTIFY_OTEL_SDK_DISABLED=false`、`BILI_NOTIFY_OTEL_EXPORTER_OTLP_ENDPOINT` 和 `BILI_NOTIFY_OTEL_EXPORTER_OTLP_PROTOCOL=grpc|http/protobuf`；AI Worker 当前要求 `http/protobuf`。非法 OpenTelemetry 配置会使启动失败；运行时导出或关闭 flush 失败仅记日志，不使业务退出。

管理员密码可在“设置”中修改，修改后所有现有会话与 WebSocket 会立即失效。本版本不提供忘记密码恢复；密码丢失后只能使用新的数据卷重新初始化。

### v10 数据库升级

从 v0.4.5 升级时，Goose v10 会在单个 SQLite 事务中把 B 站账号、UP 来源、内容、评论、seen、投递队列和 AI 内容引用转换为带平台前缀的唯一模型，并立即删除旧事实表。这是不可逆的破坏性迁移；任一密文或旧载荷损坏都会回滚整个迁移并拒绝启动，不会留下半迁移数据库。

程序不会自动创建数据库备份。停止旧进程后，升级前必须一起备份 `data.db`、存在时的 `data.db-wal` 与 `data.db-shm`，以及 `master.key`；媒体内容还应同时备份 `media/`。不要只复制正在运行的 `data.db`，也不要把数据库与不匹配的主密钥分开恢复。迁移完成后只提供 `/api/v4`；`/api/v3` 不注册并返回 404。

删除来源时，数据库事务会同时删除来源拥有的 Outbox 并写入持久化媒体清理任务。后台 cleaner 使用数据库重试状态幂等删除文件，只清理 `media/` 根目录内已经为空的父目录；重启不会丢失未完成任务，路径穿越或符号链接目标会被标记为阻塞并保留证据。

## 本地开发

AI Worker 需要 Python 3.12+、FFmpeg 和 yt-dlp。`make worker-check` 会在 `worker/.venv` 安装锁定依赖并执行 Ruff 与 pytest；`make worker-docker-build` 构建独立 Worker 镜像。本地分别运行时，Go 服务与 Worker 必须配置同一个 Unix Socket：

```bash
make worker-install
BILI_NOTIFY_AI_WORKER_SOCKET=/tmp/bili-notify-ai.sock \
  PYTHONPATH=worker worker/.venv/bin/python -m bili_ai_worker.server
make run ARGS='serve --ai-worker-socket /tmp/bili-notify-ai.sock'
```

Worker 向标准输出写 JSON 结构化日志；启用 OTel 时同一日志也导出到 Collector，且 stdout 和 OTLP 记录都关联当前 trace/span。它还导出任务结果与耗时、供应商请求与耗时、音频字节和缓存体积指标，并为 gRPC server 与供应商 HTTP 调用创建 span。Compose 部署可用 `docker compose logs -f ai-worker` 查看；转写和总结任务会记录任务 ID、模型、音频切片字节数、HTTP 状态、耗时和供应商请求 ID，模型连通性检测会记录模型、状态和耗时，但不会记录 API Key、Cookie、音频、提示词或转写正文。默认日志级别为 `info`，可通过 `BILI_NOTIFY_AI_LOG_LEVEL=debug` 调整。

克隆仓库后先启用项目内置的 Git hook：

```bash
git config core.hooksPath .githooks
```

提交信息必须遵循 Conventional Commits，格式为 `<type>[(scope)][!]: <description>`。允许的类型为 `feat`、`fix`、`docs`、`style`、`refactor`、`perf`、`test`、`build`、`ci`、`chore` 和 `revert`；例如 `feat: add dynamic filtering` 或 `fix(notify): retry timed-out webhooks`。Git 自动生成的 merge 和 revert 提交不受此格式限制。

`web/dist` 是 Vite 生成的构建产物，不提交到 Git。Makefile 是统一构建入口：`make build` 从 lockfile 安装前端依赖、构建前端，并在仓库根目录生成 `bili-notify`；`make docker-build` 生成 `bili-notify:local` 镜像，`make worker-docker-build` 生成 `bili-notify-ai-worker:local` 镜像。可通过 `BINARY`、`DOCKER_IMAGE`、`AI_WORKER_IMAGE`、`VERSION`、`COMMIT` 和 `BUILD_DATE` 覆盖产物名称与构建元数据。

常用构建命令：

```bash
make help
make build
make docker-build
make check
# 在多核机器上并行执行彼此独立的检查；CI 会按 runner 能力选择并行数
make -j3 check
```

提交前执行完整检查：

```bash
make playwright-install # 首次运行端到端测试时安装 Chromium
make frontend-lint
make frontend-test
make frontend-coverage # 与 CI 相同，四项前端覆盖率均不得低于 80%
make frontend-audit    # high 及以上 npm 漏洞失败
make frontend-e2e
make test
make test-race      # race detector + randomized test order
make vet

# 较重的稳定性门禁：race detector + 随机顺序，并重复全部 Go 测试 10 次
# 可用 GO_STABILITY_COUNT 调整次数；通常交给每日或手动 CI 执行
make test-stability

# 与 CI 相同的核心 Go 覆盖率（bilibili、notify、service、state、web）
make coverage
make coverage-race # CI 使用一次测试同时完成 race 与覆盖率门禁
go tool cover -func=coverage.out

# 可选：验证最终 scratch 镜像的初始化、健康检查和同卷重启
make docker-smoke DOCKER_IMAGE=bili-notify:e2e
```

端到端测试使用本地 TLS 伪 B站和企业微信端点，不读取真实账号或通知凭据。采集投递、管理安全和响应式场景各自启动独立数据目录、随机端口和 Go harness，并在 Chromium 的桌面浅色与 Pixel 7 触控深色项目中并行运行；测试执行 axe 可访问性扫描并校验已提交的移动历史视觉基线。失败时 Playwright 会在 `web/ui/test-results/` 保存截图、视频、trace 和对应 harness 日志。

CI 只在目标为 main 的 PR 和手动调用时运行；main 的 strict required check 确保合并候选已基于最新 main 通过门禁，合并后不再对等价代码重复执行常规 CI。CI 通过 Make 依赖图并行执行互不写入同一产物的检查，并按 runner 类型限制 Make 与测试工具的嵌套 worker 数；现有 self-hosted 优先和 GitHub-hosted fallback 选择策略保持不变。`CI Gate` 聚合格式、module tidy、workflow、npm audit、前后端测试、race、覆盖率、漏洞、观测配置以及主服务和 AI Worker 镜像冒烟结果，并作为 main 的唯一 required check。Release 不使用 GitHub cache，从源码和锁定依赖完整构建两个镜像，为它们发布相同的 SemVer 与 `latest` 标签，并分别生成 SBOM、签名和来源证明。Vitest 对单元、状态和组件测试执行并行文件级覆盖，statements、branches、functions、lines 四项全局覆盖率均不得低于 80%，设置页、控制台和操作日志页还有额外文件级阈值。Go race detector、随机顺序与上述五个核心包的跨包原子覆盖率在一次测试中完成，低于 80% 时失败；独立的 Stability workflow 每日以及手动触发时为每轮启动新测试进程，默认以 10 个随机顺序运行全部 Go race 测试，用来发现数据竞争和顺序依赖。覆盖率 artifact 由不执行仓库代码的独立 job 通过 GitHub OIDC 上传 Codecov，不使用仓库 Token。PR CI 的 BuildKit GHA cache 使用 `mode=min`，只导出最终结果需要的最小缓存集合，缓存上传失败不影响正确性。REST 与 WebSocket 契约样例位于 `web/testdata/contracts/`：Go 测试用真实处理器和生产 WebSocket 序列化类型校验样例，Vitest 读取同一批文件并通过集中定义的 Zod schema 解析。任何 API 字段变更必须在同一提交中更新生产代码、共享样例及两端契约测试。

正式镜像使用 Node 24 和与 `go.mod` 同步的 Go 工具链进行多阶段构建，仅将前端产物、静态 Go 二进制与系统 CA 放入 nonroot scratch 镜像。Renovate 联动升级 Go 指令与生产构建镜像，并维护固定 SHA 的 GitHub Actions、npm lockfile 及观测组件；开发依赖、Actions 和观测栈分别分组，任何更新都不自动合并。
