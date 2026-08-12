import { afterEach, describe, expect, it, vi } from 'vitest'
import { resources } from './resources'
import { settings, makeAudit, makeDelivery } from '../../test/fixtures'

const runtime = { status: { auth_valid: true, up_count: 1, channel_count: 1, outbox_depth: 0, ready: true }, timezone: 'Asia/Shanghai', updated_at: '2026-08-09T10:00:00Z' }
const account = { platform: 'bilibili' as const, external_id: '42', display_name: 'UP', status: 'connected' as const }
const group = { id: '9', name: '星球', owner_id: '8', owner_name: '星主' }
const source = { id: 'bilibili:up:42', platform: 'bilibili' as const, type: 'up' as const, external_id: '42', name: 'UP', enabled: true, baseline_state: 'complete' as const, backfill_done: 0, backfill_total: 0, sync_lag_sec: 0, consecutive_fails: 0 }
const content = { id: 'bilibili:content:d', platform: 'bilibili' as const, source_id: source.id, external_id: 'd', upstream_type: 'DYNAMIC_TYPE_WORD', type: 'dynamic' as const, published_at: '2026-08-09T10:00:00Z', first_seen_at: '2026-08-09T10:00:01Z', last_synced_at: '2026-08-09T10:00:01Z', baseline: false }
const channel = { id: 'mail', name: '邮件', type: 'email' as const, enabled: true, settings: {}, configured_secrets: [], created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' }
const page = <T,>(items: T[]) => ({ items, page: { next_cursor: '', has_more: false } })
const contentDetail = { content, attachments: [] }
const commentTree = { children: [], incomplete: false }

describe('resource transport', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('builds every read endpoint from typed resource parameters', async () => {
    const responses = new Map<string, unknown>([
      ['/api/v4/runtime', runtime], ['/api/v4/settings', settings], ['/api/v4/accounts', [account]], ['/api/v4/accounts/zsxq/groups', [group]], ['/api/v4/sources?platform=bilibili', [source]],
      ['/api/v4/contents?platform=bilibili&limit=20', page([content])], ['/api/v4/contents/bilibili%3Acontent%3Ad', contentDetail],
      ['/api/v4/contents/bilibili%3Acontent%3Ad/comments', commentTree], ['/api/v4/channels', [channel]],
      ['/api/v4/deliveries?limit=20&after=cursor', page([makeDelivery()])], ['/api/v4/accounts/bilibili/qr', null], ['/api/v4/microsoft-logins', []],
      ['/api/v4/audit-logs?action=up.create', page([makeAudit({ action: 'up.create' })])],
    ])
    const fetch = vi.fn(async (input: string | URL | Request) => json(responses.get(requestPath(input))))
    vi.stubGlobal('fetch', fetch)

    const signal = new AbortController().signal
    await expect(resources.runtime(signal)).resolves.toEqual(runtime)
    await expect(resources.settings(signal)).resolves.toEqual(settings)
    await expect(resources.accounts(signal)).resolves.toEqual([account])
    await expect(resources.zsxqGroups(signal)).resolves.toEqual([group])
    await expect(resources.sources('bilibili', signal)).resolves.toEqual([source])
    await expect(resources.contents({ platform: 'bilibili', limit: 20 }, signal)).resolves.toEqual(page([content]))
    await expect(resources.content(content.id, signal)).resolves.toEqual(contentDetail)
    await expect(resources.contentComments(content.id, signal)).resolves.toEqual(commentTree)
    await expect(resources.channels(signal)).resolves.toEqual([channel])
    await expect(resources.deliveries('cursor', signal)).resolves.toEqual(page([makeDelivery()]))
    await expect(resources.biliLogin(signal)).resolves.toBeNull()
    await expect(resources.microsoftLogins(signal)).resolves.toEqual([])
    await expect(resources.auditLogs({ action: 'up.create' }, signal)).resolves.toEqual(page([makeAudit({ action: 'up.create' })]))
    expect(fetch).toHaveBeenCalledTimes(13)
  })

  it('sends mutations with method, CSRF token, escaped ids and exact JSON bodies', async () => {
    const fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const path = requestPath(input)
      if (init?.method === 'DELETE' && !path.endsWith('/retry')) return new Response(null, { status: 204 })
      if (path.endsWith('/test')) return json({ status: 'sent' })
      if (path.endsWith('/retry')) return json({ status: 'queued' })
      if (path.endsWith('/accounts/bilibili/qr')) return json({ id: 'login', status: 'waiting', expires_at: '2026-08-09T10:05:00Z' })
      if (path.endsWith('/microsoft-login')) return json({ channel_id: 'c/1', status: 'pending' })
      if (path.endsWith('/settings')) return json(settings)
      if (path.includes('/channels')) return json(channel)
      if (path.includes('/sources')) return json(source)
      return json(channel)
    })
    vi.stubGlobal('fetch', fetch)

    await resources.createBilibiliSource('csrf', { uid: '42', name: 'UP', note: '', enabled: true })
    await resources.createZSXQSource('csrf', { group_id: '9', note: '', enabled: true, zsxq_topic_mode: 'all', zsxq_authors: [] })
    await resources.updateSource('csrf', { id: 'bilibili:up:4/2', platform: 'bilibili', name: '改名', note: '备注', enabled: false })
    await resources.deleteSource('csrf', 'bilibili:up:4/2')
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

    expect(fetch).toHaveBeenCalledTimes(14)
    for (const [, init] of fetch.mock.calls) expect(new Headers(init?.headers).get('X-CSRF-Token')).toBe('csrf')
    expect(fetch.mock.calls.map(([path]) => requestPath(path))).toEqual(expect.arrayContaining([
      '/api/v4/sources/bilibili%3Aup%3A4%2F2', '/api/v4/channels/c%2F1', '/api/v4/deliveries/d%2F1/retry', '/api/v4/accounts/bilibili/qr/login%2F1',
    ]))
    expect(fetch).toHaveBeenCalledWith('/api/v4/sources/bilibili', expect.objectContaining({ method: 'POST', body: JSON.stringify({ uid: '42', name: 'UP', note: '', enabled: true }) }))
    expect(fetch).toHaveBeenCalledWith('/api/v4/sources/zsxq', expect.objectContaining({ method: 'POST', body: JSON.stringify({ group_id: '9', note: '', enabled: true, zsxq_topic_mode: 'all', zsxq_authors: [] }) }))
    expect(fetch).toHaveBeenCalledWith('/api/v4/sources/bilibili%3Aup%3A4%2F2', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ name: '改名', note: '备注', enabled: false }) }))
    expect(fetch.mock.calls[5]?.[1]).toMatchObject({
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

    expect(fetch).toHaveBeenCalledWith('/api/v4/ai/profiles/profile%2F1', expect.objectContaining({
      method: 'PUT',
      body: JSON.stringify({
        name: '转写', kind: 'transcription', base_url: 'https://example.com/v1', model: 'gpt-transcribe',
        api_key: '', language: 'zh', timeout_sec: 600, enabled: true, default: true,
      }),
    }))
    expect(fetch).toHaveBeenCalledWith('/api/v4/ai/profiles/profile%2F1/availability', expect.objectContaining({ method: 'PUT', body: JSON.stringify({ enabled: false }) }))
    expect(fetch).toHaveBeenCalledWith('/api/v4/ai/profiles/profile%2F1/test', expect.objectContaining({ method: 'POST' }))
  })
})

function json(value: unknown) {
  return new Response(JSON.stringify(value), { status: 200, headers: { 'Content-Type': 'application/json' } })
}

function requestPath(input: string | URL | Request) { return typeof input === 'string' ? input : input instanceof URL ? input.href : input.url }
