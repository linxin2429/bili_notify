import { z } from 'zod'

export const channelTypeSchema = z.enum(['email', 'microsoft', 'dingtalk', 'feishu', 'wecom'])

export const serviceStatusSchema = z.object({
  auth_valid: z.boolean(),
  bili_account: z.object({ uid: z.string(), name: z.string() }).optional(),
  last_success_at: z.string().optional(),
  up_count: z.number().int(),
  channel_count: z.number().int(),
  outbox_depth: z.number().int(),
  oldest_delivery: z.string().optional(),
  ready: z.boolean(),
  risk_paused_until: z.string().optional(),
})

export const upSchema = z.object({
  uid: z.string(),
  name: z.string(),
  enabled: z.boolean(),
  baseline_ready: z.boolean(),
  last_poll_at: z.string().optional(),
  last_success_at: z.string().optional(),
  last_error: z.string().optional(),
  consecutive_fail: z.number().int(),
  follow_state: z.enum(['unknown', 'followed', 'unfollowed']),
  follow_checked_at: z.string().optional(),
  collection_route: z.enum(['feed_all', 'space']),
})

export const channelSchema = z.object({
  id: z.string(),
  name: z.string(),
  type: channelTypeSchema,
  enabled: z.boolean(),
  settings: z.record(z.string(), z.string()),
  configured_secrets: z.array(z.string()),
  created_at: z.string(),
  updated_at: z.string(),
})

const dynamicDeliveryPreviewSchema = z.object({
  id: z.string(), uid: z.string(), up_name: z.string(), type: z.string(), published_at: z.string(),
  summary: z.string(), url: z.string(),
})

const commentDeliveryPreviewSchema = z.object({
  rpid: z.string(), up_uid: z.string(), up_name: z.string(), content_type: z.string(), content_id: z.string(),
  content_title: z.string().optional(), content_url: z.string(), published_at: z.string(),
})

export const deliverySchema = z.object({
  id: z.string(),
  kind: z.enum(['dynamic', 'comment']).optional(),
  dynamic: dynamicDeliveryPreviewSchema.optional(),
  comment: commentDeliveryPreviewSchema.optional(),
  channel_id: z.string(),
  state: z.enum(['pending', 'blocked']),
  attempts: z.number().int(),
  next_at: z.string(),
  last_error: z.string().optional(),
  created_at: z.string(),
})

export const biliLoginSchema = z.object({
  id: z.string(), status: z.string(), expires_at: z.string(), qr_data_url: z.string().optional(),
}).nullable()

export const microsoftLoginSchema = z.object({
  channel_id: z.string(), status: z.string(), user_code: z.string().optional(),
  verification_uri: z.string().optional(), verification_uri_complete: z.string().optional(),
  expires_at: z.string().optional(), error: z.string().optional(),
})

export const runtimeSettingsSchema = z.object({
	poll_interval_sec: z.number().int().min(10).max(86400),
	request_rate: z.number().positive().max(10),
	request_concurrency: z.number().int().min(1).max(16),
	comment_enabled: z.boolean(),
	comment_track_n: z.number().int().min(1).max(50),
	comment_root_pages: z.number().int().min(1).max(10),
	comment_reply_pages: z.number().int().min(1).max(20),
	comment_batch_interval_sec: z.number().int().min(30).max(86400),
	log_level: z.enum(['debug', 'info', 'warn', 'error']),
	audit_log_retention_days: z.number().int().min(1).max(3650),
	system_log_retention_days: z.number().int().min(1).max(3650),
	relation_refresh_interval_sec: z.number().int().min(60).max(86400),
	space_reconcile_interval_sec: z.number().int().min(300).max(604800),
	max_dynamic_pages: z.number().int().min(1).max(20),
	risk_pause_sec: z.number().int().min(60).max(3600),
	delivery_concurrency: z.number().int().min(1).max(32),
	backlog_alert_count: z.number().int().min(1).max(100000),
	backlog_alert_age_sec: z.number().int().min(60).max(86400),
	delivery_retry_delays_sec: z.tuple([
		z.number().int().min(1).max(86400), z.number().int().min(1).max(86400), z.number().int().min(1).max(86400),
		z.number().int().min(1).max(86400), z.number().int().min(1).max(86400),
	]).refine(values => values.every((value, index) => index === 0 || value >= values[index - 1]), '重试阶段必须单调不减'),
}).strict()

export const dashboardSnapshotSchema = z.object({
  status: serviceStatusSchema,
  settings: runtimeSettingsSchema,
  ups: z.array(upSchema),
  channels: z.array(channelSchema),
  deliveries: z.array(deliverySchema),
  bili_login: biliLoginSchema.optional(),
  microsoft_logins: z.array(microsoftLoginSchema),
  timezone: z.string(),
  updated_at: z.string(),
})

