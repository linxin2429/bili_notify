import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { queryKeys } from '../api/query-keys'
import { RealtimeSync, parseEnvelope, useConnectionState } from './RealtimeSync'

class FakeWebSocket {
  static latest: FakeWebSocket
  onopen: (() => void) | null = null; onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null; onclose: (() => void) | null = null
  close = vi.fn()
  constructor(readonly url: string) { FakeWebSocket.latest = this }
}

describe('RealtimeSync', () => {
  afterEach(() => vi.unstubAllGlobals())

  it.each([
    { name: 'valid invalidation', input: { event: 'resources.invalidated', revision: 2, topics: ['runtime'] }, valid: true },
    { name: 'valid full sync', input: { event: 'sync.required', revision: 0, topics: [] }, valid: true },
    { name: 'null payload', input: null, valid: false },
    { name: 'primitive payload', input: 'resources.invalidated', valid: false },
    { name: 'unknown event', input: { event: 'changed', revision: 2, topics: [] }, valid: false },
    { name: 'unsafe revision', input: { event: 'sync.required', revision: Number.MAX_SAFE_INTEGER + 1, topics: [] }, valid: false },
    { name: 'unknown topic', input: { event: 'resources.invalidated', revision: 2, topics: ['unknown'] }, valid: false },
    { name: 'non-string topic', input: { event: 'resources.invalidated', revision: 2, topics: [42] }, valid: false },
    { name: 'missing topics', input: { event: 'resources.invalidated', revision: 2 }, valid: false },
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

  it('moves through live, invalidation and protocol-error states', async () => {
    vi.stubGlobal('WebSocket', FakeWebSocket)
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const invalidate = vi.spyOn(client, 'invalidateQueries')
    const onProtocolError = vi.fn()
    render(<QueryClientProvider client={client}><RealtimeSync onAuthenticationLost={vi.fn()} onProtocolError={onProtocolError}><ConnectionProbe /></RealtimeSync></QueryClientProvider>)

    act(() => FakeWebSocket.latest.onopen?.())
    expect(screen.getByText('live')).toBeInTheDocument()
    act(() => FakeWebSocket.latest.onmessage?.({ data: JSON.stringify({ event: 'resources.invalidated', revision: 2, topics: ['runtime', 'settings'] }) }))
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.runtime }))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.settings })

    invalidate.mockClear()
    act(() => FakeWebSocket.latest.onmessage?.({ data: JSON.stringify({ event: 'sync.required', revision: 1, topics: ['ups'] }) }))
    expect(invalidate).not.toHaveBeenCalled()

    act(() => FakeWebSocket.latest.onerror?.())
    expect(screen.getByText('polling')).toBeInTheDocument()
    act(() => FakeWebSocket.latest.onmessage?.({ data: 'not-json' }))
    expect(onProtocolError).toHaveBeenCalledWith('实时消息不符合 API 契约，已切换为 REST 重新同步')
    expect(FakeWebSocket.latest.close).toHaveBeenCalledWith(1002, 'invalid protocol')
  })

  it('reports an expired session and closes its socket on unmount', async () => {
    vi.stubGlobal('WebSocket', FakeWebSocket)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ setup_required: false, authenticated: false, csrf_token: '' }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const onAuthenticationLost = vi.fn()
    const view = render(<QueryClientProvider client={client}><RealtimeSync onAuthenticationLost={onAuthenticationLost} onProtocolError={vi.fn()}><ConnectionProbe /></RealtimeSync></QueryClientProvider>)

    act(() => FakeWebSocket.latest.onclose?.())
    await waitFor(() => expect(onAuthenticationLost).toHaveBeenCalledOnce())
    expect(client.getQueryData(queryKeys.session)).toMatchObject({ authenticated: false })
    const socket = FakeWebSocket.latest
    view.unmount()
    expect(socket.close).toHaveBeenCalledWith(1000, 'client closed')
  })
})

function ConnectionProbe() { return <span>{useConnectionState()}</span> }
