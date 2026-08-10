import type { RuntimeSettings } from '../shared/api/types'

export interface RuntimeSettingsForm {
  pollSec: string; requestRate: string; concurrency: string; commentEnabled: boolean; commentTrackN: string; commentBatchSec: string
  relationRefreshSec: string; spaceReconcileSec: string; maxDynamicPages: string; riskPauseSec: string
  zsxqDynamicSec: string; zsxqCommentSec: string; zsxqCommentsEnabled: boolean; zsxqRequestRate: string; zsxqConcurrency: string
  zsxqRiskPauseSec: string; zsxqAssetMaxMiB: string; zsxqAssetBudgetGiB: string
  logLevel: RuntimeSettings['log_level']; auditRetentionDays: string; deliveryConcurrency: string
  backlogAlertCount: string; backlogAlertAgeSec: string; retryDelaysSec: [string, string, string, string, string]
  aiAutoProcessingEnabled: boolean
}

export type RuntimeSettingsParseResult = { ok: true; value: RuntimeSettings } | { ok: false; error: string }

export function runtimeSettingsToForm(settings: RuntimeSettings): RuntimeSettingsForm {
  return {
    pollSec: String(settings.bilibili_dynamic_interval_sec), requestRate: String(settings.bilibili_request_rate), concurrency: String(settings.bilibili_request_concurrency),
    commentEnabled: settings.bilibili_comments_enabled, commentTrackN: String(settings.bilibili_comment_track_n), commentBatchSec: String(settings.bilibili_comment_interval_sec),
    relationRefreshSec: String(settings.bilibili_relation_refresh_interval_sec), spaceReconcileSec: String(settings.bilibili_space_reconcile_interval_sec),
    maxDynamicPages: String(settings.bilibili_max_dynamic_pages), riskPauseSec: String(settings.bilibili_risk_pause_sec),
    zsxqDynamicSec: String(settings.zsxq_dynamic_interval_sec), zsxqCommentSec: String(settings.zsxq_comment_interval_sec), zsxqCommentsEnabled: settings.zsxq_comments_enabled,
    zsxqRequestRate: String(settings.zsxq_request_rate), zsxqConcurrency: String(settings.zsxq_request_concurrency), zsxqRiskPauseSec: String(settings.zsxq_risk_pause_sec),
    zsxqAssetMaxMiB: String(settings.zsxq_asset_max_file_mib), zsxqAssetBudgetGiB: String(settings.zsxq_asset_total_budget_gib),
    logLevel: settings.log_level, auditRetentionDays: String(settings.audit_log_retention_days), deliveryConcurrency: String(settings.delivery_concurrency),
    backlogAlertCount: String(settings.backlog_alert_count), backlogAlertAgeSec: String(settings.backlog_alert_age_sec),
    retryDelaysSec: settings.delivery_retry_delays_sec.map(String) as RuntimeSettingsForm['retryDelaysSec'], aiAutoProcessingEnabled: settings.ai_auto_processing_enabled,
  }
}

