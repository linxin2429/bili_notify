import { render, screen } from '@testing-library/react'
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
})
