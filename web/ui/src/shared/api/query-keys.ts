import type { QueryClient } from '@tanstack/react-query'
import type { AuditQuery, ContentQuery, RealtimeTopic } from './types'

export const queryKeys = {
  session: ['session'] as const, runtime: ['runtime'] as const, settings: ['settings'] as const,
  accounts: ['accounts'] as const, zsxqGroups: ['accounts', 'zsxq', 'groups'] as const, sources: (platform = '') => ['sources', platform] as const,
  contents: (query: ContentQuery) => ['contents', query] as const, content: (id: string) => ['contents', 'detail', id] as const,
  contentComments: (id: string) => ['contents', 'comments', id] as const,
  channels: ['channels'] as const, deliveries: (after = '') => ['deliveries', { after }] as const,
  biliLogin: ['accounts', 'bilibili', 'qr'] as const, microsoftLogins: ['microsoft-logins'] as const,
  auditLogs: (query: AuditQuery) => ['audit-logs', query] as const,
  aiStatus: ['ai-status'] as const, aiProfiles: ['ai-profiles'] as const, aiPrompts: ['ai-prompts'] as const,
  aiJobs: (query: object = {}) => ['ai-jobs', query] as const, aiJob: (id: string) => ['ai-jobs', 'detail', id] as const,
}

const topicKeys: Record<RealtimeTopic, readonly string[]> = {
  runtime: queryKeys.runtime, settings: queryKeys.settings, channels: queryKeys.channels,
  deliveries: ['deliveries'], 'microsoft-logins': queryKeys.microsoftLogins, 'audit-logs': ['audit-logs'],
  'ai-status': queryKeys.aiStatus, 'ai-jobs': ['ai-jobs'],
  accounts: queryKeys.accounts, sources: ['sources'], contents: ['contents'], backfills: ['sources'],
}
export function invalidateTopics(client: QueryClient, topics: RealtimeTopic[]) { for (const topic of new Set(topics)) void client.invalidateQueries({ queryKey: topicKeys[topic] }) }
