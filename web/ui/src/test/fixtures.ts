import type { AuditLog, Delivery, RuntimeSettings } from '../types'

export const settings: RuntimeSettings = {
  poll_interval_sec: 30, request_rate: 1, request_concurrency: 2, comment_enabled: true, comment_track_n: 10,
  comment_root_pages: 2, comment_reply_pages: 3, comment_batch_interval_sec: 60, log_level: 'info', audit_log_retention_days: 90,
  relation_refresh_interval_sec: 3600, space_reconcile_interval_sec: 3600, max_dynamic_pages: 5, risk_pause_sec: 300,
  delivery_concurrency: 4, backlog_alert_count: 100, backlog_alert_age_sec: 600, delivery_retry_delays_sec: [5, 30, 120, 600, 3600],
}

export function makeDelivery(overrides: Partial<Delivery> = {}): Delivery {
  return { id: 'delivery', channel_id: 'channel', state: 'pending', attempts: 0, next_at: '2026-08-06T00:00:00Z', created_at: '2026-08-06T00:00:00Z', ...overrides }
}

export function makeAudit(overrides: Partial<AuditLog> = {}): AuditLog {
  return { id: 1, occurred_at: '2026-08-06T10:00:00Z', request_id: 'request-42', actor: 'administrator', session_id: 'session', remote_ip: '192.0.2.1', user_agent: 'browser', action: 'channel.update', resource_type: 'channel', resource_id: 'channel', outcome: 'success', http_method: 'PUT', route: '/api/v2/channels/{id}', status_code: 200, duration_ms: 12, details: {}, ...overrides }
}
