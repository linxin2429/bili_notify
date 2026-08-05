import { beforeEach, describe, expect, it, vi } from 'vitest'
import { AdminAPI, httpJSON } from './api'

const fetchMock = vi.fn<typeof fetch>()

beforeEach(() => {
  fetchMock.mockReset()
  vi.stubGlobal('fetch', fetchMock)
})

describe('AdminAPI', () => {
  it('sends mutations through resource HTTP endpoints with CSRF', async () => {
    fetchMock.mockResolvedValue(new Response(JSON.stringify({ uid: '42', name: 'test', enabled: true }), {
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

    const request = httpJSON('/api/v1/dashboard')
    const rejection = expect(request).rejects.toThrow('操作超时，结果未知')
    await vi.advanceTimersByTimeAsync(25_000)
    await rejection
    vi.useRealTimers()
  })
})
