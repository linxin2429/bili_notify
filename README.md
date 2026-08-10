# Bili Notify

除动态采集与通知外，AI 工作台可把 B 站视频音频转成带时间位置的文字，并异步总结直接输入或转写得到的文本。耗时、依赖多且处理不可信媒体的工作由独立 Python Worker 执行；Go 主进程只负责任务持久化、鉴权、调度和管理 API，因此 Worker 暂停时普通采集与通知仍然可用，待处理 AI 任务保留在 SQLite 队列中。

Bili Notify 是一个单实例 Go 服务，通过登录后的 B 站网页接口采集 UP 主动态，并可靠投递到 SMTP 邮件、Microsoft Outlook/Microsoft 365、钉钉、飞书和企业微信群机器人。已关注的监控 UP 使用账号综合动态流及时发现，并定期通过空间动态校验；未关注或关系未知的 UP 直接轮询空间动态。React 管理台与 Go 后端通过同源 WebSocket 实时通信，状态、待投递通知与内容档案持久化到单一 SQLite 数据库。

> B 站未提供面向任意公开 UP 主动态的稳定推送接口。本项目使用非官方网页接口，可能因接口变化、风控或平台规则而不可用；它不会绕过验证码、限流或风控。请仅在你有权使用的场景中部署。

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
3. 添加需要监控的 UID。首次拉取只建立基线，不通知历史动态；基线内容仍会写入“历史”页。
4. 在“历史”中按 UP、时间与关键字浏览已采集内容。
5. 如需 AI 功能，在“AI 设置”创建转写/总结模型配置档和总结提示词，然后在“AI 工作台”提交 BVID 或文本。配置档支持任意 OpenAI 兼容的 HTTPS Base URL、API Key 和模型名；API Key 加密保存且不会回传浏览器。默认示例可使用 OpenRouter 的 `https://openrouter.ai/api/v1` 与 `openai/gpt-transcribe`。

“设置”页可热更新基础与高级采集策略、投递并发与重试、积压告警、日志级别和审计日志保留期。保存的是一份完整运行设置：后续任务立即读取新策略，正在执行的任务和已经排定的重试不会被取消或改写。`BILI_NOTIFY_POLL_INTERVAL`、`BILI_NOTIFY_REQUEST_RATE`、`BILI_NOTIFY_REQUEST_CONCURRENCY`、`BILI_NOTIFY_LOG_LEVEL` 和 `BILI_NOTIFY_AUDIT_LOG_RETENTION` 只在新数据目录首次启动时播种默认值，之后以 `data.db` 中的管理台设置为准。

观测接口默认监听容器内 `:9090`，只包含 `/healthz` 和 `/readyz`，Compose 默认不发布到宿主机。Metrics 通过 OTLP 发送到 OpenTelemetry Collector，由 Collector 在 `:9464/metrics` 上转换为 Prometheus/OpenMetrics。

## 通知渠道

管理台按渠道类型提供结构化表单。密码、Webhook、签名密钥及 OAuth 令牌只写入后端；浏览器只会收到“已配置”标记，不会读回秘密值。

### SMTP 邮件

填写主机、端口、TLS 模式、发件人和收件人；用户名和密码按 SMTP 服务要求填写。TLS 模式只允许 `tls`（465 隐式 TLS）或 `starttls`，不支持明文 SMTP。

### Microsoft Outlook / Microsoft 365

Microsoft 渠道通过 Microsoft Graph 与 OAuth 2.0 设备码授权发送邮件：

1. 在 Microsoft Entra 创建应用注册；个人 Outlook 账号需允许个人 Microsoft 账户。
2. 启用“允许公共客户端流”，添加委托权限 `Mail.Send`。
3. 在控制台填写客户端 ID、租户和收件人，保存后点击“开始授权”。

`tenant` 可填写 `common`、`consumers`、`organizations`、租户 UUID 或租户域名。访问令牌和刷新令牌会加密保存并自动刷新。

### 群机器人

钉钉和飞书填写 HTTPS Webhook 与签名密钥；企业微信填写 HTTPS Webhook。所有 Webhook 与签名密钥都加密保存。

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

应用使用 Cobra/Viper 管理的 `BILI_NOTIFY_OTEL_*` 配置把 logs、metrics 和 100% 采样的 traces 发送到 OpenTelemetry Collector。Collector 把日志写入 Loki、在 `:9464` 导出 Prometheus metrics、把 trace 写入 Tempo；Prometheus 和 Loki 保留 30 天，Tempo 保留 7 天。Collector 出口使用持久化队列与无限时重试，后端短暂故障不阻塞业务。Grafana 预置了 Prometheus、Loki、Tempo 数据源、两个面板和 metrics/logs/traces 关联。

Trace 在这个项目中有必要：一次采集或投递会跨越 B 站 HTTP、SQLite 事务、媒体下载和通知渠道，仅靠日志和指标无法稳定定位慢点与失败链路。采集创建 Outbox 任务时会持久化 W3C Trace Context，异步投递恢复同一 Trace，因此可以在 Tempo 的一条 waterfall 中查看从内容发现、入队到通知渠道发送的完整因果链。本服务流量低，100% 采样的成本可控；空闲 Outbox 轮询、探针、静态资源和 WebSocket 长连接生命周期不采集，只记录 WebSocket 握手。

在 Explore 中可使用：

```logql
{service_name="bili-notify"} | category="system"
{service_name="bili-notify"} | category="system" | severity_text=~"WARN|ERROR"
{service_name="bili-notify"} | trace_id="Trace ID"
```

基础 `compose.yaml` 显式设置 `BILI_NOTIFY_OTEL_SDK_DISABLED=true`，不依赖观测栈。若手工部署 Collector，至少设置 `BILI_NOTIFY_OTEL_SDK_DISABLED=false`、`BILI_NOTIFY_OTEL_EXPORTER_OTLP_ENDPOINT` 和 `BILI_NOTIFY_OTEL_EXPORTER_OTLP_PROTOCOL=grpc|http/protobuf`。非法 OpenTelemetry 配置会使启动失败；运行时导出或关闭 flush 失败仅记日志，不使业务退出。

管理员密码可在“设置”中修改，修改后所有现有会话与 WebSocket 会立即失效。本版本不提供忘记密码恢复；密码丢失后只能使用新的数据卷重新初始化。

本版本不兼容旧的外置主密钥数据库，也不会自动删除或迁移旧卷。升级前请备份；切换新版时必须显式创建全新数据卷。

## 本地开发

AI Worker 需要 Python 3.12+、FFmpeg 和 yt-dlp。`make worker-check` 会在 `worker/.venv` 安装锁定依赖并执行 Ruff 与 pytest；`make worker-docker-build` 构建独立 Worker 镜像。本地分别运行时，Go 服务与 Worker 必须配置同一个 Unix Socket：

```bash
make worker-install
BILI_NOTIFY_AI_WORKER_SOCKET=/tmp/bili-notify-ai.sock \
  PYTHONPATH=worker worker/.venv/bin/python -m bili_ai_worker.server
make run ARGS='serve --ai-worker-socket /tmp/bili-notify-ai.sock'
```

Worker 向标准输出写 JSON 结构化日志。Compose 部署可用 `docker compose logs -f ai-worker` 查看；转写和总结任务会记录任务 ID、模型、音频切片字节数、HTTP 状态、耗时和供应商请求 ID，模型连通性检测会记录模型、状态和耗时，但不会记录 API Key、Cookie、音频、提示词或转写正文。默认日志级别为 `info`，可通过 `BILI_NOTIFY_AI_LOG_LEVEL=debug` 调整。

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
