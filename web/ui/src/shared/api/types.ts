import type { components } from './generated/schema'
import type { z } from 'zod'
import type { aiJobSchema, aiProfileTestResultSchema, aiWorkerStatusSchema } from './contracts'

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
export interface AIProfile { id: string; name: string; kind: 'transcription' | 'text'; base_url: string; model: string; language?: string; prompt?: string; temperature?: number; max_output_tokens?: number; context_window_chars?: number; timeout_sec: number; enabled: boolean; default: boolean; configured_secrets: string[]; created_at: string; updated_at: string }
export interface AIProfileDraft extends Omit<AIProfile, 'id' | 'configured_secrets' | 'created_at' | 'updated_at'> { id?: string; api_key?: string }
export type AIProfileTestResult = z.infer<typeof aiProfileTestResultSchema>
export interface AIPrompt { id: string; name: string; system_prompt: string; chunk_prompt: string; reduce_prompt: string; default: boolean; created_at: string; updated_at: string }
export interface AIPromptDraft extends Omit<AIPrompt, 'id' | 'created_at' | 'updated_at'> { id?: string }
export type AIJob = z.infer<typeof aiJobSchema>
export type AIWorkerStatus = z.infer<typeof aiWorkerStatusSchema>

export type ChannelDraft =
  | { id?: string; name: string; type: 'email'; enabled: boolean; settings: { host: string; port: string; username: string; from: string; to: string; tls: string }; secrets?: { password?: string } }
  | { id?: string; name: string; type: 'microsoft'; enabled: boolean; settings: { client_id: string; tenant: string; to: string }; secrets?: never }
  | { id?: string; name: string; type: 'dingtalk'; enabled: boolean; settings: Record<string, never>; secrets?: { webhook?: string; secret?: string } }
  | { id?: string; name: string; type: 'feishu'; enabled: boolean; settings: { app_id: string }; secrets?: { webhook?: string; secret?: string; app_secret?: string } }
  | { id?: string; name: string; type: 'wecom'; enabled: boolean; settings: Record<string, never>; secrets?: { webhook?: string } }

export interface ContentQuery { uid?: string; q?: string; from?: string; to?: string; limit?: number; after?: string }
export interface AuditQuery extends Omit<ContentQuery, 'uid'> { action?: string; outcome?: string; resource_type?: string }
export interface CursorPage<T> { items: T[]; page: Schemas['PageMetadata'] }
