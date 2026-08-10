import { z } from 'zod'
import type { components } from './generated/schema'
import { realtimeTopics } from './realtime-contract'

type Schemas = components['schemas']

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
}).strict() satisfies z.ZodType<Schemas['Status']>

export const runtimeSchema = z.object({
  status: serviceStatusSchema,
  timezone: z.string(),
  updated_at: z.string(),
}).strict() satisfies z.ZodType<Schemas['Runtime']>

export const upSchema = z.object({
  uid: z.string(), name: z.string(), enabled: z.boolean(), baseline_ready: z.boolean(),
  last_poll_at: z.string().optional(), last_success_at: z.string().optional(), last_error: z.string().optional(),
  consecutive_fail: z.number().int(), follow_state: z.enum(['unknown', 'followed', 'unfollowed']),
  follow_checked_at: z.string().optional(), collection_route: z.enum(['feed_all', 'space']),
}).strict() satisfies z.ZodType<Schemas['UP']>

export const channelSchema = z.object({
  id: z.string(), name: z.string(), type: channelTypeSchema, enabled: z.boolean(),
  settings: z.record(z.string(), z.string()), configured_secrets: z.array(z.string()),
  created_at: z.string(), updated_at: z.string(),
}).strict() satisfies z.ZodType<Schemas['Channel']>

const dynamicDeliveryPreviewSchema = z.object({
  id: z.string(), uid: z.string(), up_name: z.string(), type: z.string(), published_at: z.string(), summary: z.string(), url: z.string(),
}).strict() satisfies z.ZodType<Schemas['DynamicPreview']>
const commentDeliveryPreviewSchema = z.object({
  rpid: z.string(), up_uid: z.string(), up_name: z.string(), content_type: z.string(), content_id: z.string(),
  content_title: z.string().optional(), content_url: z.string(), published_at: z.string(),
}).strict() satisfies z.ZodType<Schemas['CommentPreview']>
export const deliverySchema = z.object({
  id: z.string(), kind: z.enum(['dynamic', 'comment']), dynamic: dynamicDeliveryPreviewSchema.optional(),
  comment: commentDeliveryPreviewSchema.optional(), channel_id: z.string(), state: z.enum(['pending', 'blocked']),
  attempts: z.number().int(), next_at: z.string(), last_error: z.string().optional(), created_at: z.string(),
}).strict() satisfies z.ZodType<Schemas['Delivery']>

const biliLoginObjectSchema = z.object({
  id: z.string(), status: z.string(), expires_at: z.string(), qr_data_url: z.string().optional(),
}).strict() satisfies z.ZodType<Schemas['BilibiliLogin']>
export const biliLoginSchema = biliLoginObjectSchema.nullable()
export const microsoftLoginSchema = z.object({
  channel_id: z.string(), status: z.string(), user_code: z.string().optional(), verification_uri: z.string().optional(),
  verification_uri_complete: z.string().optional(), expires_at: z.string().optional(), error: z.string().optional(),
}).strict() satisfies z.ZodType<Schemas['MicrosoftLogin']>

export const runtimeSettingsSchema = z.object({
  poll_interval_sec: z.number().int().min(10).max(86400), request_rate: z.number().positive().max(10),
  request_concurrency: z.number().int().min(1).max(16), comment_enabled: z.boolean(),
  comment_track_n: z.number().int().min(1).max(50), comment_root_pages: z.number().int().min(1).max(10),
  comment_reply_pages: z.number().int().min(1).max(20), comment_batch_interval_sec: z.number().int().min(30).max(86400),
  log_level: z.enum(['debug', 'info', 'warn', 'error']), audit_log_retention_days: z.number().int().min(1).max(3650),
  relation_refresh_interval_sec: z.number().int().min(60).max(86400), space_reconcile_interval_sec: z.number().int().min(300).max(604800),
  max_dynamic_pages: z.number().int().min(1).max(20), risk_pause_sec: z.number().int().min(60).max(3600),
  delivery_concurrency: z.number().int().min(1).max(32), backlog_alert_count: z.number().int().min(1).max(100000),
  backlog_alert_age_sec: z.number().int().min(60).max(86400),
  delivery_retry_delays_sec: z.tuple([
    z.number().int().min(1).max(86400), z.number().int().min(1).max(86400), z.number().int().min(1).max(86400),
    z.number().int().min(1).max(86400), z.number().int().min(1).max(86400),
  ]).refine(values => values.every((value, index) => index === 0 || value >= values[index - 1]), '重试阶段必须单调不减'),
}).strict()

