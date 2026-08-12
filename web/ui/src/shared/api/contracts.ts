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

export const channelSchema = z.object({
  id: z.string(), name: z.string(), type: channelTypeSchema, enabled: z.boolean(),
  settings: z.record(z.string(), z.string()), configured_secrets: z.array(z.string()),
  created_at: z.string(), updated_at: z.string(),
}).strict() satisfies z.ZodType<Schemas['Channel']>

export const deliverySchema = z.object({
  id: z.string(), kind: z.enum(['content', 'comment', 'ai', 'system']), platform: z.enum(['bilibili', 'zsxq']).optional(),
  source_id: z.string().optional(), content_id: z.string().optional(), channel_id: z.string(), state: z.enum(['pending', 'blocked']),
  attempts: z.number().int(), next_at: z.string(), last_error: z.string().optional(), created_at: z.string(), title: z.string().optional(), summary: z.string().optional(),
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
  bilibili_dynamic_interval_sec: z.number().int().min(10).max(86400), bilibili_request_rate: z.number().positive().max(10),
  bilibili_request_concurrency: z.number().int().min(1).max(16), bilibili_comments_enabled: z.boolean(),
  bilibili_comment_track_n: z.number().int().min(1).max(50), bilibili_comment_interval_sec: z.number().int().min(30).max(86400),
  bilibili_relation_refresh_interval_sec: z.number().int().min(60).max(86400), bilibili_space_reconcile_interval_sec: z.number().int().min(300).max(604800),
  bilibili_max_dynamic_pages: z.number().int().min(1).max(20), bilibili_risk_pause_sec: z.number().int().min(60).max(3600),
  zsxq_dynamic_interval_sec: z.number().int().min(30).max(86400), zsxq_comment_interval_sec: z.number().int().min(30).max(86400),
  zsxq_comments_enabled: z.boolean(), zsxq_request_rate: z.number().positive().max(10), zsxq_request_concurrency: z.number().int().min(1).max(16),
  zsxq_risk_pause_sec: z.number().int().min(60).max(3600), zsxq_asset_max_file_mib: z.number().int().min(1).max(2048),
  zsxq_asset_total_budget_gib: z.number().int().min(1).max(10240),
  log_level: z.enum(['debug', 'info', 'warn', 'error']), audit_log_retention_days: z.number().int().min(1).max(3650),
  delivery_concurrency: z.number().int().min(1).max(32), backlog_alert_count: z.number().int().min(1).max(100000),
  backlog_alert_age_sec: z.number().int().min(60).max(86400),
  delivery_retry_delays_sec: z.tuple([
    z.number().int().min(1).max(86400), z.number().int().min(1).max(86400), z.number().int().min(1).max(86400),
    z.number().int().min(1).max(86400), z.number().int().min(1).max(86400),
  ]).refine(values => values.every((value, index) => index === 0 || value >= values[index - 1]), '重试阶段必须单调不减'),
  ai_auto_processing_enabled: z.boolean(),
}).strict()

export const platformAccountSchema = z.object({
  platform: z.enum(['bilibili', 'zsxq']), external_id: z.string().optional(), display_name: z.string().optional(), masked_phone: z.string().optional(),
  status: z.enum(['disconnected', 'connected', 'invalid', 'risk_paused']), verified_at: z.string().optional(), updated_at: z.string().optional(),
  risk_paused_until: z.string().optional(), last_error: z.string().optional(),
}).strict() satisfies z.ZodType<Schemas['PlatformAccount']>

export const sourceSchema = z.object({
  id: z.string(), platform: z.enum(['bilibili', 'zsxq']), type: z.enum(['up', 'planet']), external_id: z.string(), name: z.string(), note: z.string().optional(),
  owner_id: z.string().optional(), owner_name: z.string().optional(), enabled: z.boolean(), baseline_state: z.enum(['pending', 'running', 'complete', 'failed']),
  backfill_cursor: z.string().optional(), high_watermark: z.string().optional(), backfill_done: z.number().int(), backfill_total: z.number().int(),
  last_poll_at: z.string().optional(), last_success_at: z.string().optional(), last_comment_at: z.string().optional(), sync_lag_sec: z.number().int(),
  last_error: z.string().optional(), consecutive_fails: z.number().int(),
}).strict() satisfies z.ZodType<Schemas['Source']>

