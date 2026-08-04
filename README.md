# Bili Notify

Bili Notify 是一个单实例 Go 服务，通过登录后的 B 站网页接口轮询 UP 主动态，并可靠投递到 SMTP 邮件、Microsoft Outlook/Microsoft 365、钉钉、飞书和企业微信群机器人。React 管理台与 Go 后端通过同源 WebSocket 实时通信，状态和待投递通知持久化到 bbolt。

> B 站未提供面向任意公开 UP 主动态的稳定推送接口。本项目使用非官方网页接口，可能因接口变化、风控或平台规则而不可用；它不会绕过验证码、限流或风控。请仅在你有权使用的场景中部署。

## 快速启动

不需要预先生成主密钥、管理员密码哈希或 TLS 私钥：

```bash
docker compose up -d --build
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

镜像标签由 git tag `vMAJOR.MINOR.PATCH` 发布时写入：`MAJOR.MINOR.PATCH`、`MAJOR.MINOR`、`MAJOR` 与 `latest`。push 到 `main` 只跑 CI 校验，不推送镜像。

日志会输出一次性 `setup_code`。浏览器访问 `https://localhost:8443`，接受首次自签名证书警告，然后输入初始化码并设置至少 12 字节的管理员密码。初始化完成后代码立即失效。

服务首次启动会在 `/data` 自动创建：

- `state.db`：运行状态数据库（bbolt）；
- `content.db`：已采集动态与 UP 回复内容库（SQLite）；
- `master.key`：随机 AES-256 主密钥；
- `tls.pem`：十年有效的本地自签名 ECDSA 证书和私钥。

这些文件只保存在 Docker 数据卷中。主密钥与数据库同卷可以实现无人值守重启，但不能防护整个数据卷同时被窃取的情况。生产环境需要可信证书时，可在服务前部署终止 TLS 的反向代理；应用自身始终使用 HTTPS/WSS。

首次登录后：

1. 在“概览”生成二维码并使用哔哩哔哩 App 扫码。
2. 添加至少一个通知渠道并发送测试通知。
3. 添加需要监控的 UID。首次拉取只建立基线，不通知历史动态；基线内容仍会写入“历史”页。
4. 在“历史”中按 UP、时间与关键字浏览已采集内容。

观测接口默认监听容器内 `:9090`，包含 `/healthz`、`/readyz` 和 `/metrics`，Compose 默认不发布到宿主机。

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

管理员密码可在“设置”中修改，修改后所有现有会话与 WebSocket 会立即失效。本版本不提供忘记密码恢复；密码丢失后只能使用新的数据卷重新初始化。

本版本不兼容旧的外置主密钥数据库，也不会自动删除或迁移旧卷。升级前请备份；切换新版时必须显式创建全新数据卷。

## 本地开发

前端构建产物提交在 `web/dist`，因此干净克隆可以直接执行 Go 构建。修改前端后必须重新生成产物：

```bash
cd web/ui
npm ci
npm run lint
npm test
npm run build

cd ../..
go build ./...
go test ./...
go test -race ./...
go vet ./...
```

正式镜像使用 Node 24 和 Go 1.26 多阶段构建，仅将前端产物、静态 Go 二进制与系统 CA 放入 nonroot scratch 镜像。
