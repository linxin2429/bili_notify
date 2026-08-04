# Bili Notify

Bili Notify 是一个单实例 Go 服务，通过登录后的 B 站网页接口轮询 UP 主动态，并可靠投递到 SMTP 邮件、Microsoft Outlook/Microsoft 365、钉钉、飞书和企业微信群机器人。运行配置通过内置 TLS 管理控制台维护，动态和待投递通知持久化到 bbolt。

> B 站未提供面向任意公开 UP 主动态的稳定推送接口。本项目使用非官方网页接口，可能因接口变化、风控或平台规则而不可用；它不会绕过验证码、限流或风控。请仅在你有权使用的场景中部署。

## 快速启动

需要 Docker 29+、Docker Compose 和支持 POSIX ACL 的 Linux 文件系统（`setfacl` 命令）。

```bash
mkdir -p secrets
openssl rand -base64 32 > secrets/master-key

# 生成管理员密码哈希，按提示输入两次密码。
docker compose build
docker run --rm -it bili-notify:local admin hash-password
# 将命令输出的 $argon2id$... 整行保存到 secrets/admin-password-hash

# 示例自签证书仅适合本地测试；正式环境应挂载可信证书。
openssl req -x509 -newkey rsa:3072 -sha256 -nodes -days 30 \
  -keyout secrets/tls.key -out secrets/tls.crt \
  -subj '/CN=localhost' -addext 'subjectAltName=DNS:localhost,IP:127.0.0.1'

# 容器固定以 nonroot UID 65532 运行。Linux bind mount 会保留宿主机权限，
# 因此用 ACL 仅授予该 UID 读取权，不要把私钥改成全局可读。
chmod 0700 secrets
chmod 0600 secrets/*
setfacl -m u:65532:rx secrets
setfacl -m u:65532:r secrets/*

docker compose up -d --build
```

浏览器访问 `https://localhost:8443`，完成以下设置：

1. 使用管理员密码登录。
2. 在“B站登录”中生成二维码并使用哔哩哔哩 App 扫码。
3. 添加至少一个通知渠道并发送测试通知。
4. 添加需要监控的 UID。首次拉取只建立基线，不通知历史动态。

观测接口默认监听容器内 `:9090`，包括 `/healthz`、`/readyz` 和 `/metrics`，Compose 默认不发布到宿主机。

## 渠道设置

控制台中的 `settings` 使用 JSON：

```json
{"host":"smtp.example.com","port":"465","tls":"tls","username":"bot@example.com","password":"...","from":"bot@example.com","to":"a@example.com,b@example.com"}
```

邮件的 `tls` 只能为 `tls`（隐式 TLS）或 `starttls`，不支持明文 SMTP。

### Microsoft Outlook / Microsoft 365

Microsoft 渠道通过 Microsoft Graph 和 OAuth 2.0 设备码授权发送邮件，不使用已经停用的 SMTP Basic Auth。先在 Microsoft Entra 管理中心完成以下配置：

1. 创建“应用注册”，个人 Outlook 账号需选择“任何组织目录中的账户和个人 Microsoft 账户”。
2. 在“身份验证”中将“允许公共客户端流”设为“是”；设备码流不需要客户端密码或重定向 URI。
3. 添加 Microsoft Graph 的委托权限 `Mail.Send`。组织租户是否需要管理员同意由租户策略决定。
4. 复制“应用程序(客户端) ID”。

在控制台创建 `microsoft` 渠道；新渠道会保持停用，避免授权完成前产生阻塞任务：

```json
{"client_id":"00000000-0000-0000-0000-000000000000","tenant":"common","to":"a@example.com,b@example.com"}
```

`tenant` 可用 `common`（个人和组织账户）、`consumers`（仅个人账户）、`organizations`（仅组织账户）、租户 ID 或租户域名。保存后依次点击“授权 Microsoft”，在微软页面输入设备码并同意 `Mail.Send`，再启用渠道并发送测试通知。发件人固定为完成授权的邮箱账户。

访问令牌、刷新令牌和其他渠道秘密一起使用 AES-256-GCM 加密保存；令牌过期时程序自动刷新并原子更新加密记录。重新授权会替换旧令牌。

```json
{"webhook":"https://oapi.dingtalk.com/robot/send?access_token=...","secret":"SEC..."}
```

```json
{"webhook":"https://open.feishu.cn/open-apis/bot/v2/hook/...","secret":"..."}
```

```json
{"webhook":"https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=..."}
```

钉钉和飞书首版要求机器人启用签名密钥。

## 运维

```bash
# 持续查看 JSON 结构化日志
docker compose logs -f bili-notify

# 查看版本和命令
docker compose run --rm bili-notify --help

# 健康检查
docker compose exec bili-notify /bili-notify healthcheck

# 主密钥轮换：先停止服务，再执行；成功后用新文件替换 secrets/master-key。
docker compose stop bili-notify
docker compose run --rm -v ./secrets/new-master-key:/run/secrets/new-master-key:ro \
  bili-notify rekey --new-key-file /run/secrets/new-master-key
```

默认 `info` 日志包含启动、认证变化、UP 基线、采集失败与恢复、新动态入队、渠道测试、投递结果和 Microsoft 授权状态。需要查看每轮采集及每个 UP 的成功明细时，将 Compose 中的 `BILI_NOTIFY_LOG_LEVEL` 改为 `debug`。日志不会输出 Cookie、OAuth 令牌、SMTP 密码、Webhook 或动态正文。

证书文件被原子替换后会自动校验并热加载。替换文件可能丢失 ACL，替换后需重新执行
`setfacl -m u:65532:r secrets/tls.crt secrets/tls.key`。错误主密钥、无效证书或无效管理员密码哈希会导致服务拒绝启动。

详细需求、接口、可靠性和安全设计见 [docs/requirements-and-design.md](docs/requirements-and-design.md)。

## 本地开发

```bash
go test ./...
go test -race ./...
go vet ./...
```

项目要求 Go 1.26；正式构建使用无 CGO、nonroot scratch 镜像，并只复制系统 CA 证书。