export const dynamicMediaSchema = z.object({
  kind: z.string(), url: z.string(), width: z.number().int().optional(), height: z.number().int().optional(),
})

export const dynamicStatsSchema = z.object({
  forwards: z.number().int(), comments: z.number().int(), likes: z.number().int(),
})

export const dynamicVideoSchema = z.object({
  duration: z.string().optional(), views: z.string().optional(), danmaku: z.string().optional(),
})

export const dynamicPreviewSchema = z.object({
  id: z.string().optional(), uid: z.string().optional(), up_name: z.string().optional(), type: z.string().optional(),
  title: z.string().optional(), summary: z.string().optional(), description: z.string().optional(),
  url: z.string().optional(), target_url: z.string().optional(), badge: z.string().optional(),
  media: z.array(dynamicMediaSchema).optional(), video: dynamicVideoSchema.optional(),
})

export const dynamicHistorySchema = z.object({
  id: z.string(), uid: z.string(), up_name: z.string(), type: z.string(), published_at: z.string(), discovered_at: z.string(),
  baseline: z.boolean(), title: z.string().optional(), summary: z.string().optional(), description: z.string().optional(),
  url: z.string().optional(), target_url: z.string().optional(), badge: z.string().optional(),
  media: z.array(dynamicMediaSchema).optional(), stats: dynamicStatsSchema.optional(), video: dynamicVideoSchema.optional(),
  original: dynamicPreviewSchema.optional(),
})

export const commentHistorySchema = z.object({
  rpid: z.string(), up_uid: z.string(), up_name: z.string(), content_type: z.string().optional(),
  content_id: z.string().optional(), content_title: z.string().optional(), content_url: z.string().optional(),
  published_at: z.string(), discovered_at: z.string(), baseline: z.boolean(), incomplete: z.boolean().optional(),
})

const commentThreadEntrySchema = z.object({
  rpid: z.string(), parent: z.string().optional(), mid: z.string(), name: z.string(), message: z.string(), time: z.string(),
  is_up: z.boolean().optional(), is_trigger: z.boolean().optional(),
})

export const commentDetailSchema = z.object({
  rpid: z.string(), up_uid: z.string(), up_name: z.string(), content_type: z.string(), content_id: z.string(),
  content_title: z.string().optional(), content_url: z.string(), published_at: z.string(), incomplete: z.boolean().optional(),
  thread: z.array(commentThreadEntrySchema),
})

export const auditLogSchema = z.object({
  id: z.number().int(), occurred_at: z.string(), request_id: z.string(), actor: z.enum(['administrator', 'anonymous']),
  session_id: z.string(), remote_ip: z.string(), user_agent: z.string(), action: z.string(), resource_type: z.string(),
  resource_id: z.string(), outcome: z.enum(['success', 'failure', 'denied']), http_method: z.string(), route: z.string(),
  status_code: z.number().int(), error_code: z.string().optional(), duration_ms: z.number().int(),
  details: z.record(z.string(), z.unknown()),
})

export const contentPageSchema = <T extends z.ZodType>(itemSchema: T) => z.object({
  items: z.array(itemSchema), total: z.number().int(), limit: z.number().int(), offset: z.number().int(),
})

export const dynamicHistoryPageSchema = contentPageSchema(dynamicHistorySchema)
export const commentHistoryPageSchema = contentPageSchema(commentHistorySchema)
export const auditLogPageSchema = contentPageSchema(auditLogSchema)

export const sessionStateSchema = z.object({
  setup_required: z.boolean(), authenticated: z.boolean(), csrf_token: z.string().optional(),
})
export const csrfStateSchema = z.object({ csrf_token: z.string() })
export const sentStatusSchema = z.object({ status: z.literal('sent') })
export const queuedStatusSchema = z.object({ status: z.literal('queued') })
export const emptyResponseSchema = z.undefined()

export const websocketEnvelopeSchema = z.object({ event: z.string(), revision: z.number().int(), data: z.unknown() })

export function parseWebsocketEvent(event: string, data: unknown): unknown {
  switch (event) {
    case 'snapshot': return dashboardSnapshotSchema.parse(data)
    case 'status.updated': return serviceStatusSchema.parse(data)
    case 'settings.updated': return runtimeSettingsSchema.parse(data)
    case 'ups.updated': return z.array(upSchema).parse(data)
    case 'channels.updated': return z.array(channelSchema).parse(data)
    case 'deliveries.updated': return z.array(deliverySchema).parse(data)
    case 'bilibili.login.updated': return biliLoginSchema.parse(data)
    case 'microsoft.login.updated': return z.array(microsoftLoginSchema).parse(data)
    default: throw new Error(`未知服务器事件：${event}`)
  }
}