export const contentSchema = z.object({
  id: z.string(), platform: z.enum(['bilibili', 'zsxq']), source_id: z.string(), external_id: z.string(),
  author_id: z.string().optional(), author_name: z.string().optional(), upstream_type: z.string(),
  type: z.enum(['dynamic', 'video', 'article', 'discussion', 'question', 'answer', 'task', 'long_article']),
  title: z.string().optional(), text: z.string().optional(), safe_html: z.string().optional(), url: z.string().optional(),
  published_at: z.string(), updated_at: z.string().optional(), first_seen_at: z.string(), last_synced_at: z.string(),
  deleted_at: z.string().optional(), stats: z.record(z.string(), z.number().int()).optional(), tree_incomplete: z.boolean().optional(), baseline: z.boolean(),
}).strict() satisfies z.ZodType<Schemas['Content']>

export const attachmentSchema = z.object({
  id: z.string(), content_id: z.string(), external_id: z.string(), type: z.enum(['image', 'file', 'audio', 'video', 'link']),
  file_name: z.string().optional(), mime: z.string().optional(), size: z.number().int().optional(), width: z.number().int().optional(),
  height: z.number().int().optional(), duration_sec: z.number().int().optional(), remote_host: z.string().optional(), localized: z.boolean(), localize_error: z.string().optional(),
}).strict() satisfies z.ZodType<Schemas['Attachment']>

export const commentTreeNodeSchema: z.ZodType<Schemas['CommentTreeNode']> = z.lazy(() => z.object({
  id: z.string(), platform: z.enum(['bilibili', 'zsxq']), content_id: z.string(), root_id: z.string().optional(), parent_id: z.string().optional(),
  author_id: z.string().optional(), author_role: z.enum(['owner', 'admin', 'guest', 'partner', 'member', 'up']),
  updated_at: z.string().optional(), deleted_at: z.string().optional(), media: z.array(attachmentSchema).optional(),
  children: z.array(commentTreeNodeSchema).optional(), name: z.string(), message: z.string(), published_at: z.string(), is_trigger: z.boolean().optional(),
}).strict())

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
export const contentPageSchema = cursorPageSchema(contentSchema)
export const contentDetailSchema = z.object({ content: contentSchema, attachments: z.array(attachmentSchema) }).strict()
export const commentTreeSchema = z.object({ children: z.array(commentTreeNodeSchema), incomplete: z.boolean() }).strict()
export const deliveryPageSchema = cursorPageSchema(deliverySchema)
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
  context_window_chars: z.number().int().optional(), timeout_sec: z.number().int(), enabled: z.boolean(), default: z.boolean(), configured_secrets: z.array(z.string()),
  created_at: z.string(), updated_at: z.string(),
}).strict()
export const aiProfileTestResultSchema = z.object({
  ok: z.boolean(), latency_ms: z.number().int(), message: z.string(), error_code: z.string().optional(),
  provider_http_status: z.number().int().optional(), provider_error: z.string().optional(),
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
  state: z.enum(['queued', 'running', 'succeeded', 'failed', 'canceled', 'skipped']), stage: z.string(), progress: z.number().int(),
  profile_id: z.string(), prompt_id: z.string().optional(), origin: z.enum(['workbench', 'dynamic']), source_content_id: z.string().optional(),
  depends_on_job_id: z.string().optional(), attempts: z.number().int(), error_code: z.string().optional(), last_error: z.string().optional(),
  source: z.object({ content_id: z.string(), bvid: z.string().optional(), author: z.string().optional(), title: z.string().optional(), url: z.string().optional() }).strict().optional(),
  transcription_input: z.object({ bvid: z.string(), page: z.number().int().optional() }).strict().optional(),
  summary_input: z.object({ text: z.string().optional(), transcription_job_id: z.string().optional() }).strict().optional(),
  transcription_result: aiTranscriptionResultSchema.optional(), summary_result: aiSummaryResultSchema.optional(),
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
