import type React from 'react'
import { ThemeProvider, createTheme } from '@mui/material'
import { MemoryRouter } from 'react-router-dom'
import { render } from '@testing-library/react'
import type { AuditLog, Channel, DashboardSnapshot, Delivery, DynamicHistoryItem, RuntimeSettings, UP } from '../types'

export const settings: RuntimeSettings = {
  poll_interval_sec: 30, request_rate: 2, request_concurrency: 4,
  comment_enabled: true, comment_track_n: 10, comment_root_pages: 2,
  comment_reply_pages: 5, comment_batch_interval_sec: 120,
}

export function makeUP(overrides: Partial<UP> = {}): UP {
  return { uid: '42', name: '测试 UP', enabled: true, baseline_ready: true, consecutive_fail: 0, follow_state: 'followed', collection_route: 'feed_all', ...overrides }
}

export function makeChannel(overrides: Partial<Channel> = {}): Channel {
  return { id: 'channel', name: '测试渠道', type: 'wecom', enabled: true, settings: {}, configured_secrets: ['webhook'], created_at: '2026-08-01T00:00:00Z', updated_at: '2026-08-01T00:00:00Z', ...overrides }
}

export function makeDelivery(overrides: Partial<Delivery> = {}): Delivery {
  return { id: 'delivery', channel_id: 'channel', state: 'blocked', attempts: 1, next_at: '2026-08-05T15:00:00Z', created_at: '2026-08-05T14:00:00Z', ...overrides }
}

export function makeSnapshot(overrides: Partial<DashboardSnapshot> = {}): DashboardSnapshot {
  return {
    status: { auth_valid: true, last_success_at: new Date().toISOString(), up_count: 1, channel_count: 1, outbox_depth: 0, ready: true },
    settings, ups: [makeUP()], channels: [makeChannel()], deliveries: [], microsoft_logins: [],
    timezone: 'Asia/Shanghai', updated_at: '2026-08-06T12:00:00Z', ...overrides,
  }
}

export function makeDynamic(overrides: Partial<DynamicHistoryItem> = {}): DynamicHistoryItem {
  return { id: 'dynamic', uid: '42', up_name: '测试 UP', type: 'DYNAMIC_TYPE_WORD', published_at: '2026-08-06T10:00:00Z', discovered_at: '2026-08-06T10:00:01Z', baseline: false, ...overrides }
}

export function makeAudit(overrides: Partial<AuditLog> = {}): AuditLog {
  return { id: 1, occurred_at: '2026-08-06T10:00:00Z', request_id: 'request-42', actor: 'administrator', session_id: 'session', remote_ip: '192.0.2.1', user_agent: 'browser', action: 'channel.update', resource_type: 'channel', resource_id: 'channel', outcome: 'success', http_method: 'PUT', route: '/api/v1/channels/{id}', status_code: 200, duration_ms: 12, details: {}, ...overrides }
}

export function renderRoute(ui: React.ReactElement, initialEntry = '/') {
  return render(<ThemeProvider theme={createTheme()}><MemoryRouter initialEntries={[initialEntry]} future={{ v7_startTransition: true, v7_relativeSplatPath: true }}>{ui}</MemoryRouter></ThemeProvider>)
}
