import { afterEach, describe, expect, it, vi } from 'vitest'
import { runtimeQuery } from './runtime-query'

const valid = {
  timezone: 'Asia/Shanghai',
  updated_at: '2026-08-09T10:00:00Z',
  status: { auth_valid: true, up_count: 1, channel_count: 2, outbox_depth: 3, ready: true },
}

describe('runtimeQuery', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('uses the shared runtime key and a short stale window', () => {
    const query = runtimeQuery()
    expect(query.queryKey).toEqual(['runtime'])
    expect(query.staleTime).toBe(10_000)
  })

  it.each([
    { name: 'valid runtime', body: valid, ok: true },
    { name: 'optional status fields', body: { ...valid, status: { ...valid.status, bili_account: { uid: '1', name: 'UP' }, last_success_at: '2026-08-09T10:00:00Z', oldest_delivery: '2026-08-09T09:00:00Z', risk_paused_until: '2026-08-09T11:00:00Z' } }, ok: true },
    { name: 'null payload', body: null, ok: false },
    { name: 'missing timezone', body: { ...valid, timezone: 1 }, ok: false },
    { name: 'missing updated_at', body: { ...valid, updated_at: 1 }, ok: false },
    { name: 'missing status', body: { timezone: valid.timezone, updated_at: valid.updated_at }, ok: false },
    { name: 'invalid account', body: { ...valid, status: { ...valid.status, bili_account: { uid: 1, name: 'UP' } } }, ok: false },
    { name: 'invalid last success', body: { ...valid, status: { ...valid.status, last_success_at: 1 } }, ok: false },
    { name: 'invalid oldest delivery', body: { ...valid, status: { ...valid.status, oldest_delivery: 1 } }, ok: false },
    { name: 'invalid risk pause', body: { ...valid, status: { ...valid.status, risk_paused_until: 1 } }, ok: false },
    { name: 'negative count', body: { ...valid, status: { ...valid.status, outbox_depth: -1 } }, ok: false },
    { name: 'fractional count', body: { ...valid, status: { ...valid.status, up_count: 1.5 } }, ok: false },
    { name: 'non-finite count', body: { ...valid, status: { ...valid.status, channel_count: Number.POSITIVE_INFINITY } }, ok: false },
    { name: 'zero counts', body: { ...valid, status: { ...valid.status, up_count: 0, channel_count: 0, outbox_depth: 0 } }, ok: true },
  ])('parses $name', async ({ body, ok }) => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify(body), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const pending = runtimeQuery().queryFn!({ signal: new AbortController().signal } as never)
    if (ok) await expect(pending).resolves.toEqual(body)
    else await expect(pending).rejects.toMatchObject({ kind: 'contract' })
  })
})
