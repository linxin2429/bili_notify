import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { RealtimeSync, parseEnvelope, useConnectionState } from './RealtimeSync'

class FakeWebSocket {
  static latest: FakeWebSocket
  onopen: (() => void) | null = null; onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null; onclose: (() => void) | null = null
  constructor(readonly url: string) { FakeWebSocket.latest = this }
  close() { /* test controls closure explicitly */ }
}

describe('RealtimeSync', () => {
  afterEach(() => vi.unstubAllGlobals())

  it.each([
    { name: 'valid invalidation', input: { event: 'resources.invalidated', revision: 2, topics: ['runtime'] }, valid: true },
    { name: 'unknown topic', input: { event: 'resources.invalidated', revision: 2, topics: ['unknown'] }, valid: false },
    { name: 'negative revision', input: { event: 'sync.required', revision: -1, topics: [] }, valid: false },
    { name: 'legacy data payload', input: { event: 'resources.invalidated', revision: 2, topics: [], data: {} }, valid: false },
  ])('validates $name messages', ({ input, valid }) => expect(Boolean(parseEnvelope(input))).toBe(valid))

  it('keeps the application readable in REST polling mode when websocket closes', async () => {
    vi.stubGlobal('WebSocket', FakeWebSocket)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ setup_required: false, authenticated: true, csrf_token: 'csrf' }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    render(<QueryClientProvider client={client}><RealtimeSync onAuthenticationLost={vi.fn()} onProtocolError={vi.fn()}><ConnectionProbe /></RealtimeSync></QueryClientProvider>)
    expect(FakeWebSocket.latest.url).toContain('/api/v2/ws')
    FakeWebSocket.latest.onclose?.()
    expect(await screen.findByText('polling')).toBeInTheDocument()
  })
})

function ConnectionProbe() { return <span>{useConnectionState()}</span> }