export function parseRuntimeSettingsForm(input: RuntimeSettingsForm): RuntimeSettingsParseResult {
  const retryDelays: RuntimeSettings['delivery_retry_delays_sec'] = input.retryDelaysSec.map(Number)
  const value: RuntimeSettings = {
    bilibili_dynamic_interval_sec: Number(input.pollSec), bilibili_request_rate: Number(input.requestRate), bilibili_request_concurrency: Number(input.concurrency),
    bilibili_comments_enabled: input.commentEnabled, bilibili_comment_track_n: Number(input.commentTrackN), bilibili_comment_interval_sec: Number(input.commentBatchSec),
    bilibili_relation_refresh_interval_sec: Number(input.relationRefreshSec), bilibili_space_reconcile_interval_sec: Number(input.spaceReconcileSec),
    bilibili_max_dynamic_pages: Number(input.maxDynamicPages), bilibili_risk_pause_sec: Number(input.riskPauseSec),
    zsxq_dynamic_interval_sec: Number(input.zsxqDynamicSec), zsxq_comment_interval_sec: Number(input.zsxqCommentSec), zsxq_comments_enabled: input.zsxqCommentsEnabled,
    zsxq_request_rate: Number(input.zsxqRequestRate), zsxq_request_concurrency: Number(input.zsxqConcurrency), zsxq_risk_pause_sec: Number(input.zsxqRiskPauseSec),
    zsxq_asset_max_file_mib: Number(input.zsxqAssetMaxMiB), zsxq_asset_total_budget_gib: Number(input.zsxqAssetBudgetGiB),
    log_level: input.logLevel, audit_log_retention_days: Number(input.auditRetentionDays), delivery_concurrency: Number(input.deliveryConcurrency),
    backlog_alert_count: Number(input.backlogAlertCount), backlog_alert_age_sec: Number(input.backlogAlertAgeSec), delivery_retry_delays_sec: retryDelays,
    ai_auto_processing_enabled: input.aiAutoProcessingEnabled,
  }
  if (!integerIn(value.bilibili_dynamic_interval_sec, 10, 86400)) return failure('B 站轮询间隔必须是 10 到 86400 秒的整数')
  if (!rateIn(value.bilibili_request_rate)) return failure('B 站请求速率必须在 (0, 10] 内')
  if (!integerIn(value.bilibili_request_concurrency, 1, 16)) return failure('B 站请求并发数必须是 1 到 16 的整数')
  if (!integerIn(value.bilibili_comment_track_n, 1, 50)) return failure('B 站评论跟踪条数必须是 1 到 50 的整数')
  if (!integerIn(value.bilibili_comment_interval_sec, 30, 86400)) return failure('B 站评论间隔必须是 30 到 86400 秒的整数')
  if (!integerIn(value.bilibili_relation_refresh_interval_sec, 60, 86400)) return failure('关注关系刷新间隔必须是 60 到 86400 秒的整数')
  if (!integerIn(value.bilibili_space_reconcile_interval_sec, 300, 604800)) return failure('空间校验间隔必须是 300 到 604800 秒的整数')
  if (!integerIn(value.bilibili_max_dynamic_pages, 1, 20)) return failure('动态翻页上限必须是 1 到 20 的整数')
  if (!integerIn(value.bilibili_risk_pause_sec, 60, 3600)) return failure('B 站风控暂停必须是 60 到 3600 秒的整数')
  if (!integerIn(value.zsxq_dynamic_interval_sec, 30, 86400) || !integerIn(value.zsxq_comment_interval_sec, 30, 86400)) return failure('知识星球同步间隔必须是 30 到 86400 秒的整数')
  if (!rateIn(value.zsxq_request_rate) || !integerIn(value.zsxq_request_concurrency, 1, 16)) return failure('知识星球请求速率或并发数无效')
  if (!integerIn(value.zsxq_risk_pause_sec, 60, 3600)) return failure('知识星球风控暂停必须是 60 到 3600 秒的整数')
  if (!integerIn(value.zsxq_asset_max_file_mib, 1, 2048) || !integerIn(value.zsxq_asset_total_budget_gib, 1, 10240)) return failure('知识星球附件限制无效')
  if (!integerIn(value.audit_log_retention_days, 1, 3650) || !integerIn(value.delivery_concurrency, 1, 32)) return failure('日志保留或投递并发数无效')
  if (!integerIn(value.backlog_alert_count, 1, 100000) || !integerIn(value.backlog_alert_age_sec, 60, 86400)) return failure('积压告警阈值无效')
  if (!retryDelays.every(delay => integerIn(delay, 1, 86400)) || !retryDelays.every((delay, index) => index === 0 || delay >= retryDelays[index - 1])) return failure('五段重试时间必须为有效的非递减整数')
  return { ok: true, value }
}

function integerIn(value: number, minimum: number, maximum: number): boolean { return Number.isInteger(value) && value >= minimum && value <= maximum }
function rateIn(value: number): boolean { return Number.isFinite(value) && value > 0 && value <= 10 }
function failure(error: string): RuntimeSettingsParseResult { return { ok: false, error } }
