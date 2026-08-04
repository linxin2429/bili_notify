import type {
  BiliLogin, Channel, ChannelDraft, CommentDetail, ContentPage, DashboardSnapshot, DynamicDetail,
  MicrosoftLogin, RuntimeSettings, UP,
} from './types'

export async function httpJSON<T>(path: string, options: RequestInit = {}, csrf = ''): Promise<T> {
  const headers = new Headers(options.headers)
  if (options.body) headers.set('Content-Type', 'application/json')
  if (csrf) headers.set('X-CSRF-Token', csrf)
  const response = await fetch(path, { ...options, headers, credentials: 'same-origin' })
  if (response.status === 204) return undefined as T
  const body = await response.json()
  if (!response.ok) throw new Error(body.error?.message || response.statusText)
  return body as T
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

  queryDynamics<T>(query: ContentQuery) { return httpJSON<ContentPage<T>>(`/api/v1/dynamics?${queryString(query)}`) }

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
