import { z } from 'zod'
import type {
  AuditQuery, ChannelDraft, ContentPage, ContentQuery, DynamicHistoryItem, UP,
} from './types'
import {
  auditLogPageSchema, biliLoginSchema, channelSchema, commentDetailSchema, commentHistoryPageSchema,
  dashboardSnapshotSchema, dynamicHistorySchema, emptyResponseSchema, microsoftLoginSchema,
  queuedStatusSchema, runtimeSettingsSchema, sentStatusSchema, upSchema,
} from './contracts'

const requestTimeoutMS = 25_000

const dynamicHistoryEnvelopeSchema = z.object({
  items: z.array(z.unknown()), total: z.number().int(), limit: z.number().int(), offset: z.number().int(),
})

export function parseDynamicHistoryPage(data: unknown): ContentPage<DynamicHistoryItem> {
  const page = dynamicHistoryEnvelopeSchema.parse(data)
  const items: DynamicHistoryItem[] = []
  for (const item of page.items) {
    const parsed = dynamicHistorySchema.safeParse(item)
    if (parsed.success) items.push(parsed.data)
  }
  return { items, total: page.total, limit: page.limit, offset: page.offset }
}

export async function httpJSON<T>(path: string, schema: z.ZodType<T>, options: RequestInit = {}, csrf = ''): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body) headers.set('Content-Type', 'application/json')
  if (csrf) headers.set('X-CSRF-Token', csrf)
  const controller = new AbortController()
  let timedOut = false
  const abortFromCaller = () => controller.abort(options.signal?.reason)
  if (options.signal?.aborted) abortFromCaller()
  else options.signal?.addEventListener('abort', abortFromCaller, { once: true })
  const timeout = window.setTimeout(() => {
    timedOut = true
    controller.abort()
  }, requestTimeoutMS)
  try {
    const response = await fetch(path, { ...options, headers, credentials: 'same-origin', signal: controller.signal })
    if (response.status === 204) return schema.parse(undefined)
    const body = await response.json()
    if (!response.ok) throw new Error(body.error?.message || response.statusText)
    return schema.parse(body)
  } catch (error) {
    if (timedOut) throw new Error('操作超时，结果未知，请刷新状态后重试')
    throw error
  } finally {
    window.clearTimeout(timeout)
    options.signal?.removeEventListener('abort', abortFromCaller)
  }
}

export class AdminAPI {
  constructor(private readonly csrf: string) {}

  dashboard() { return httpJSON('/api/v1/dashboard', dashboardSnapshotSchema) }

  createUP(input: Pick<UP, 'uid' | 'name' | 'enabled'>) {
    return this.write('/api/v1/ups', upSchema, 'POST', input)
  }

  updateUP(input: Pick<UP, 'uid' | 'name' | 'enabled'>) {
    return this.write(`/api/v1/ups/${encodeURIComponent(input.uid)}`, upSchema, 'PUT', { name: input.name, enabled: input.enabled })
  }

  deleteUP(uid: string) { return this.write(`/api/v1/ups/${encodeURIComponent(uid)}`, emptyResponseSchema, 'DELETE') }

  createChannel(input: ChannelDraft) { return this.write('/api/v1/channels', channelSchema, 'POST', input) }

  updateChannel(input: ChannelDraft & { id: string }) {
    return this.write(`/api/v1/channels/${encodeURIComponent(input.id)}`, channelSchema, 'PUT', input)
  }

  deleteChannel(id: string) { return this.write(`/api/v1/channels/${encodeURIComponent(id)}`, emptyResponseSchema, 'DELETE') }

  testChannel(id: string) { return this.write(`/api/v1/channels/${encodeURIComponent(id)}/test`, sentStatusSchema, 'POST') }

  retryDelivery(id: string) { return this.write(`/api/v1/deliveries/${encodeURIComponent(id)}/retry`, queuedStatusSchema, 'POST') }

  startBiliLogin() { return this.write('/api/v1/bilibili-login', biliLoginSchema.unwrap(), 'POST') }

  cancelBiliLogin(id: string) { return this.write(`/api/v1/bilibili-login/${encodeURIComponent(id)}`, emptyResponseSchema, 'DELETE') }

  startMicrosoftLogin(channelID: string) {
    return this.write(`/api/v1/channels/${encodeURIComponent(channelID)}/microsoft-login`, microsoftLoginSchema, 'POST')
  }

  cancelMicrosoftLogin(channelID: string) {
    return this.write(`/api/v1/channels/${encodeURIComponent(channelID)}/microsoft-login`, emptyResponseSchema, 'DELETE')
  }

  updateSettings(settings: z.infer<typeof runtimeSettingsSchema>) { return this.write('/api/v1/settings', runtimeSettingsSchema, 'PUT', settings) }

  async queryDynamics(query: ContentQuery) {
    return parseDynamicHistoryPage(await httpJSON(`/api/v1/dynamics?${queryString(query)}`, z.unknown()))
  }

  queryComments(query: ContentQuery) { return httpJSON(`/api/v1/comments?${queryString(query)}`, commentHistoryPageSchema) }

  getComment(rpid: string) { return httpJSON(`/api/v1/comments/${encodeURIComponent(rpid)}`, commentDetailSchema) }

  queryAuditLogs(query: AuditQuery) { return httpJSON(`/api/v1/audit-logs?${queryString(query)}`, auditLogPageSchema) }

  private write<T>(path: string, schema: z.ZodType<T>, method: string, body?: unknown) {
    return httpJSON(path, schema, { method, ...(body === undefined ? {} : { body: JSON.stringify(body) }) }, this.csrf)
  }
}

function queryString(query: ContentQuery | AuditQuery): string {
  const values = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== '') values.set(key, String(value))
  }
  return values.toString()
}
