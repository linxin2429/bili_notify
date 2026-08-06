import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { nextRevision, parseEvent, RealtimeClient, type RealtimeCallbacks } from './realtime'
import { makeSnapshot } from './test/fixtures'

class FakeWebSocket {
  static instances: FakeWebSocket[] = []
  onopen: (() => void) | null = null
  onmessage: ((event: { data: string }) => void) | null = null
  onerror: (() => void) | null = null
  onclose: (() => void) | null = null
  close = vi.fn()

  constructor(readonly url: string) { FakeWebSocket.instances.push(this) }
  open() { this.onopen?.() }
  message(value: unknown) { this.onmessage?.({ data: JSON.stringify(value) }) }
  raw(value: string) { this.onmessage?.({ data: value }) }
  error() { this.onerror?.() }
  disconnect() { this.onclose?.() }
}

function callbacks(): RealtimeCallbacks {
  return { onSnapshot: vi.fn(), onEvent: vi.fn(), onState: vi.fn(), onAuthLost: vi.fn(), onError: vi.fn() }
}

beforeEach(() => {
  FakeWebSocket.instances = []
  vi.stubGlobal('WebSocket', FakeWebSocket)
})

afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('realtime revision handling', () => {
  it.each([
    { current: 10, event: 'snapshot', incoming: 0, want: 0 },
    { current: 10, event: 'status.updated', incoming: 10, want: 10 },
    { current: 10, event: 'status.updated', incoming: 9, want: null },
  ])('$event $incoming after $current', ({ current, event, incoming, want }) => expect(nextRevision(current, event, incoming)).toBe(want))

  it('rejects unknown and malformed event payloads', () => {
    expect(() => parseEvent('unknown', {})).toThrow('未知服务器事件')
    expect(() => parseEvent('ups.updated', [{ uid: 1 }])).toThrow()
  })
})

describe('RealtimeClient', () => {
  it('connects, publishes state, and dispatches snapshots and ordered updates', () => {
    const handlers = callbacks(); const client = new RealtimeClient(handlers)
    client.start()
    const socket = FakeWebSocket.instances[0]
    expect(socket.url).toMatch(/^ws:\/\//)
    expect(handlers.onState).toHaveBeenCalledWith('connecting')
    socket.open()
    expect(handlers.onState).toHaveBeenLastCalledWith('live')
    socket.message({ event: 'snapshot', revision: 5, data: makeSnapshot() })
    socket.message({ event: 'status.updated', revision: 6, data: { ...makeSnapshot().status, ready: false } })
    socket.message({ event: 'status.updated', revision: 4, data: makeSnapshot().status })
    expect(handlers.onSnapshot).toHaveBeenCalledOnce()
    expect(handlers.onEvent).toHaveBeenCalledOnce()
    expect(handlers.onEvent).toHaveBeenCalledWith('status.updated', expect.objectContaining({ ready: false }), 6)
  })

  it('reports socket and parsing errors', () => {
    const handlers = callbacks(); new RealtimeClient(handlers).start()
    const socket = FakeWebSocket.instances[0]
    socket.error(); socket.raw('{broken'); socket.message({ event: 'future.event', revision: 1, data: {} })
    expect(handlers.onState).toHaveBeenCalledWith('stale')
    expect(handlers.onError).toHaveBeenCalledTimes(2)
  })

  it('stops and closes the active socket without reconnecting', async () => {
    vi.stubGlobal('fetch', vi.fn())
    const handlers = callbacks(); const client = new RealtimeClient(handlers)
    client.start(); const socket = FakeWebSocket.instances[0]
    client.stop(); socket.disconnect()
    expect(socket.close).toHaveBeenCalledWith(1000, 'client closed')
    expect(fetch).not.toHaveBeenCalled()
  })

  it('reports lost authentication without reconnecting', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ setup_required: false, authenticated: false }))))
    const handlers = callbacks(); new RealtimeClient(handlers).start()
    FakeWebSocket.instances[0].disconnect()
    await vi.waitFor(() => expect(handlers.onAuthLost).toHaveBeenCalledOnce())
    expect(fetch).toHaveBeenCalledWith('/api/v1/session', { credentials: 'same-origin' })
    expect(FakeWebSocket.instances).toHaveLength(1)
  })

  it.each([
    { name: 'authenticated session', response: () => Promise.resolve(new Response(JSON.stringify({ setup_required: false, authenticated: true }))) },
    { name: 'temporary session failure', response: () => Promise.reject(new Error('restart')) },
  ])('reconnects after $name', async ({ response }) => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', vi.fn().mockImplementation(response))
    const handlers = callbacks(); new RealtimeClient(handlers).start()
    FakeWebSocket.instances[0].disconnect()
    await vi.advanceTimersByTimeAsync(1_000)
    expect(FakeWebSocket.instances).toHaveLength(2)
    expect(handlers.onState).toHaveBeenCalledWith('reconnecting')
  })
})
