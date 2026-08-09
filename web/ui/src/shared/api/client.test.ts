import { afterEach, describe, expect, it, vi } from 'vitest'
import { z } from 'zod'
import { requestJSON, setAuthenticationLostHandler } from './client'
import { ApiError } from './errors'

describe('requestJSON', () => {
  afterEach(() => { vi.unstubAllGlobals(); setAuthenticationLostHandler(undefined) })

  it.each([
    { name: 'network', response: () => Promise.reject(new TypeError('offline')), kind: 'network', message: 'offline' },
    { name: 'contract', response: () => Promise.resolve(new Response(JSON.stringify({ ok: 'no' }), { status: 200, headers: { 'Content-Type': 'application/json', 'X-Request-ID': 'req-1' } })), kind: 'contract', message: '服务器响应不符合 API 契约' },
  ])('normalizes $name failures', async ({ response, kind, message }) => {
    vi.stubGlobal('fetch', vi.fn(response))
    await expect(requestJSON('/resource', z.object({ ok: z.boolean() }))).rejects.toMatchObject({ kind, message })
  })

  it('invalidates authentication from any 401 response', async () => {
    const lost = vi.fn(); setAuthenticationLostHandler(lost)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: { code: 'session_expired', message: '请重新登录' } }), { status: 401, headers: { 'Content-Type': 'application/json', 'X-Request-ID': 'req-401' } })))
    const error = await requestJSON('/protected', z.object({ ok: z.boolean() })).catch(value => value as ApiError)
    expect(error).toMatchObject({ kind: 'http', status: 401, code: 'session_expired', requestId: 'req-401' })
    expect(lost).toHaveBeenCalledOnce()
  })
})
