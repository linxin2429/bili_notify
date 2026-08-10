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
      if (path === '/api/v2/ai/status') return json({ connected: true, version: 'test', yt_dlp_available: true, ffmpeg_available: true, active_transcriptions: 0, active_summaries: 0, cache_bytes: 1024 })
      if (path === '/api/v2/ai/profiles' && init?.method === 'POST') return json(aiProfiles[0], 201)
      if (path === '/api/v2/ai/profiles') return json(aiProfiles)
      if (path.endsWith('/test') && path.startsWith('/api/v2/ai/profiles/')) return json({ ok: true, latency_ms: 12, message: '模型响应正常', provider_http_status: 200 })
      if (path === '/api/v2/ai/prompts' && init?.method === 'POST') return json(aiPrompts[0], 201)
      if (path === '/api/v2/ai/prompts') return json(aiPrompts)
      if (path === '/api/v2/ai/transcriptions' || path === '/api/v2/ai/summaries') return json(aiJobs[0], 202)
      if (path.endsWith('/retry')) return json({ status: 'queued' }, 202)
      if (path === '/api/v2/ai/jobs/job-done') return json(aiJobDetail)
      if (path.startsWith('/api/v2/ai/jobs')) return json({ items: aiJobs, total: aiJobs.length, limit: 50, offset: 0 })
      throw new Error(`unexpected request ${path}`)
    })
    vi.stubGlobal('fetch', fetch)
    const user = userEvent.setup(); render(<App />)
    if (await screen.findByText(/登录实时管理台|正在连接/).then(node => node.textContent === '登录实时管理台')) {
      await user.type(screen.getByLabelText('管理员密码'), 'password'); await user.click(screen.getByRole('button', { name: '登录' }))
    }
    expect(await screen.findByRole('heading', { name: '运行概览' })).toBeInTheDocument()
    FakeWebSocket.latest.onopen?.(); expect(await screen.findByText('实时')).toBeInTheDocument()
    for (const [label, heading] of [['UP 主', 'UP 主'], ['通知渠道', '通知渠道'], ['投递队列', '投递队列'], ['历史', '历史内容'], ['操作日志', '操作日志']] as const) {
      await user.click(screen.getAllByRole('link', { name: new RegExp(`^${label}$`) })[0]); expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument()
    }
    await user.click(screen.getAllByRole('link', { name: /^AI 工作台$/ })[0]); expect(await screen.findByRole('heading', { name: 'AI 工作台' })).toBeInTheDocument()
    await user.type(screen.getByLabelText('BVID'), 'BV1xx411c7mD'); await user.clear(screen.getByLabelText('分 P')); await user.type(screen.getByLabelText('分 P'), '1'); await user.selectOptions(screen.getByLabelText('模型配置档'), 'transcribe'); await user.click(screen.getByRole('button', { name: '提交任务' }))
    await user.click(screen.getByRole('tab', { name: '文本总结' })); await user.selectOptions(screen.getByLabelText('来源转写（可选）'), 'job-done'); await user.selectOptions(screen.getByLabelText('模型配置档'), 'summary'); await user.selectOptions(screen.getByLabelText('提示词模板'), 'prompt'); await user.click(screen.getByRole('button', { name: '提交任务' }))
    expect(await screen.findByText('00:01')).toBeInTheDocument()
    await user.click(screen.getByText('文本总结', { selector: 'strong' })); await user.click(await screen.findByRole('button', { name: '重新执行' }))
    await user.click(screen.getAllByRole('link', { name: /^AI 设置$/ })[0]); expect(await screen.findByRole('heading', { name: 'AI 设置' })).toBeInTheDocument()
    await user.selectOptions(screen.getByLabelText('用途'), 'text'); await user.type(screen.getAllByLabelText('名称')[0], 'new'); await user.type(screen.getByLabelText('API Key'), 'key'); await user.clear(screen.getByLabelText('Base URL')); await user.type(screen.getByLabelText('Base URL'), 'https://example.com/v1'); await user.clear(screen.getByLabelText('模型')); await user.type(screen.getByLabelText('模型'), 'model'); await user.clear(screen.getByLabelText('超时（秒）')); await user.type(screen.getByLabelText('超时（秒）'), '60'); await user.clear(screen.getByLabelText('温度')); await user.type(screen.getByLabelText('温度'), '1'); await user.clear(screen.getByLabelText('最大输出 Token')); await user.type(screen.getByLabelText('最大输出 Token'), '100'); await user.clear(screen.getByLabelText('单段上下文字符数')); await user.type(screen.getByLabelText('单段上下文字符数'), '1000'); await user.click(screen.getByText('设为该用途的默认配置'))
    await user.click(screen.getAllByRole('button', { name: '编辑' })[0]); await user.click(screen.getAllByRole('button', { name: '检测模型连通性' })[0]); await user.click(screen.getAllByRole('button', { name: '编辑' })[2]); await user.type(screen.getByLabelText('System Prompt'), ' updated'); await user.click(screen.getAllByRole('button', { name: '取消编辑' })[1])
    await user.click(screen.getAllByRole('link', { name: /^设置$/ })[0]); expect(await screen.findByRole('heading', { name: '设置' })).toBeInTheDocument()
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
const aiProfiles = [
  { id: 'transcribe', name: '转写', kind: 'transcription', base_url: 'https://example.com/v1', model: 'gpt-transcribe', language: 'zh', timeout_sec: 600, enabled: true, default: true, configured_secrets: ['api_key'], created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' },
  { id: 'summary', name: '总结', kind: 'text', base_url: 'https://example.com/v1', model: 'gpt-5-mini', temperature: 0.2, max_output_tokens: 4096, context_window_chars: 100000, timeout_sec: 600, enabled: true, default: true, configured_secrets: ['api_key'], created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' },
]
const aiPrompts = [{ id: 'prompt', name: '默认', system_prompt: 'system', chunk_prompt: '{{text}}', reduce_prompt: '{{summaries}}', default: true, created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' }]
const aiJobs = [
  { id: 'job-done', kind: 'transcription', state: 'succeeded', stage: 'completed', progress: 100, profile_id: 'transcribe', attempts: 1, created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' },
  { id: 'job-failed', kind: 'summary', state: 'failed', stage: 'failed', progress: 10, profile_id: 'summary', prompt_id: 'prompt', attempts: 1, error_code: 'provider_failure', last_error: 'failed', created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' },
]
const aiJobDetail = { ...aiJobs[0], result: { bvid: 'BV1xx411c7mD', title: '视频', pages: [{ page: 1, title: 'P1', duration_ms: 3000, segments: [{ start_ms: 1000, end_ms: 2000, text: '内容' }] }] } }
function json(value: unknown, status = 200) { return new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } }) }
function requestPath(input: string | URL | Request) { return typeof input === 'string' ? input : input instanceof URL ? input.href : input.url }
