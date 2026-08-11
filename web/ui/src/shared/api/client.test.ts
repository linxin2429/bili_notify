import { afterEach, describe, expect, it, vi } from 'vitest'
import { z } from 'zod'
import { queryString, requestJSON, setAuthenticationLostHandler } from './client'
import { apiErrorMessage, ApiError, parseErrorBody } from './errors'

describe('requestJSON', () => {
  afterEach(() => { vi.unstubAllGlobals(); setAuthenticationLostHandler(undefined) })

  it.each([
    { name: 'network', response: () => Promise.reject(new TypeError('offline')), kind: 'network', message: 'offline' },
    { name: 'contract', response: () => Promise.resolve(new Response(JSON.stringify({ ok: 'no' }), { status: 200, headers: { 'Content-Type': 'application/json', 'X-Request-ID': 'req-1' } })), kind: 'contract', message: '服务器响应不符合 API 契约' },
  ])('normalizes $name failures', async ({ response, kind, message }) => {
    vi.stubGlobal('fetch', vi.fn(response))
    await expect(requestJSON('/resource', z.object({ ok: z.boolean() }))).rejects.toMatchObject({ kind, message })
  })

  it('invalidates authentication only for admin session 401 responses', async () => {
    const lost = vi.fn(); setAuthenticationLostHandler(lost)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { code: 'session_expired', message: '请重新登录' } }), { status: 401, headers: { 'Content-Type': 'application/json', 'X-Request-ID': 'req-401' } })))
    const error = await requestJSON('/protected', z.object({ ok: z.boolean() })).catch(value => value as ApiError)
    expect(error).toMatchObject({ kind: 'http', status: 401, code: 'session_expired', requestId: 'req-401' })
    expect(lost).toHaveBeenCalledOnce()

    lost.mockClear()
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { code: 'authentication_failed', message: 'Knowledge Planet authentication failed' } }), { status: 401, headers: { 'Content-Type': 'application/json', 'X-Request-ID': 'req-platform' } })))
    const platform = await requestJSON('/api/v3/accounts/zsxq/sms-code', z.object({ ok: z.boolean() })).catch(value => value as ApiError)
    expect(platform).toMatchObject({ kind: 'http', status: 401, code: 'authentication_failed' })
    expect(lost).not.toHaveBeenCalled()
  })

  it('supports empty responses, JSON headers and caller cancellation', async () => {
    const fetch = vi.fn(async (_path: string | URL | Request, init?: RequestInit) => {
      expect(init).toMatchObject({ credentials: 'same-origin' })
      expect(new Headers(init?.headers)).toMatchObject(expect.any(Headers))
      return new Response(null, { status: 204, headers: { 'X-Request-ID': 'empty' } })
    })
    vi.stubGlobal('fetch', fetch)
    const schema = { safeParse: (value: unknown) => ({ success: value === undefined, data: undefined }) }
    await expect(requestJSON('/empty', schema, { method: 'DELETE', csrf: 'csrf', body: '{}' })).resolves.toBeUndefined()
    expect(new Headers(fetch.mock.calls[0]?.[1]?.headers).get('Content-Type')).toBe('application/json')
    expect(new Headers(fetch.mock.calls[0]?.[1]?.headers).get('X-CSRF-Token')).toBe('csrf')

    const controller = new AbortController(); controller.abort('left page')
    vi.stubGlobal('fetch', vi.fn(async (_path, init) => { throw (init?.signal as AbortSignal).reason }))
    await expect(requestJSON('/cancelled', schema, { signal: controller.signal })).rejects.toMatchObject({ kind: 'aborted', message: '请求已取消' })
  })

  it('normalizes text HTTP responses and timeouts', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('proxy unavailable', { status: 502, statusText: 'Bad Gateway' })))
    await expect(requestJSON('/text', z.unknown())).rejects.toMatchObject({ kind: 'http', status: 502, message: 'proxy unavailable', retryable: true })

    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn((_path, init) => new Promise((_resolve, reject) => {
      init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')))
    })))
    const pending = requestJSON('/slow', z.unknown())
    const rejected = expect(pending).rejects.toMatchObject({ kind: 'timeout' })
    await vi.advanceTimersByTimeAsync(25_000)
    await rejected
    vi.useRealTimers()
  })

  it('formats query strings and structured errors without losing safe fields', () => {
    expect(queryString({ q: 'hello world', limit: 20, empty: '', ignored: false })).toBe('?q=hello+world&limit=20')
    expect(queryString({ empty: '' })).toBe('')
    const error = new ApiError('失败', 'http', { status: 422, code: 'invalid', requestId: 'req', fields: { name: '必填' } })
    expect(error).toMatchObject({ status: 422, code: 'invalid', requestId: 'req', fields: { name: '必填' }, retryable: false })
    expect(apiErrorMessage(error)).toBe('失败（请求 req）')
    expect(apiErrorMessage(new Error('plain'))).toBe('plain')
    expect(apiErrorMessage('bad')).toBe('发生未知错误')
    expect(parseErrorBody({ error: { code: 'invalid', message: 'bad', fields: { name: 'required', count: 3 } } })).toEqual({ success: true, data: { error: { code: 'invalid', message: 'bad', fields: { name: 'required' } } } })
    expect(parseErrorBody(null)).toEqual({ success: false })
    expect(parseErrorBody({ error: null })).toEqual({ success: false })
  })
})
