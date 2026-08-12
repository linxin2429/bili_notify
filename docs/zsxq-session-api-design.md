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

同一 external account 更新 token 不改变来源启停状态。切换到不同账号时，账号替换与全部 ZSXQ 来源停用必须在同一 SQLite 事务完成；任一步失败整体回滚。导入成功不自动同步来源。`POST /api/v4/accounts/zsxq/sync-sources` 每次从数据库读取当前 token，请求 `/v2/groups` 后合并可见来源。

## 4. 管理 API、审计与前端

导入成功返回 `201 PlatformAccount`，且账号 DTO 永不包含 session。Cookie 解析错误返回 `400 validation_failed`；上游确认 token 无效或过期返回 `422 invalid_token`，两者通过可选 `error.fields.cookie` 定位输入；签名、网络或未知上游失败返回 `502 upstream_failed`。所有写操作保留管理员会话和 CSRF 校验。

删除短信端点 `POST /accounts/zsxq/sms-code` 与验证码端点 `POST /accounts/zsxq/session`；保留 `DELETE /accounts/zsxq/session` 和 `POST /accounts/zsxq/sync-sources`。导入写 `zsxq.token.import` 审计事件，摘要、日志和错误不得包含 Cookie、token、请求体或上游正文。

登录页只有一个秘密输入框，并说明：登录知识星球网页，在开发者工具中选择 `api.zsxq.com` 请求，复制 `Cookie` 请求头值并粘贴。值只存在组件表单 state，不进入 URL、Query cache、localStorage 或错误详情。成功后清空输入、失效账号查询并进入来源页；来源同步仍由用户点击“刷新可见星球”。删除 Aliyun captcha 模块、全局类型与 CSP 外联白名单。

## 5. 采集与附件安全

两个采集协程每轮从存储读取 session 中的 token，并显式传给所有 API 请求，因此不会共享或覆盖认证状态。token 无效时账号标记 invalid 并真正清除密文。附件只在首个目标仍为 `api.zsxq.com` 时携带 `Authorization`；重定向及 CDN 请求必须删除 `Authorization`、Cookie 等认证头并重新执行 SSRF 校验。

## 6. 验证

使用固定时钟、设备 ID 与 request ID 验证签名原文、HMAC、headers、path/query 和五个上游合同；覆盖 envelope 漂移、认证、签名、权限、限流、404、HTML、畸形和超大响应。Cookie 解析使用表驱动测试与原生 fuzz。管理 API 覆盖认证、CSRF、JSON/字段错误、400/422/502/201、旧账号保留、同账号更新、异账号原子停用与回滚。持久化测试证明只密封 token、旧 session 判 invalid 和认证失效清密文；媒体测试证明首跳带 Authorization、重定向不转发。前端覆盖帮助文案、秘密输入、字段错误、载荷、成功跳转与查询失效，并证明 captcha 不再加载。
