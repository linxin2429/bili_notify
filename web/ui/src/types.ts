export type ConnectionState = 'connecting' | 'live' | 'reconnecting' | 'stale'
export type ThemePreference = 'system' | 'light' | 'dark'
export type ChannelType = 'email' | 'microsoft' | 'dingtalk' | 'feishu' | 'wecom'

export interface ServiceStatus {
  auth_valid: boolean
  last_success_at?: string
  up_count: number
  channel_count: number
  outbox_depth: number
  oldest_delivery?: string
  ready: boolean
  risk_paused_until?: string
}

export interface UP {
  uid: string
  name: string
  enabled: boolean
  baseline_ready: boolean
  last_poll_at?: string
  last_success_at?: string
  last_error?: string
  consecutive_fail: number
}

export interface Channel {
  id: string
  name: string
  type: ChannelType
  enabled: boolean
  settings: Record<string, string>
  configured_secrets: string[]
  created_at: string
  updated_at: string
}

export interface Delivery {
  id: string
  kind?: 'dynamic' | 'comment'
  dynamic?: {
    id: string
    uid: string
    up_name: string
    type: string
    published_at: string
    summary: string
    url: string
  }
  comment?: {
    rpid: string
    up_uid: string
    up_name: string
    content_type: string
    content_id: string
    content_title?: string
    content_url: string
    published_at: string
  }
  channel_id: string
  state: 'pending' | 'blocked'
  attempts: number
  next_at: string
  last_error?: string
  created_at: string
}

export interface BiliLogin {
  id: string
  status: string
  expires_at: string
  qr_data_url?: string
}

export interface MicrosoftLogin {
  channel_id: string
  status: string
  user_code?: string
  verification_uri?: string
  verification_uri_complete?: string
  expires_at?: string
  error?: string
}

export interface RuntimeSettings {
  poll_interval_sec: number
  request_rate: number
  request_concurrency: number
  comment_enabled: boolean
  comment_track_n: number
  comment_root_pages: number
  comment_reply_pages: number
  comment_batch_interval_sec: number
}

export interface DashboardSnapshot {
  status: ServiceStatus
  settings: RuntimeSettings
  ups: UP[]
  channels: Channel[]
  deliveries: Delivery[]
  bili_login?: BiliLogin | null
  microsoft_logins: MicrosoftLogin[]
  timezone: string
  updated_at: string
}

export interface ChannelDraft {
  id?: string
  name: string
  type: ChannelType
  enabled: boolean
  settings: Record<string, string>
  secrets?: Record<string, string>
}
