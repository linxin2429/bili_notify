import type { DashboardSnapshot } from './types'
import { dashboardSnapshotSchema, parseWebsocketEvent, sessionStateSchema, websocketEnvelopeSchema } from './contracts'

export function nextRevision(current: number, event: string, incoming: number): number | null {
  return event === 'snapshot' || incoming >= current ? incoming : null
}

export interface RealtimeCallbacks {
  onSnapshot: (snapshot: DashboardSnapshot) => void
  onEvent: (event: string, data: unknown, revision: number) => void
  onState: (state: 'connecting' | 'live' | 'reconnecting' | 'stale') => void
  onAuthLost: () => void
  onError: (message: string) => void
}

export function parseEvent(event: string, data: unknown): unknown {
  return parseWebsocketEvent(event, data)
}

export class RealtimeClient {
  private socket?: WebSocket
  private stopped = false
  private retry = 0
  private revision = 0
  constructor(private readonly callbacks: RealtimeCallbacks) {}

  start() {
    this.stopped = false
    this.connect()
  }

  stop() {
    this.stopped = true
    this.socket?.close(1000, 'client closed')
    this.socket = undefined
  }

  private connect() {
    if (this.stopped) return
    this.revision = 0
    this.callbacks.onState(this.retry === 0 ? 'connecting' : 'reconnecting')
    const protocol = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const socket = new WebSocket(`${protocol}//${location.host}/api/v1/ws`)
    this.socket = socket
    socket.onopen = () => {
      this.retry = 0
      this.callbacks.onState('live')
    }
    socket.onmessage = event => this.receive(event.data)
    socket.onerror = () => this.callbacks.onState('stale')
    socket.onclose = () => {
      if (this.stopped) return
      this.callbacks.onState('stale')
      void this.verifySessionAndReconnect()
    }
  }

  private receive(raw: string) {
    try {
      const envelope = websocketEnvelopeSchema.parse(JSON.parse(raw))
      const revision = nextRevision(this.revision, envelope.event, envelope.revision)
      if (revision === null) return
      this.revision = revision
      if (envelope.event === 'snapshot') this.callbacks.onSnapshot(dashboardSnapshotSchema.parse(envelope.data))
      else this.callbacks.onEvent(envelope.event, parseEvent(envelope.event, envelope.data), envelope.revision)
    } catch (error) {
      this.callbacks.onError(error instanceof Error ? error.message : '无法解析服务器消息')
    }
  }

  private async verifySessionAndReconnect() {
    try {
      const response = await fetch('/api/v1/session', { credentials: 'same-origin' })
      const state = sessionStateSchema.parse(await response.json())
      if (!state.authenticated) {
        this.callbacks.onAuthLost()
        return
      }
    } catch {
      // The server may still be restarting; keep the last known state visible.
    }
    const delay = Math.min(30000, 1000 * 2 ** this.retry++)
    if (!this.stopped) window.setTimeout(() => this.connect(), delay)
  }
}
