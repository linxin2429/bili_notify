import type { AuditLog, Delivery, RuntimeSettings } from '../shared/api/types'

export const settings: RuntimeSettings = {
  bilibili_dynamic_interval_sec: 30, bilibili_request_rate: 1, bilibili_request_concurrency: 2, bilibili_comments_enabled: true,
  bilibili_comment_track_n: 10, bilibili_comment_interval_sec: 60, bilibili_relation_refresh_interval_sec: 3600,
  bilibili_space_reconcile_interval_sec: 3600, bilibili_max_dynamic_pages: 5, bilibili_risk_pause_sec: 300,
  zsxq_dynamic_interval_sec: 60, zsxq_comment_interval_sec: 600, zsxq_comments_enabled: true, zsxq_request_rate: 1,
  zsxq_request_concurrency: 2, zsxq_risk_pause_sec: 600, zsxq_asset_max_file_mib: 500, zsxq_asset_total_budget_gib: 50,
  log_level: 'info', audit_log_retention_days: 90,
  delivery_concurrency: 4, backlog_alert_count: 100, backlog_alert_age_sec: 600, delivery_retry_delays_sec: [5, 30, 120, 600, 3600],
  ai_auto_processing_enabled: false,
}

export function makeDelivery(overrides: Partial<Delivery> = {}): Delivery {
  return { id: 'delivery', kind: 'content', channel_id: 'channel', state: 'pending', attempts: 0, next_at: '2026-08-06T00:00:00Z', created_at: '2026-08-06T00:00:00Z', ...overrides }
}

export function makeAudit(overrides: Partial<AuditLog> = {}): AuditLog {
  return { id: 1, occurred_at: '2026-08-06T10:00:00Z', request_id: 'request-42', actor: 'administrator', session_id: 'session', remote_ip: '192.0.2.1', user_agent: 'browser', action: 'channel.update', resource_type: 'channel', resource_id: 'channel', outcome: 'success', http_method: 'PUT', route: '/api/v3/channels/{id}', status_code: 200, duration_ms: 12, details: {}, ...overrides }
}
