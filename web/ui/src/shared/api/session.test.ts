import { afterEach, describe, expect, it, vi } from 'vitest'
import { sessionAPI } from './session'

describe('session transport', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('accepts the replacement CSRF token returned after changing the password', async () => {
    const fetch = vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      expect(input).toBe('/api/v4/session/password')
      expect(init?.method).toBe('PUT')
      return new Response(JSON.stringify({ csrf_token: 'replacement-csrf' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      })
    })
    vi.stubGlobal('fetch', fetch)

    await expect(sessionAPI.changePassword('old-csrf', 'current-password', 'replacement-password')).resolves.toEqual({ csrf_token: 'replacement-csrf' })
    expect(fetch).toHaveBeenCalledWith('/api/v4/session/password', expect.objectContaining({
      method: 'PUT',
      body: JSON.stringify({ current_password: 'current-password', new_password: 'replacement-password' }),
    }))
    expect(new Headers(fetch.mock.calls[0]?.[1]?.headers).get('X-CSRF-Token')).toBe('old-csrf')
  })
})
