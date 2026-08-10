import type { RuntimeSettings } from '../shared/api/types'

export interface RuntimeSettingsForm {
  pollSec: string
  requestRate: string
  concurrency: string
  commentEnabled: boolean
  commentTrackN: string
  commentRootPages: string
  commentReplyPages: string
  commentBatchSec: string
  logLevel: RuntimeSettings['log_level']
  auditRetentionDays: string
  relationRefreshSec: string
  spaceReconcileSec: string
  maxDynamicPages: string
  riskPauseSec: string
  deliveryConcurrency: string
  backlogAlertCount: string
  backlogAlertAgeSec: string
  retryDelaysSec: [string, string, string, string, string]
  aiAutoProcessingEnabled: boolean
}

export type RuntimeSettingsParseResult = { ok: true; value: RuntimeSettings } | { ok: false; error: string }

export function runtimeSettingsToForm(settings: RuntimeSettings): RuntimeSettingsForm {
  return {
    pollSec: String(settings.poll_interval_sec), requestRate: String(settings.request_rate), concurrency: String(settings.request_concurrency),
    commentEnabled: settings.comment_enabled, commentTrackN: String(settings.comment_track_n), commentRootPages: String(settings.comment_root_pages),
    commentReplyPages: String(settings.comment_reply_pages), commentBatchSec: String(settings.comment_batch_interval_sec), logLevel: settings.log_level,
    auditRetentionDays: String(settings.audit_log_retention_days),
    relationRefreshSec: String(settings.relation_refresh_interval_sec), spaceReconcileSec: String(settings.space_reconcile_interval_sec),
    maxDynamicPages: String(settings.max_dynamic_pages), riskPauseSec: String(settings.risk_pause_sec),
    deliveryConcurrency: String(settings.delivery_concurrency), backlogAlertCount: String(settings.backlog_alert_count),
    backlogAlertAgeSec: String(settings.backlog_alert_age_sec), retryDelaysSec: settings.delivery_retry_delays_sec.map(String) as RuntimeSettingsForm['retryDelaysSec'],
    aiAutoProcessingEnabled: settings.ai_auto_processing_enabled,
  }
}

export function parseRuntimeSettingsForm(input: RuntimeSettingsForm): RuntimeSettingsParseResult {
  const retryDelays: RuntimeSettings['delivery_retry_delays_sec'] = [
    Number(input.retryDelaysSec[0]), Number(input.retryDelaysSec[1]), Number(input.retryDelaysSec[2]),
    Number(input.retryDelaysSec[3]), Number(input.retryDelaysSec[4]),
  ]
  const value: RuntimeSettings = {
    poll_interval_sec: Number(input.pollSec), request_rate: Number(input.requestRate), request_concurrency: Number(input.concurrency),
    comment_enabled: input.commentEnabled, comment_track_n: Number(input.commentTrackN), comment_root_pages: Number(input.commentRootPages),
    comment_reply_pages: Number(input.commentReplyPages), comment_batch_interval_sec: Number(input.commentBatchSec), log_level: input.logLevel,
    audit_log_retention_days: Number(input.auditRetentionDays),
    relation_refresh_interval_sec: Number(input.relationRefreshSec), space_reconcile_interval_sec: Number(input.spaceReconcileSec),
    max_dynamic_pages: Number(input.maxDynamicPages), risk_pause_sec: Number(input.riskPauseSec),
    delivery_concurrency: Number(input.deliveryConcurrency), backlog_alert_count: Number(input.backlogAlertCount),
    backlog_alert_age_sec: Number(input.backlogAlertAgeSec), delivery_retry_delays_sec: retryDelays,
    ai_auto_processing_enabled: input.aiAutoProcessingEnabled,
  }
  if (!integerIn(value.poll_interval_sec, 10, 86400)) return failure('轮询间隔必须是 10 到 86400 秒的整数')
  if (!Number.isFinite(value.request_rate) || !(value.request_rate > 0 && value.request_rate <= 10)) return failure('请求速率必须在 (0, 10] 内')
  if (!integerIn(value.request_concurrency, 1, 16)) return failure('请求并发数必须是 1 到 16 的整数')
  if (!integerIn(value.comment_track_n, 1, 50)) return failure('评论跟踪条数必须是 1 到 50 的整数')
  if (!integerIn(value.comment_root_pages, 1, 10)) return failure('根评论页数必须是 1 到 10 的整数')
  if (!integerIn(value.comment_reply_pages, 1, 20)) return failure('子评论页数必须是 1 到 20 的整数')
  if (!integerIn(value.comment_batch_interval_sec, 30, 86400)) return failure('评论批次间隔必须是 30 到 86400 秒的整数')
  if (!integerIn(value.audit_log_retention_days, 1, 3650)) return failure('审计日志保留天数必须是 1 到 3650 的整数')
  if (!integerIn(value.relation_refresh_interval_sec, 60, 86400)) return failure('关注关系刷新间隔必须是 60 到 86400 秒的整数')
  if (!integerIn(value.space_reconcile_interval_sec, 300, 604800)) return failure('空间校验间隔必须是 300 到 604800 秒的整数')
  if (!integerIn(value.max_dynamic_pages, 1, 20)) return failure('动态翻页上限必须是 1 到 20 的整数')
  if (!integerIn(value.risk_pause_sec, 60, 3600)) return failure('风控暂停时长必须是 60 到 3600 秒的整数')
  if (!integerIn(value.delivery_concurrency, 1, 32)) return failure('投递并发数必须是 1 到 32 的整数')
  if (!integerIn(value.backlog_alert_count, 1, 100000)) return failure('积压条数阈值必须是 1 到 100000 的整数')
  if (!integerIn(value.backlog_alert_age_sec, 60, 86400)) return failure('积压时长阈值必须是 60 到 86400 秒的整数')
  if (!retryDelays.every(delay => integerIn(delay, 1, 86400))) return failure('五段重试时间必须是 1 到 86400 秒的整数')
  if (!retryDelays.every((delay, index) => index === 0 || delay >= retryDelays[index - 1])) return failure('五段重试时间必须单调不减')
  return { ok: true, value }
}

function integerIn(value: number, minimum: number, maximum: number): boolean {
  return Number.isInteger(value) && value >= minimum && value <= maximum
}

function failure(error: string): RuntimeSettingsParseResult { return { ok: false, error } }
