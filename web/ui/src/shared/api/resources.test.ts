import { afterEach, describe, expect, it, vi } from 'vitest'
import { resources } from './resources'
import { settings, makeAudit, makeDelivery } from '../../test/fixtures'

const runtime = { status: { auth_valid: true, up_count: 1, channel_count: 1, outbox_depth: 0, ready: true }, timezone: 'Asia/Shanghai', updated_at: '2026-08-09T10:00:00Z' }
const up = { uid: '42', name: 'UP', enabled: true, baseline_ready: true, consecutive_fail: 0, follow_state: 'followed' as const, collection_route: 'feed_all' as const }
const channel = { id: 'mail', name: '邮件', type: 'email' as const, enabled: true, settings: {}, configured_secrets: [], created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' }
const page = <T,>(items: T[]) => ({ items, page: { next_cursor: '', has_more: false } })
const dynamic = { id: 'd', uid: '42', up_name: 'UP', type: 'DYNAMIC_TYPE_WORD', published_at: '2026-08-09T10:00:00Z', discovered_at: '2026-08-09T10:00:01Z', baseline: false, summary: '', url: '' }
const comment = { rpid: 'r', up_uid: '42', up_name: 'UP', published_at: '2026-08-09T10:00:00Z', discovered_at: '2026-08-09T10:00:01Z', baseline: false }
const detail = { rpid: 'r', up_uid: '42', up_name: 'UP', content_type: 'dynamic', content_id: 'd', content_url: '', published_at: '2026-08-09T10:00:00Z', thread: [] }

describe('resource transport', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('builds every read endpoint from typed resource parameters', async () => {
    const responses = new Map<string, unknown>([
      ['/api/v2/runtime', runtime], ['/api/v2/settings', settings], ['/api/v2/ups', [up]], ['/api/v2/channels', [channel]],
      ['/api/v2/deliveries?limit=20&after=cursor', page([makeDelivery()])], ['/api/v2/bilibili-login', null], ['/api/v2/microsoft-logins', []],
      ['/api/v2/dynamics?uid=42&limit=20', page([dynamic])], ['/api/v2/comments?q=hello', page([comment])], ['/api/v2/comments/r%2F1', detail],
      ['/api/v2/audit-logs?action=up.create', page([makeAudit({ action: 'up.create' })])],
    ])
    const fetch = vi.fn(async (input: string | URL | Request) => json(responses.get(requestPath(input))))
    vi.stubGlobal('fetch', fetch)

    const signal = new AbortController().signal
    await expect(resources.runtime(signal)).resolves.toEqual(runtime)
    await expect(resources.settings(signal)).resolves.toEqual(settings)
    await expect(resources.ups(signal)).resolves.toEqual([up])
    await expect(resources.channels(signal)).resolves.toEqual([channel])
    await expect(resources.deliveries('cursor', signal)).resolves.toEqual(page([makeDelivery()]))
    await expect(resources.biliLogin(signal)).resolves.toBeNull()
    await expect(resources.microsoftLogins(signal)).resolves.toEqual([])
    await expect(resources.dynamics({ uid: '42', limit: 20 }, signal)).resolves.toEqual(page([dynamic]))
    await expect(resources.comments({ q: 'hello' }, signal)).resolves.toEqual(page([comment]))
    await expect(resources.comment('r/1', signal)).resolves.toEqual(detail)
    await expect(resources.auditLogs({ action: 'up.create' }, signal)).resolves.toEqual(page([makeAudit({ action: 'up.create' })]))
    expect(fetch).toHaveBeenCalledTimes(11)
  })

  it('sends mutations with method, CSRF token, escaped ids and exact JSON bodies', async () => {
    const fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = requestPath(input)
      if (init?.method === 'DELETE' && !path.endsWith('/retry')) return new Response(null, { status: 204 })
      if (path.endsWith('/test')) return json({ status: 'sent' })
      if (path.endsWith('/retry')) return json({ status: 'queued' })
      if (path.endsWith('/bilibili-login')) return json({ id: 'login', status: 'waiting', expires_at: '2026-08-09T10:05:00Z' })
      if (path.endsWith('/microsoft-login')) return json({ channel_id: 'c/1', status: 'pending' })
      if (path.endsWith('/settings')) return json(settings)
      if (path.includes('/channels')) return json(channel)
      return json(up)
    })
    vi.stubGlobal('fetch', fetch)

    await resources.createUP('csrf', { uid: '42', name: 'UP', enabled: true })
    await resources.updateUP('csrf', { uid: '4/2', name: '改名', enabled: false })
    await resources.deleteUP('csrf', '4/2')
    const draft = { name: '邮件', type: 'email' as const, enabled: true, settings: { host: 'smtp', port: '465', tls: 'tls', username: '', from: 'a', to: 'b' } }
    await resources.createChannel('csrf', draft)
    await resources.updateChannel('csrf', { ...draft, id: 'c/1' })
    await resources.deleteChannel('csrf', 'c/1')
    await resources.testChannel('csrf', 'c/1')
    await resources.retryDelivery('csrf', 'd/1')
    await resources.startBiliLogin('csrf')
    await resources.cancelBiliLogin('csrf', 'login/1')
    await resources.startMicrosoftLogin('csrf', 'c/1')
    await resources.cancelMicrosoftLogin('csrf', 'c/1')
    await resources.updateSettings('csrf', settings)

    expect(fetch).toHaveBeenCalledTimes(13)
    for (const [, init] of fetch.mock.calls) expect(new Headers(init?.headers).get('X-CSRF-Token')).toBe('csrf')
    expect(fetch.mock.calls.map(([path]) => requestPath(path))).toEqual(expect.arrayContaining([
      '/api/v2/ups/4%2F2', '/api/v2/channels/c%2F1', '/api/v2/deliveries/d%2F1/retry', '/api/v2/bilibili-login/login%2F1',
    ]))
    expect(fetch.mock.calls[1]?.[1]).toMatchObject({ method: 'PUT', body: JSON.stringify({ name: '改名', enabled: false }) })
    expect(fetch.mock.calls[4]?.[1]).toMatchObject({
      method: 'PUT',
      body: JSON.stringify(draft),
    })
  })

  it('whitelists writable fields in AI profile requests', async () => {
    const profile = {
      id: 'profile/1', name: '转写', kind: 'transcription' as const, base_url: 'https://example.com/v1', model: 'gpt-transcribe',
      api_key: '', language: 'zh', timeout_sec: 600, enabled: true, default: true, configured_secrets: ['api_key'],
      created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T11:00:00Z',
    }
    const responseProfile = {
      id: profile.id, name: profile.name, kind: profile.kind, base_url: profile.base_url, model: profile.model,
      language: profile.language, timeout_sec: profile.timeout_sec, enabled: profile.enabled, default: profile.default,
      configured_secrets: profile.configured_secrets, created_at: profile.created_at, updated_at: profile.updated_at,
    }
    const fetch = vi.fn(async (input: string | URL | Request) => requestPath(input).endsWith('/test') ? json({ ok: true, latency_ms: 12, message: 'ok', provider_http_status: 200 }) : json(responseProfile))
    vi.stubGlobal('fetch', fetch)

    await resources.updateAIProfile('csrf', profile)
    await resources.updateAIProfileAvailability('csrf', profile.id, false)
    await resources.testAIProfile('csrf', profile.id)

    expect(fetch).toHaveBeenCalledWith('/api/v2/ai/profiles/profile%2F1', expect.objectContaining({
      method: 'PUT',
      body: JSON.stringify({
        name: '转写', kind: 'transcription', base_url: 'https://example.com/v1', model: 'gpt-transcribe',
        api_key: '', language: 'zh', timeout_sec: 600, enabled: true, default: true,
      }),
    }))
    expect(fetch).toHaveBeenCalledWith('/api/v2/ai/profiles/profile%2F1/availability', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ enabled: false }) }))
    expect(fetch).toHaveBeenCalledWith('/api/v2/ai/profiles/profile%2F1/test', expect.objectContaining({ method: 'POST' }))
  })
})

function json(value: unknown) {
  return new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function requestPath(input: string | URL | Request) { return typeof input === 'string' ? input : input instanceof URL ? input.href : input.url }