export const dynamicLinkSchema = z.object({ text: z.string(), url: z.string() }).strict() satisfies z.ZodType<Schemas['DynamicLink']>
export const dynamicMediaSchema = z.object({ kind: z.enum(['cover', 'image']), url: z.string(), width: z.number().int().optional(), height: z.number().int().optional() }).strict() satisfies z.ZodType<Schemas['DynamicMedia']>
export const dynamicStatsSchema = z.object({ forwards: z.number().int(), comments: z.number().int(), likes: z.number().int() }).strict() satisfies z.ZodType<Schemas['DynamicStats']>
export const dynamicVideoSchema = z.object({ duration: z.string().optional(), views: z.string().optional(), danmaku: z.string().optional() }).strict() satisfies z.ZodType<Schemas['DynamicVideo']>
export const dynamicPreviewSchema: z.ZodType<Schemas['DynamicOriginal']> = z.lazy(() => z.object({
  id: z.string().optional(), uid: z.string().optional(), up_name: z.string().optional(), type: z.string().optional(), published_at: z.string().optional(),
  title: z.string().optional(), summary: z.string().optional(), description: z.string().optional(), url: z.string().optional(),
  target_url: z.string().optional(), badge: z.string().optional(), links: z.array(dynamicLinkSchema).optional(),
  media: z.array(dynamicMediaSchema).optional(), stats: dynamicStatsSchema.optional(), video: dynamicVideoSchema.optional(),
  original: dynamicPreviewSchema.optional(),
}).strict())
export const dynamicHistorySchema = z.object({
  id: z.string(), uid: z.string(), up_name: z.string(), type: z.string(), published_at: z.string(), discovered_at: z.string(),
  baseline: z.boolean(), title: z.string().optional(), summary: z.string(), description: z.string().optional(),
  url: z.string(), target_url: z.string().optional(), badge: z.string().optional(), links: z.array(dynamicLinkSchema).optional(),
  media: z.array(dynamicMediaSchema).optional(), stats: dynamicStatsSchema.optional(), video: dynamicVideoSchema.optional(),
  original: dynamicPreviewSchema.optional(), commentable: z.boolean().optional(), comment_type: z.number().int().optional(),
  comment_oid: z.string().optional(), comment_count: z.number().int().optional(),
}).strict() satisfies z.ZodType<Schemas['DynamicHistory']>
export const commentHistorySchema = z.object({
  rpid: z.string(), up_uid: z.string(), up_name: z.string(), content_type: z.string().optional(), content_id: z.string().optional(),
  content_title: z.string().optional(), content_url: z.string().optional(), published_at: z.string(), discovered_at: z.string(),
  baseline: z.boolean(), incomplete: z.boolean().optional(),
}).strict() satisfies z.ZodType<Schemas['CommentHistory']>
const commentThreadEntrySchema = z.object({
  rpid: z.string(), parent: z.string().optional(), mid: z.string(), name: z.string(), message: z.string(), time: z.string(),
  is_up: z.boolean().optional(), is_trigger: z.boolean().optional(),
}).strict() satisfies z.ZodType<Schemas['CommentNode']>
export const commentDetailSchema = z.object({
  rpid: z.string(), up_uid: z.string(), up_name: z.string(), content_type: z.string(), content_id: z.string(),
  content_title: z.string().optional(), content_url: z.string(), published_at: z.string(), incomplete: z.boolean().optional(),
  thread: z.array(commentThreadEntrySchema),
}).strict() satisfies z.ZodType<Schemas['CommentNotification']>
export const auditLogSchema = z.object({
  id: z.number().int(), occurred_at: z.string(), request_id: z.string(), actor: z.string(),
  session_id: z.string(), remote_ip: z.string(), user_agent: z.string(), action: z.string(), resource_type: z.string(),
  resource_id: z.string(), outcome: z.enum(['success', 'failure', 'denied']), http_method: z.string(), route: z.string(),
  status_code: z.number().int(), error_code: z.string().optional(), duration_ms: z.number().int(), details: z.record(z.string(), z.unknown()),
}).strict() satisfies z.ZodType<Schemas['AuditLog']>

