import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AdminAPI, httpJSON, parseDynamicHistoryPage } from './api'
import { dashboardSnapshotSchema, emptyResponseSchema } from './contracts'
import { makeChannel, makeSnapshot, makeUP, settings } from './test/fixtures'

const fetchMock = vi.fn<typeof fetch>()

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

describe('AdminAPI', () => {
  it('keeps valid dynamic rows when neighboring archived payloads are malformed', () => {
    const valid = { id: 'valid', uid: '42', up_name: 'UP', type: 'DYNAMIC_TYPE_WORD', published_at: '2026-08-06T00:00:00Z', discovered_at: '2026-08-06T00:00:01Z', baseline: false }
    expect(parseDynamicHistoryPage({ items: [{ id: 42 }, valid, null], total: 3, limit: 20, offset: 0 })).toEqual({ items: [valid], total: 3, limit: 20, offset: 0 })
  })

  it.each([
    { name: 'missing items', input: { total: 0, limit: 20, offset: 0 } },
    { name: 'non-integer page offset', input: { items: [], total: 0, limit: 20, offset: 0.5 } },
  ])('rejects a dynamic history envelope with $name', ({ input }) => {
    expect(() => parseDynamicHistoryPage(input)).toThrow()
  })

  it.each([
    { name: 'dashboard', call: (api: AdminAPI) => api.dashboard(), path: '/api/v1/dashboard', method: undefined, body: makeSnapshot(), status: 200 },
    { name: 'create UP', call: (api: AdminAPI) => api.createUP({ uid: '42', name: 'UP', enabled: true }), path: '/api/v1/ups', method: 'POST', body: makeUP(), status: 200 },
    { name: 'delete UP', call: (api: AdminAPI) => api.deleteUP('42'), path: '/api/v1/ups/42', method: 'DELETE', body: undefined, status: 204 },
    { name: 'create channel', call: (api: AdminAPI) => api.createChannel({ name: 'channel', type: 'wecom', enabled: true, settings: {}, secrets: { webhook: 'secret' } }), path: '/api/v1/channels', method: 'POST', body: makeChannel(), status: 200 },
    { name: 'update channel', call: (api: AdminAPI) => api.updateChannel({ id: 'channel', name: 'channel', type: 'wecom', enabled: true, settings: {} }), path: '/api/v1/channels/channel', method: 'PUT', body: makeChannel(), status: 200 },
    { name: 'delete channel', call: (api: AdminAPI) => api.deleteChannel('channel'), path: '/api/v1/channels/channel', method: 'DELETE', body: undefined, status: 204 },
    { name: 'test channel', call: (api: AdminAPI) => api.testChannel('channel'), path: '/api/v1/channels/channel/test', method: 'POST', body: { status: 'sent' }, status: 200 },
    { name: 'start Bilibili login', call: (api: AdminAPI) => api.startBiliLogin(), path: '/api/v1/bilibili-login', method: 'POST', body: { id: 'login', status: 'waiting', expires_at: 'later' }, status: 200 },
    { name: 'cancel Bilibili login', call: (api: AdminAPI) => api.cancelBiliLogin('login'), path: '/api/v1/bilibili-login/login', method: 'DELETE', body: undefined, status: 204 },
    { name: 'start Microsoft login', call: (api: AdminAPI) => api.startMicrosoftLogin('channel'), path: '/api/v1/channels/channel/microsoft-login', method: 'POST', body: { channel_id: 'channel', status: 'pending' }, status: 200 },
    { name: 'cancel Microsoft login', call: (api: AdminAPI) => api.cancelMicrosoftLogin('channel'), path: '/api/v1/channels/channel/microsoft-login', method: 'DELETE', body: undefined, status: 204 },
    { name: 'update settings', call: (api: AdminAPI) => api.updateSettings(settings), path: '/api/v1/settings', method: 'PUT', body: settings, status: 200 },
    { name: 'query comments', call: (api: AdminAPI) => api.queryComments({ uid: '42' }), path: '/api/v1/comments?uid=42', method: undefined, body: { items: [], total: 0, limit: 20, offset: 0 }, status: 200 },
    { name: 'get comment', call: (api: AdminAPI) => api.getComment('comment/id'), path: '/api/v1/comments/comment%2Fid', method: undefined, body: { rpid: '1', up_uid: '42', up_name: 'UP', content_type: 'video', content_id: 'BV', content_url: 'url', published_at: 'now', thread: [] }, status: 200 },
  ])('calls the $name endpoint', async ({ call, path, method, body, status }) => {
    fetchMock.mockResolvedValue(new Response(status === 204 ? null : JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }))
    await call(new AdminAPI('csrf-token'))
    expect(fetchMock.mock.calls[0][0]).toBe(path)
    expect(fetchMock.mock.calls[0][1]?.method).toBe(method)
  })

  it('sends mutations through resource HTTP endpoints with CSRF', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({
      uid: '42', name: 'test', enabled: true, baseline_ready: false, consecutive_fail: 0,
      follow_state: 'unknown', collection_route: 'space',
    }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    await new AdminAPI('csrf-token').updateUP({ uid: '42', name: 'test', enabled: true })

    expect(fetchMock).toHaveBeenCalledOnce()
    const [path, options] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/v1/ups/42')
    expect(options?.method).toBe('PUT')
    expect(new Headers(options?.headers).get('X-CSRF-Token')).toBe('csrf-token')
    expect(JSON.parse(String(options?.body))).toEqual({ name: 'test', enabled: true })
  })

  it('encodes history filters as query parameters', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ items: [], total: 0, limit: 20, offset: 0 }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    await new AdminAPI('csrf-token').queryDynamics({ uid: '42', q: '测试 内容', limit: 20 })

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/dynamics?uid=42&q=%E6%B5%8B%E8%AF%95+%E5%86%85%E5%AE%B9&limit=20')
    expect(fetchMock.mock.calls[0][1]?.method).toBeUndefined()
  })

  it('encodes operation log filters as query parameters', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ items: [], total: 0, limit: 20, offset: 0 }), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }))

    await new AdminAPI('csrf-token').queryAuditLogs({ action: 'channel.update', outcome: 'failure', q: 'request 42', limit: 20 })

    expect(fetchMock.mock.calls[0][0]).toBe('/api/v1/audit-logs?action=channel.update&outcome=failure&q=request+42&limit=20')
    expect(fetchMock.mock.calls[0][1]?.method).toBeUndefined()
  })

  it('queues one encoded delivery id for retry with CSRF', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ status: 'queued' }), {
      status: 202,
      headers: { 'Content-Type': 'application/json' },
    }))

    await new AdminAPI('csrf-token').retryDelivery('delivery/测试')

    const [path, options] = fetchMock.mock.calls[0]
    expect(path).toBe('/api/v1/deliveries/delivery%2F%E6%B5%8B%E8%AF%95/retry')
    expect(options?.method).toBe('POST')
    expect(new Headers(options?.headers).get('X-CSRF-Token')).toBe('csrf-token')
    expect(options?.body).toBeUndefined()
  })

  it('surfaces the API error message', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ error: { code: 'conflict', message: 'UP already exists' } }), {
      status: 409,
      headers: { 'Content-Type': 'application/json' },
    }))

    await expect(new AdminAPI('csrf-token').createUP({ uid: '42', name: '', enabled: true })).rejects.toThrow('UP already exists')
  })

  it('aborts a request after the operation timeout', async () => {
    vi.useFakeTimers()
    fetchMock.mockImplementation((_input, options) => new Promise((_resolve, reject) => {
      options?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')), { once: true })
    }))

    const request = httpJSON('/api/v1/dashboard', dashboardSnapshotSchema)
    const rejection = expect(request).rejects.toThrow('操作超时，结果未知')
    await vi.advanceTimersByTimeAsync(25_000)
    await rejection
    vi.useRealTimers()
  })

  it('returns undefined for an empty response and sends JSON headers', async () => {
    fetchMock.mockResolvedValue(new Response(null, { status: 204 }))
    await expect(httpJSON('/empty', emptyResponseSchema, { method: 'DELETE', body: '{}' }, 'csrf')).resolves.toBeUndefined()
    const options = fetchMock.mock.calls[0][1]
    expect(new Headers(options?.headers).get('Content-Type')).toBe('application/json')
    expect(new Headers(options?.headers).get('X-CSRF-Token')).toBe('csrf')
  })

  it('forwards a caller abort without reporting a timeout', async () => {
    const caller = new AbortController()
    fetchMock.mockImplementation((_input, options) => new Promise((_resolve, reject) => options?.signal?.addEventListener('abort', () => reject(new DOMException('caller aborted', 'AbortError')), { once: true })))
    const request = httpJSON('/api/v1/dashboard', dashboardSnapshotSchema, { signal: caller.signal })
    caller.abort('cancelled')
    await expect(request).rejects.toMatchObject({ name: 'AbortError' })
  })

  it('rejects a response that violates its schema', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ status: 'not-a-dashboard' }), { status: 200 }))
    await expect(new AdminAPI('csrf').dashboard()).rejects.toThrow()
  })
})
