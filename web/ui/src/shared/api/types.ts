import type { components } from './generated/schema'

type Schemas = components['schemas']

export type ConnectionState = 'connecting' | 'live' | 'reconnecting' | 'polling'
export type ThemePreference = 'system' | 'light' | 'dark'
export type ChannelType = Schemas['ChannelInput']['type']
export type ServiceStatus = Schemas['Status']
export type Runtime = Schemas['Runtime']
export type UP = Schemas['UP']
export type Channel = Schemas['Channel']
export type Delivery = Schemas['Delivery']
export type BiliLogin = Schemas['BilibiliLogin'] | null
export type MicrosoftLogin = Schemas['MicrosoftLogin']
export type RuntimeSettings = Schemas['RuntimeSettings']
export type DynamicHistoryItem = Schemas['DynamicHistory']
export type DynamicMedia = Schemas['DynamicMedia']
export type DynamicPreview = Schemas['DynamicOriginal']
export type DynamicStats = Schemas['DynamicStats']
export type DynamicVideo = Schemas['DynamicVideo']
export type CommentHistoryItem = Schemas['CommentHistory']
export type AuditLog = Schemas['AuditLog']
export type AuditOutcome = AuditLog['outcome']
export type CommentDetail = Schemas['CommentNotification']
export type SessionState = Schemas['Session']
export type RealtimeTopic = Schemas['RealtimeEvent']['topics'][number]

export type ChannelDraft =
  | { id?: string; name: string; type: 'email'; enabled: boolean; settings: { host: string; port: string; username: string; from: string; to: string; tls: string }; secrets?: { password?: string } }
  | { id?: string; name: string; type: 'microsoft'; enabled: boolean; settings: { client_id: string; tenant: string; to: string }; secrets?: never }
  | { id?: string; name: string; type: 'dingtalk'; enabled: boolean; settings: Record<string, never>; secrets?: { webhook?: string; secret?: string } }
  | { id?: string; name: string; type: 'feishu'; enabled: boolean; settings: { app_id: string }; secrets?: { webhook?: string; secret?: string; app_secret?: string } }
  | { id?: string; name: string; type: 'wecom'; enabled: boolean; settings: Record<string, never>; secrets?: { webhook?: string } }

export interface ContentQuery { uid?: string; q?: string; from?: string; to?: string; limit?: number; after?: string }
export interface AuditQuery extends Omit<ContentQuery, 'uid'> { action?: string; outcome?: string; resource_type?: string }
export interface CursorPage<T> { items: T[]; page: Schemas['PageMetadata'] }
