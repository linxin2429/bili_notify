import type { Query, QueryClient } from '@tanstack/react-query'
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

export const queryPrefixes = {
  sources: ['sources'] as const,
  contents: ['contents'] as const,
  deliveries: ['deliveries'] as const,
  auditLogs: ['audit-logs'] as const,
  aiJobs: ['ai-jobs'] as const,
}

const topicKeys: Record<RealtimeTopic, readonly string[]> = {
  runtime: queryKeys.runtime, settings: queryKeys.settings, channels: queryKeys.channels,
  deliveries: queryPrefixes.deliveries, 'microsoft-logins': queryKeys.microsoftLogins, 'audit-logs': queryPrefixes.auditLogs,
  'ai-status': queryKeys.aiStatus, 'ai-jobs': queryPrefixes.aiJobs,
  accounts: queryKeys.accounts, sources: queryPrefixes.sources, contents: queryPrefixes.contents, backfills: queryPrefixes.sources,
}
export function invalidateTopics(client: QueryClient, topics: RealtimeTopic[], staleBefore?: number) {
  for (const topic of new Set(topics)) {
    void client.invalidateQueries({
      queryKey: topicKeys[topic],
      ...(staleBefore !== undefined && {
        predicate: (query: Query) => query.state.dataUpdatedAt > 0 && query.state.dataUpdatedAt < staleBefore,
      }),
    })
  }
}
