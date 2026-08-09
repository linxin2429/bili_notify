import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App'

describe('session boundary', () => {
  afterEach(() => vi.unstubAllGlobals())
  it('shows a retryable bootstrap error instead of an infinite loading screen', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { code: 'unavailable', message: '服务暂不可用' } }), { status: 503, headers: { 'Content-Type': 'application/json' } })))
    render(<App />)
    expect(await screen.findByRole('heading', { name: '无法连接管理服务' }, { timeout: 5_000 })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重新连接' })).toBeEnabled()
  })

  it('keeps anonymous sessions on the login screen', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => json({ setup_required: false, authenticated: false })))
    render(<App />)
    expect(await screen.findByText('登录实时管理台')).toBeInTheDocument()
  })

  it('loads every lazy route, cycles theme and logs out from the application shell', async () => {
    window.history.pushState({}, '', '/overview')
    vi.stubGlobal('WebSocket', FakeWebSocket)
    let loggedOut = false
    const fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = requestPath(input)
      if (path === '/api/v2/session' && init?.method === 'DELETE') { loggedOut = true; return new Response(null, { status: 204 }) }
      if (path === '/api/v2/session' && init?.method === 'POST') return json({ csrf_token: 'csrf' })
      if (path === '/api/v2/session') return json(loggedOut ? { setup_required: false, authenticated: false } : { setup_required: false, authenticated: true, csrf_token: 'csrf' })
      if (path === '/api/v2/runtime') return json(runtime)
      if (path === '/api/v2/settings') return json(settings)
      if (path === '/api/v2/ups') return json([up])
      if (path === '/api/v2/channels') return json([channel])
      if (path === '/api/v2/bilibili-login') return json(null)
      if (path === '/api/v2/microsoft-logins') return json([])
      if (path.startsWith('/api/v2/deliveries')) return json({ items: [], page: emptyPage })
      if (path.startsWith('/api/v2/dynamics')) return json({ items: [], page: emptyPage })
      if (path.startsWith('/api/v2/comments')) return json({ items: [], page: emptyPage })
      if (path.startsWith('/api/v2/audit-logs')) return json({ items: [], page: emptyPage })
      throw new Error(`unexpected request ${path}`)
    })
    vi.stubGlobal('fetch', fetch)
    const user = userEvent.setup(); render(<App />)
    if (await screen.findByText(/登录实时管理台|正在连接/).then(node => node.textContent === '登录实时管理台')) {
      await user.type(screen.getByLabelText('管理员密码'), 'password'); await user.click(screen.getByRole('button', { name: '登录' }))
    }
    expect(await screen.findByRole('heading', { name: '运行概览' })).toBeInTheDocument()
    FakeWebSocket.latest.onopen?.(); expect(await screen.findByText('实时')).toBeInTheDocument()
    for (const [label, heading] of [['UP 主', 'UP 主'], ['通知渠道', '通知渠道'], ['投递队列', '投递队列'], ['历史', '历史内容'], ['操作日志', '操作日志'], ['设置', '设置']] as const) {
      await user.click(screen.getAllByRole('link', { name: new RegExp(label) })[0]); expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument()
    }
    const theme = screen.getByLabelText('主题：跟随系统'); await user.click(theme); await user.click(screen.getByLabelText('主题：亮色')); await user.click(screen.getByLabelText('主题：暗色'))
    await user.click(screen.getByLabelText('退出登录')); await waitFor(() => expect(fetch).toHaveBeenCalledWith('/api/v2/session', expect.objectContaining({ method: 'DELETE' })))
  })
})

class FakeWebSocket {
  static latest: FakeWebSocket
  onopen: (() => void) | null = null; onmessage: ((event: { data: string }) => void) | null = null; onerror: (() => void) | null = null; onclose: (() => void) | null = null
  constructor(readonly url: string) { FakeWebSocket.latest = this }
  close() { /* the application cleanup owns the close */ }
}

const runtime = { status: { auth_valid: true, bili_account: { uid: '1', name: '账号' }, up_count: 1, channel_count: 1, outbox_depth: 0, ready: true }, timezone: 'Asia/Shanghai', updated_at: '2026-08-09T10:00:00Z' }
const settings = { poll_interval_sec: 30, request_rate: 1, request_concurrency: 2, comment_enabled: true, comment_track_n: 10, comment_root_pages: 2, comment_reply_pages: 3, comment_batch_interval_sec: 60, log_level: 'info', audit_log_retention_days: 90, relation_refresh_interval_sec: 3600, space_reconcile_interval_sec: 3600, max_dynamic_pages: 5, risk_pause_sec: 300, delivery_concurrency: 4, backlog_alert_count: 100, backlog_alert_age_sec: 600, delivery_retry_delays_sec: [5, 30, 120, 600, 3600] }
const up = { uid: '42', name: 'UP', enabled: true, baseline_ready: true, consecutive_fail: 0, follow_state: 'followed', collection_route: 'feed_all' }
const channel = { id: 'mail', name: '邮件', type: 'email', enabled: true, settings: {}, configured_secrets: [], created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' }
const emptyPage = { next_cursor: '', has_more: false }
function json(value: unknown) { return new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } }) }
function requestPath(input: string | URL | Request) { return typeof input === 'string' ? input : input instanceof URL ? input.href : input.url }
