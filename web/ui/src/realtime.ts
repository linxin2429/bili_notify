import { z } from 'zod'
import type { DashboardSnapshot } from './types'

const statusSchema = z.object({
  auth_valid: z.boolean(),
  last_success_at: z.string().optional(),
  up_count: z.number(),
  channel_count: z.number(),
  outbox_depth: z.number(),
  oldest_delivery: z.string().optional(),
  ready: z.boolean(),
  risk_paused_until: z.string().optional(),
})

const upSchema = z.object({
  uid: z.string(), name: z.string(), enabled: z.boolean(), baseline_ready: z.boolean(),
  last_poll_at: z.string().optional(), last_success_at: z.string().optional(),
  last_error: z.string().optional(), consecutive_fail: z.number(),
})

const channelSchema = z.object({
  id: z.string(), name: z.string(), type: z.enum(['email', 'microsoft', 'dingtalk', 'feishu', 'wecom']),
  enabled: z.boolean(), settings: z.record(z.string(), z.string()), configured_secrets: z.array(z.string()),
  created_at: z.string(), updated_at: z.string(),
})

const deliverySchema = z.object({
  id: z.string(),
  dynamic: z.object({
    id: z.string(), uid: z.string(), up_name: z.string(), type: z.string(), published_at: z.string(),
    summary: z.string(), url: z.string(),
  }),
  channel_id: z.string(), state: z.enum(['pending', 'blocked']), attempts: z.number(), next_at: z.string(),
  last_error: z.string().optional(), created_at: z.string(),
})

const biliLoginSchema = z.object({
  id: z.string(), status: z.string(), expires_at: z.string(), qr_data_url: z.string().optional(),
}).nullable()

const microsoftLoginSchema = z.object({
  channel_id: z.string(), status: z.string(), user_code: z.string().optional(),
  verification_uri: z.string().optional(), verification_uri_complete: z.string().optional(),
  expires_at: z.string().optional(), error: z.string().optional(),
})

const snapshotSchema = z.object({
  status: statusSchema,
  ups: z.array(upSchema),
  channels: z.array(channelSchema),
  deliveries: z.array(deliverySchema),
  bili_login: biliLoginSchema.optional(),
  microsoft_logins: z.array(microsoftLoginSchema),
  updated_at: z.string(),
})

const envelopeSchema = z.union([
  z.object({ event: z.string(), revision: z.number(), data: z.unknown() }),
  z.object({ id: z.string(), ok: z.boolean(), data: z.unknown().optional(), error: z.object({ code: z.string(), message: z.string() }).optional() }),
])

export interface RealtimeCallbacks {
  onSnapshot: (snapshot: DashboardSnapshot) => void
  onEvent: (event: string, data: unknown, revision: number) => void
  onState: (state: 'connecting' | 'live' | 'reconnecting' | 'stale') => void
  onAuthLost: () => void
  onError: (message: string) => void
}

export function parseEvent(event: string, data: unknown): unknown {
  switch (event) {
    case 'snapshot': return snapshotSchema.parse(data)
    case 'status.updated': return statusSchema.parse(data)
    case 'ups.updated': return z.array(upSchema).parse(data)
    case 'channels.updated': return z.array(channelSchema).parse(data)
    case 'deliveries.updated': return z.array(deliverySchema).parse(data)
    case 'bilibili.login.updated': return biliLoginSchema.parse(data)
    case 'microsoft.login.updated': return z.array(microsoftLoginSchema).parse(data)
    default: throw new Error(`未知服务器事件：${event}`)
  }
}

export class RealtimeClient {
  private socket?: WebSocket
  private stopped = false
  private retry = 0
  private revision = 0
  private pending = new Map<string, { resolve: (value: unknown) => void; reject: (error: Error) => void; timer: number }>()

  constructor(private readonly callbacks: RealtimeCallbacks) {}

  start() {
    this.stopped = false
    this.connect()
  }

  stop() {
    this.stopped = true
    this.socket?.close(1000, 'client closed')
    this.socket = undefined
    this.rejectPending('连接已关闭，操作结果未知')
  }

  command<T = unknown>(action: string, payload: unknown = {}): Promise<T> {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) {
      return Promise.reject(new Error('实时连接不可用，请等待重新连接'))
    }
    const id = crypto.randomUUID()
    return new Promise<T>((resolve, reject) => {
      const timer = window.setTimeout(() => {
        this.pending.delete(id)
        reject(new Error('操作超时，结果未知，请检查最新状态'))
      }, 25000)
      this.pending.set(id, { resolve: value => resolve(value as T), reject, timer })
      this.socket?.send(JSON.stringify({ id, action, payload }))
    })
  }

  private connect() {
    if (this.stopped) return
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
      this.rejectPending('实时连接已断开，操作结果未知，请检查最新状态')
      this.callbacks.onState('stale')
      void this.verifySessionAndReconnect()
    }
  }

  private receive(raw: string) {
    try {
      const envelope = envelopeSchema.parse(JSON.parse(raw))
      if ('event' in envelope) {
        if (envelope.revision < this.revision) return
        this.revision = envelope.revision
        const data = parseEvent(envelope.event, envelope.data)
        if (envelope.event === 'snapshot') this.callbacks.onSnapshot(data as DashboardSnapshot)
        else this.callbacks.onEvent(envelope.event, data, envelope.revision)
        return
      }
      const request = this.pending.get(envelope.id)
      if (!request) return
      window.clearTimeout(request.timer)
      this.pending.delete(envelope.id)
      if (envelope.ok) request.resolve(envelope.data)
      else request.reject(new Error(envelope.error?.message || '操作失败'))
    } catch (error) {
      this.callbacks.onError(error instanceof Error ? error.message : '无法解析服务器消息')
    }
  }

  private async verifySessionAndReconnect() {
    try {
      const response = await fetch('/api/v1/session', { credentials: 'same-origin' })
      const state = await response.json() as { authenticated?: boolean }
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

  private rejectPending(message: string) {
    for (const request of this.pending.values()) {
      window.clearTimeout(request.timer)
      request.reject(new Error(message))
    }
    this.pending.clear()
  }
}

export const schemasForTest = { snapshotSchema, statusSchema }