export const cursorPageSchema = <T extends z.ZodType>(item: T) => z.object({
  items: z.array(item),
  page: z.object({ next_cursor: z.string(), has_more: z.boolean() }).strict(),
}).strict()
export const deliveryPageSchema = cursorPageSchema(deliverySchema)
export const dynamicHistoryPageSchema = cursorPageSchema(dynamicHistorySchema)
export const commentHistoryPageSchema = cursorPageSchema(commentHistorySchema)
export const auditLogPageSchema = cursorPageSchema(auditLogSchema)

export const sessionStateSchema = z.object({ setup_required: z.boolean(), authenticated: z.boolean(), csrf_token: z.string().optional() }).strict() satisfies z.ZodType<Schemas['Session']>
export const csrfStateSchema = z.object({ csrf_token: z.string() }).strict() satisfies z.ZodType<Schemas['CSRFToken']>
export const sentStatusSchema = z.object({ status: z.literal('sent') }).strict() satisfies z.ZodType<Schemas['SentStatus']>
export const queuedStatusSchema = z.object({ status: z.literal('queued') }).strict() satisfies z.ZodType<Schemas['QueuedStatus']>
export const emptyResponseSchema = z.undefined()

export const aiWorkerStatusSchema = z.object({
  connected: z.boolean(), version: z.string().optional(), yt_dlp_available: z.boolean(), ffmpeg_available: z.boolean(),
  active_transcriptions: z.number().int(), active_summaries: z.number().int(), cache_bytes: z.number().int(),
  last_checked_at: z.string().optional(), last_error: z.string().optional(),
}).strict()
export const aiProfileSchema = z.object({
  id: z.string(), name: z.string(), kind: z.enum(['transcription', 'text']), base_url: z.string(), model: z.string(),
  language: z.string().optional(), prompt: z.string().optional(), temperature: z.number().optional(), max_output_tokens: z.number().int().optional(),
  context_window_chars: z.number().int().optional(), timeout_sec: z.number().int(), default: z.boolean(), configured_secrets: z.array(z.string()),
  created_at: z.string(), updated_at: z.string(),
}).strict()
export const aiPromptSchema = z.object({
  id: z.string(), name: z.string(), system_prompt: z.string(), chunk_prompt: z.string(), reduce_prompt: z.string(),
  default: z.boolean(), created_at: z.string(), updated_at: z.string(),
}).strict()
export const aiSegmentSchema = z.object({ start_ms: z.number().int(), end_ms: z.number().int(), text: z.string() }).strict()
export const aiTranscriptionResultSchema = z.object({
  bvid: z.string(), title: z.string(), pages: z.array(z.object({ page: z.number().int(), cid: z.string().optional(), title: z.string(), duration_ms: z.number().int(), segments: z.array(aiSegmentSchema) }).strict()), usage: z.record(z.string(), z.unknown()).optional(),
}).strict()
export const aiSummaryResultSchema = z.object({ markdown: z.string(), usage: z.record(z.string(), z.unknown()).optional() }).strict()
export const aiJobSchema = z.object({
  id: z.string(), client_request_id: z.string().optional(), kind: z.enum(['transcription', 'summary']),
  state: z.enum(['queued', 'running', 'succeeded', 'failed', 'canceled']), stage: z.string(), progress: z.number().int(),
  profile_id: z.string(), prompt_id: z.string().optional(), attempts: z.number().int(), error_code: z.string().optional(), last_error: z.string().optional(),
  input: z.unknown().optional(), result: z.union([aiTranscriptionResultSchema, aiSummaryResultSchema]).optional(),
  created_at: z.string(), started_at: z.string().optional(), finished_at: z.string().optional(), updated_at: z.string(),
}).strict()
export const aiJobPageSchema = z.object({ items: z.array(aiJobSchema), total: z.number().int(), limit: z.number().int(), offset: z.number().int() }).strict()
export const canceledStatusSchema = z.object({ status: z.literal('canceled') }).strict()
export const workerReachableSchema = z.object({ status: z.literal('worker_reachable') }).strict()

export const realtimeTopicSchema = z.enum(realtimeTopics)
export const websocketEnvelopeSchema = z.object({
  event: z.enum(['sync.required', 'resources.invalidated']),
  revision: z.number().int().nonnegative(),
  topics: z.array(realtimeTopicSchema),
}).strict()
