import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, render, screen, waitFor } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { queryKeys } from '../api/query-keys'
import { duringSessionReplacement } from '../api/session-cache'
import { RealtimeSync, parseEnvelope, useConnectionState } from './RealtimeSync'

class FakeWebSocket {
  static latest: FakeWebSocket
  onopen: (() => void) | null = null; onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null; onclose: (() => void | Promise<void>) | null = null
  close = vi.fn()
  constructor(readonly url: string) { FakeWebSocket.latest = this }
}

describe('RealtimeSync', () => {
  afterEach(() => vi.unstubAllGlobals())

  it.each([
    { name: 'valid invalidation', input: { event: 'resources.invalidated', revision: 2, topics: ['runtime'] }, valid: true },
    { name: 'valid full sync with AI topics', input: { event: 'sync.required', revision: 0, topics: ['ai-status', 'ai-jobs'] }, valid: true },
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
    act(() => { void FakeWebSocket.latest.onclose?.() })
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
    act(() => FakeWebSocket.latest.onmessage?.({ data: JSON.stringify({ event: 'resources.invalidated', revision: 2, topics: ['runtime', 'settings', 'ai-status', 'ai-jobs'] }) }))
    await waitFor(() => expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.runtime }))
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.settings })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: queryKeys.aiStatus })
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['ai-jobs'] })

    invalidate.mockClear()
    act(() => FakeWebSocket.latest.onmessage?.({ data: JSON.stringify({ event: 'sync.required', revision: 1, topics: ['ups'] }) }))
    expect(invalidate).not.toHaveBeenCalled()

    act(() => FakeWebSocket.latest.onerror?.())
    expect(screen.getByText('polling')).toBeInTheDocument()
    act(() => FakeWebSocket.latest.onmessage?.({ data: 'not-json' }))
    expect(onProtocolError).toHaveBeenCalledWith('实时消息不符合 API 契约，已切换为 REST 重新同步')
    expect(FakeWebSocket.latest.close).toHaveBeenCalledWith(4002, 'invalid application message')
  })

  it('reports an expired session and closes its socket on unmount', async () => {
    vi.stubGlobal('WebSocket', FakeWebSocket)
    vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ setup_required: false, authenticated: false, csrf_token: '' }), { status: 200, headers: { 'Content-Type': 'application/json' } })))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const onAuthenticationLost = vi.fn()
    const view = render(<QueryClientProvider client={client}><RealtimeSync onAuthenticationLost={onAuthenticationLost} onProtocolError={vi.fn()}><ConnectionProbe /></RealtimeSync></QueryClientProvider>)

    act(() => { void FakeWebSocket.latest.onclose?.() })
    await waitFor(() => expect(onAuthenticationLost).toHaveBeenCalledOnce())
    expect(client.getQueryData(queryKeys.session)).toMatchObject({ authenticated: false })
    const socket = FakeWebSocket.latest
    view.unmount()
    expect(socket.close).toHaveBeenCalledWith(1000, 'client closed')
  })

  it('discards an anonymous session response started during password replacement', async () => {
    vi.stubGlobal('WebSocket', FakeWebSocket)
    let resolveSession!: (response: Response) => void
    vi.stubGlobal('fetch', vi.fn(() => new Promise<Response>(resolve => { resolveSession = resolve })))
    let finishReplacement!: () => void
    const replacement = duringSessionReplacement(() => new Promise<void>(resolve => { finishReplacement = resolve }))
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const onAuthenticationLost = vi.fn()
    const previousSocket = FakeWebSocket.latest
    render(<QueryClientProvider client={client}><RealtimeSync onAuthenticationLost={onAuthenticationLost} onProtocolError={vi.fn()}><ConnectionProbe /></RealtimeSync></QueryClientProvider>)

    await waitFor(() => expect(FakeWebSocket.latest).not.toBe(previousSocket))
    let closePromise!: Promise<void>
    act(() => { closePromise = Promise.resolve(FakeWebSocket.latest.onclose?.()) })
    await waitFor(() => expect(fetch).toHaveBeenCalledOnce())
    expect(screen.getByText('polling')).toBeInTheDocument()
    finishReplacement()
    await replacement
    await act(async () => {
      resolveSession(new Response(JSON.stringify({ setup_required: false, authenticated: false }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
      await closePromise
    })

    expect(onAuthenticationLost).not.toHaveBeenCalled()
    expect(client.getQueryData(queryKeys.session)).toBeUndefined()
  })
})

function ConnectionProbe() { return <span>{useConnectionState()}</span> }
