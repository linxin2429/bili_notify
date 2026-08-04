import { z } from 'zod'
import type {
  BiliLogin, Channel, ChannelDraft, CommentDetail, ContentPage, DashboardSnapshot, DynamicDetail,
  DynamicHistoryItem, MicrosoftLogin, RuntimeSettings, UP,
} from './types'

const requestTimeoutMS = 25_000

const dynamicMediaSchema = z.object({
  kind: z.string(), url: z.string(), width: z.number().int().optional(), height: z.number().int().optional(),
})

const dynamicPreviewSchema = z.object({
  id: z.string().optional(), uid: z.string().optional(), up_name: z.string().optional(), type: z.string().optional(),
  title: z.string().optional(), summary: z.string().optional(), description: z.string().optional(),
  url: z.string().optional(), target_url: z.string().optional(), badge: z.string().optional(),
  media: z.array(dynamicMediaSchema).optional(),
})

const dynamicHistorySchema = z.object({
  id: z.string(), uid: z.string(), up_name: z.string(), type: z.string(), published_at: z.string(), discovered_at: z.string(),
  baseline: z.boolean(), title: z.string().optional(), summary: z.string().optional(), description: z.string().optional(),
  url: z.string().optional(), target_url: z.string().optional(), badge: z.string().optional(),
  media: z.array(dynamicMediaSchema).optional(), original: dynamicPreviewSchema.optional(),
})

const dynamicHistoryPageSchema = z.object({
  items: z.array(dynamicHistorySchema), total: z.number().int(), limit: z.number().int(), offset: z.number().int(),
})

export function parseDynamicHistoryPage(data: unknown): ContentPage<DynamicHistoryItem> {
  return dynamicHistoryPageSchema.parse(data)
}

export async function httpJSON<T>(path: string, options: RequestInit = {}, csrf = ''): Promise<T> {
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
    if (response.status === 204) return undefined as T
    const body = await response.json()
    if (!response.ok) throw new Error(body.error?.message || response.statusText)
    return body as T
  } catch (error) {
    if (timedOut) throw new Error('操作超时，结果未知，请刷新状态后重试')
    throw error
  } finally {
    window.clearTimeout(timeout)
    options.signal?.removeEventListener('abort', abortFromCaller)
  }
}

export interface ContentQuery {
  uid?: string
  q?: string
  from?: string
  to?: string
  limit?: number
  offset?: number
}

export class AdminAPI {
  constructor(private readonly csrf: string) {}

  dashboard() { return httpJSON<DashboardSnapshot>('/api/v1/dashboard') }

  createUP(input: Pick<UP, 'uid' | 'name' | 'enabled'>) {
    return this.write<UP>('/api/v1/ups', 'POST', input)
  }

  updateUP(input: Pick<UP, 'uid' | 'name' | 'enabled'>) {
    return this.write<UP>(`/api/v1/ups/${encodeURIComponent(input.uid)}`, 'PUT', { name: input.name, enabled: input.enabled })
  }

  deleteUP(uid: string) { return this.write<void>(`/api/v1/ups/${encodeURIComponent(uid)}`, 'DELETE') }

  createChannel(input: ChannelDraft) { return this.write<Channel>('/api/v1/channels', 'POST', input) }

  updateChannel(input: ChannelDraft & { id: string }) {
    return this.write<Channel>(`/api/v1/channels/${encodeURIComponent(input.id)}`, 'PUT', input)
  }

  deleteChannel(id: string) { return this.write<void>(`/api/v1/channels/${encodeURIComponent(id)}`, 'DELETE') }

  testChannel(id: string) { return this.write<{ status: string }>(`/api/v1/channels/${encodeURIComponent(id)}/test`, 'POST') }

  startBiliLogin() { return this.write<BiliLogin>('/api/v1/bilibili-login', 'POST') }

  cancelBiliLogin(id: string) { return this.write<void>(`/api/v1/bilibili-login/${encodeURIComponent(id)}`, 'DELETE') }

  startMicrosoftLogin(channelID: string) {
    return this.write<MicrosoftLogin>(`/api/v1/channels/${encodeURIComponent(channelID)}/microsoft-login`, 'POST')
  }

  cancelMicrosoftLogin(channelID: string) {
    return this.write<void>(`/api/v1/channels/${encodeURIComponent(channelID)}/microsoft-login`, 'DELETE')
  }

  updateSettings(settings: RuntimeSettings) { return this.write<RuntimeSettings>('/api/v1/settings', 'PUT', settings) }

  async queryDynamics(query: ContentQuery) {
    return parseDynamicHistoryPage(await httpJSON<unknown>(`/api/v1/dynamics?${queryString(query)}`))
  }

  queryComments<T>(query: ContentQuery) { return httpJSON<ContentPage<T>>(`/api/v1/comments?${queryString(query)}`) }

  getDynamic(id: string) { return httpJSON<DynamicDetail>(`/api/v1/dynamics/${encodeURIComponent(id)}`) }

  getComment(rpid: string) { return httpJSON<CommentDetail>(`/api/v1/comments/${encodeURIComponent(rpid)}`) }

  private write<T>(path: string, method: string, body?: unknown) {
    return httpJSON<T>(path, { method, ...(body === undefined ? {} : { body: JSON.stringify(body) }) }, this.csrf)
  }
}

function queryString(query: ContentQuery): string {
  const values = new URLSearchParams()
  for (const [key, value] of Object.entries(query)) {
    if (value !== undefined && value !== '') values.set(key, String(value))
  }
  return values.toString()
}
