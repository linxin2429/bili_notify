# 知识星球 Session 导入与 API 客户端设计

## 1. 目标、来源与限制

知识星球没有供本项目使用的稳定公开 API。本集成只实现归档所需的最小协议，参考 `zsxq-sdk` 固定提交 `a236838`，不引入该 SDK，也不实现短信、验证码、滑块或密码登录。管理员从自己已登录的知识星球网页请求中复制完整 `Cookie` 请求头值；服务只提取 `zsxq_access_token`，验证当前用户后加密保存。

参考实现中的签名密钥和算法截至本设计尚未用真实账号联调，因此本地合同测试通过不等于已经证明线上生产可用。协议变化必须先修改本文，再修改代码。

## 2. 上游合同与认证

Base URL 固定为 `https://api.zsxq.com`，应用版本为 `2.83.0`。客户端仅实现：

- `GET /v3/users/self`
- `GET /v2/groups`
- `GET /v2/groups/{id}/topics`
- `GET /v2/topics/{id}`
- `GET /v2/topics/{id}/comments`

每次调用显式传入 token；客户端不持有 cookie jar 或可变登录状态。请求携带 `authorization: <token>`、`x-timestamp`、`x-request-id`、`x-version: 2.83.0` 和稳定的 `x-aduid`。签名为：

```text
HMAC-SHA1("zsxq-sdk-secret", timestamp + "\n" + upper(method) + "\n" + path [+ "\n" + exactBody])
```

结果以小写十六进制写入 `x-signature`。query 不参与签名，body 必须使用实际发送的精确字节。客户端不增加内部重试；超时、并发、速率和调度恢复继续由现有 HTTP client、RequestGate 与采集器负责。

响应限制大小并严格解析 `{succeeded,code,error,info,resp_data}`，错误不携带响应正文。HTTP 401 或业务码 `10001/10002` 是 token 失效；`10003` 是签名或协议失败；HTTP 429 或 `40001` 是限流；来源权限码是来源级权限错误；HTTP 404 或资源码是远端不存在；其余失败是上游错误。topics 保留 `end_time` 游标，comments 保留 `begin_time` 游标并完整翻页。

## 3. Cookie 导入、存储与账号切换

`POST /api/v4/accounts/zsxq/token` 接收 `{"cookie":"<Cookie header value>"}`。服务以 `http.ParseCookie` 解析，拒绝控制字符、缺失/空值/重复 `zsxq_access_token` 及超过管理 API 1 MiB 请求限制的输入，不约束 token 为 UUID。候选 token 先请求 `/v3/users/self`；只有验证成功才写数据库，失败保留旧账号。

数据库 schema 不变，`PlatformAccount.Session` 仅保存 `{"zsxq_access_token":"..."}`，继续由 Vault 密封。旧密文已有该键时直接使用；没有该键的账号视为 invalid，要求重新导入。认证失效时显式以空 session 更新，不能利用 `nil` 的“保留旧秘密”语义。

账号凭证与采集源是两个独立事实：账号保存“当前用什么 token 访问”，来源保存“管理员希望采集哪些星球”。同一账号更新 token 或切换到不同账号都只替换账号记录，不修改、停用或删除任何 ZSXQ 来源。管理端通过 `GET /api/v4/accounts/zsxq/groups` 实时读取当前账号的可见星球；创建来源时只提交选中的 `group_id`，服务端再次读取列表并使用上游名称和星主信息，拒绝当前账号不可见的 ID。目录不持久化，已有来源不会因切换账号而自动删除。

## 4. 管理 API、审计与前端

导入成功返回 `201 PlatformAccount`，且账号 DTO 永不包含 session。Cookie 解析错误返回 `400 validation_failed`；上游确认 token 无效或过期返回 `422 invalid_token`，两者通过可选 `error.fields.cookie` 定位输入；签名、网络或未知上游失败返回 `502 upstream_failed`。所有写操作保留管理员会话和 CSRF 校验。

删除短信端点 `POST /accounts/zsxq/sms-code`、验证码端点 `POST /accounts/zsxq/session` 与批量来源同步端点；保留 `DELETE /accounts/zsxq/session`。导入写 `zsxq.token.import` 审计事件，摘要、日志和错误不得包含 Cookie、token、请求体或上游正文。

登录页只有一个秘密输入框，并说明：登录知识星球网页，在开发者工具中选择 `api.zsxq.com` 请求，复制 `Cookie` 请求头值并粘贴。值只存在组件表单 state，不进入 URL、Query cache、localStorage 或错误详情。成功后清空输入、失效账号查询并进入来源页；更换 Session 不修改来源。来源页把 B 站和知识星球添加入口分开：B 站填写 UID，知识星球从当前账号目录下拉选择，只填写可选备注和启停状态。已添加星球仍显示但不可再次选择。

## 5. 采集与附件安全

两个采集协程每轮从存储读取 session 中的最新 token，并显式传给所有已启用来源的 API 请求，因此账号或 token 更新后无需重建来源。停用来源不采集；新 token 对某个来源无权限时只记录来源错误，不改变来源启停状态。手动来源没有预先同步的星主 ID，评论解析仅在星主 ID 缺失时采用上游 `owner` 角色；已有星主 ID 时仍以 ID 为准，不能由其他作者声明角色。token 本身无效时账号标记 invalid 并真正清除密文。附件只在首个目标仍为 `api.zsxq.com` 时携带 `Authorization`；重定向及 CDN 请求必须删除 `Authorization`、Cookie 等认证头并重新执行 SSRF 校验。

## 6. 验证

使用固定时钟、设备 ID 与 request ID 验证签名原文、HMAC、headers、path/query 和采集所需上游合同；覆盖星球目录的大整数 ID、缺失字段、envelope 漂移、认证、签名、权限、限流、404、HTML、畸形和超大响应。Cookie 解析使用表驱动测试与原生 fuzz。管理 API 覆盖认证、CSRF、JSON/字段错误、账号目录、不可见星球、重复来源和服务端派生名称/星主信息。持久化测试证明只密封 token、旧 session 判 invalid 和认证失效清密文；媒体测试证明首跳带 Authorization、重定向不转发。前端覆盖独立的平台入口、星球下拉、已添加项禁用、加载失败重试、成功跳转与查询失效。
