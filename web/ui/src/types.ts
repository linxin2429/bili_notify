import type { z } from 'zod'
import type {
  auditLogSchema, biliLoginSchema, channelSchema, channelTypeSchema, commentDetailSchema, commentHistorySchema,
  deliverySchema, dynamicHistorySchema, dynamicMediaSchema, dynamicPreviewSchema, dynamicStatsSchema, dynamicVideoSchema,
  microsoftLoginSchema, realtimeTopicSchema, runtimeSchema, runtimeSettingsSchema, serviceStatusSchema, sessionStateSchema, upSchema,
} from './contracts'

export type ConnectionState = 'connecting' | 'live' | 'reconnecting' | 'polling'
export type ThemePreference = 'system' | 'light' | 'dark'
export type ChannelType = z.infer<typeof channelTypeSchema>
export type ServiceStatus = z.infer<typeof serviceStatusSchema>
export type Runtime = z.infer<typeof runtimeSchema>
export type UP = z.infer<typeof upSchema>
export type Channel = z.infer<typeof channelSchema>
export type Delivery = z.infer<typeof deliverySchema>
export type BiliLogin = z.infer<typeof biliLoginSchema>
export type MicrosoftLogin = z.infer<typeof microsoftLoginSchema>
export type RuntimeSettings = z.infer<typeof runtimeSettingsSchema>
export type DynamicHistoryItem = z.infer<typeof dynamicHistorySchema>
export type DynamicMedia = z.infer<typeof dynamicMediaSchema>
export type DynamicPreview = z.infer<typeof dynamicPreviewSchema>
export type DynamicStats = z.infer<typeof dynamicStatsSchema>
export type DynamicVideo = z.infer<typeof dynamicVideoSchema>
export type CommentHistoryItem = z.infer<typeof commentHistorySchema>
export type AuditLog = z.infer<typeof auditLogSchema>
export type AuditOutcome = AuditLog['outcome']
export type CommentDetail = z.infer<typeof commentDetailSchema>
export type SessionState = z.infer<typeof sessionStateSchema>
export type RealtimeTopic = z.infer<typeof realtimeTopicSchema>

export type ChannelDraft =
  | { id?: string; name: string; type: 'email'; enabled: boolean; settings: { host: string; port: string; username: string; from: string; to: string; tls: string }; secrets?: { password?: string } }
  | { id?: string; name: string; type: 'microsoft'; enabled: boolean; settings: { client_id: string; tenant: string; to: string }; secrets?: never }
  | { id?: string; name: string; type: 'dingtalk'; enabled: boolean; settings: Record<string, never>; secrets?: { webhook?: string; secret?: string } }
  | { id?: string; name: string; type: 'feishu'; enabled: boolean; settings: { app_id: string }; secrets?: { webhook?: string; secret?: string; app_secret?: string } }
  | { id?: string; name: string; type: 'wecom'; enabled: boolean; settings: Record<string, never>; secrets?: { webhook?: string } }

export interface ContentQuery { uid?: string; q?: string; from?: string; to?: string; limit?: number; after?: string }
export interface AuditQuery extends Omit<ContentQuery, 'uid'> { action?: string; outcome?: string; resource_type?: string }
export interface CursorPage<T> { items: T[]; page: { next_cursor: string; has_more: boolean } }
