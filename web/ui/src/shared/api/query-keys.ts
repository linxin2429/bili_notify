import type { QueryClient } from '@tanstack/react-query'
import type { AuditQuery, ContentQuery, RealtimeTopic } from '../../types'

export const queryKeys = {
  session: ['session'] as const, runtime: ['runtime'] as const, settings: ['settings'] as const, ups: ['ups'] as const,
  channels: ['channels'] as const, deliveries: (after = '') => ['deliveries', { after }] as const,
  biliLogin: ['bilibili-login'] as const, microsoftLogins: ['microsoft-logins'] as const,
  dynamics: (query: ContentQuery) => ['dynamics', query] as const, comments: (query: ContentQuery) => ['comments', query] as const,
  comment: (rpid: string) => ['comments', 'detail', rpid] as const, auditLogs: (query: AuditQuery) => ['audit-logs', query] as const,
}

const topicKeys: Record<RealtimeTopic, readonly string[]> = {
  runtime: queryKeys.runtime, settings: queryKeys.settings, ups: queryKeys.ups, channels: queryKeys.channels,
  deliveries: ['deliveries'], 'bilibili-login': queryKeys.biliLogin, 'microsoft-logins': queryKeys.microsoftLogins,
  dynamics: ['dynamics'], comments: ['comments'], 'audit-logs': ['audit-logs'],
}
export function invalidateTopics(client: QueryClient, topics: RealtimeTopic[]) { for (const topic of new Set(topics)) void client.invalidateQueries({ queryKey: topicKeys[topic] }) }
