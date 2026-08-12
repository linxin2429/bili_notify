# 知识星球官方 MCP 密钥与采集客户端设计

## 能力边界

知识星球集成只使用官方长期运行边界：管理员在 [Jasmine 密钥管理](https://garden.zsxq.com/jasmine/) 创建可撤销密钥，Bili Notify 加密保存后调用 `https://mcp.zsxq.com/topic/mcp/`。服务不读取 Cookie，不保存 `zsxq_access_token`，不模拟网页或移动客户端，也不实现私有签名。交互式 `zsxq-cli` 的设备授权和系统 Keychain 适合终端用户，不适合无交互、可迁移的 Docker 服务，因此镜像不捆绑 CLI、Node 或 Keychain。

账号凭据与采集来源是两个独立事实。凭据回答“当前以谁的权限读取”，来源回答“管理员希望长期归档哪些星球”。更新或删除密钥不修改来源、内容、评论、附件、历史回补游标或高水位。升级迁移只删除旧 `zsxq` 账号行，以确保旧 Cookie 密文不可继续使用。

## MCP 协议

客户端每次向固定 MCP 原点发送 JSON-RPC 2.0 `tools/call`，工具名为 `call_zsxq_api`，参数为原有只读合同的 `method`、`path`、`query` 和可选 `body`。密钥只放在 `X-Api-Key`；请求不包含 Cookie、Authorization、Origin、Referer、设备标识、本地 HMAC 或客户端版本签名。

服务接受官方 `text/event-stream` 和 JSON 响应，总量最多 8 MiB。SSE 必须存在且只存在一个与请求 JSON-RPC ID 相同的 `message` 事件。结果必须只有一个 `text` 内容块，并严格解包 `{success,status_code,body}`；成功的 `body` 再按现有 `{succeeded,code,resp_data}` 合同解析。ID 错误、重复/缺失事件、畸形或尾随 JSON、缺失字段及结构漂移都归类为上游协议失败，原始正文不进入错误、日志或管理响应。

HTTP 或代理状态统一分类：401 表示密钥失效，403 表示权限不足，404 表示远端删除，429 表示限流，其余非 2xx 和 5xx 表示上游失败。业务封套继续识别等价认证、权限、删除和限流码；不再包含 Cookie 客户端专属的签名拒绝或 `1059` 分支。

## 管理 API 与生命周期

`PUT /api/v4/accounts/zsxq/credential` 接收 `{"api_key":"<opaque secret>"}`。密钥去除首尾空白后必须非空、不含 Unicode 控制字符且不超过 8 KiB。候选密钥先调用 `/v3/users/self` 验证，成功后才原子替换 `PlatformAccount.Session["zsxq_api_key"]`；失败保留旧密钥。成功返回不含秘密的 `PlatformAccount`。无效密钥返回 `422 invalid_api_key`，字段定位到 `api_key`；权限、限流和上游失败分别为 403、429 和 502。

`DELETE /api/v4/accounts/zsxq/credential` 删除单例账号凭据并返回 204。两项审计动作分别为 `zsxq.credential.update` 和 `zsxq.credential.delete`。账号列表、WebSocket、Query cache、审计详情、日志和错误都不得包含密钥。

每轮主题和评论采集都重新从 SQLite 读取最新密钥，因此更新后无需重建 worker 或来源。401 会清空密文、把账号标记为 `invalid`，并生成要求管理员在 Jasmine 重新创建或更新密钥的系统提醒。某个星球的 403 只更新该来源错误，不停用或删除来源，也不回退到 Cookie。

## 附件边界

文件元数据仍通过 `/v2/files/{file_id}/download_url` 经 MCP 获取临时签名 URL。附件下载器只接收该 URL，不接收账号密钥；首跳和所有重定向都以无认证请求下载，并主动删除 `X-Api-Key`、Authorization 和 Cookie，同时对每一跳重新执行公网地址校验。签名 URL 只存在于内存中的归档过程，不写入日志或通知。

## 管理台

“知识星球密钥连接”页只在组件内存保存密码型输入值，提供 Jasmine 创建、复制和粘贴说明。成功后清空输入、刷新账号状态并进入来源页；失败保留输入便于纠正。来源页只显示连接、更新或删除密钥，不再出现 Session、Cookie、开发者工具或 OAuth 扫码指引。
