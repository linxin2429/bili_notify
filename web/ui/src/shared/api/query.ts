import { queryOptions } from '@tanstack/react-query'
import type { AuditQuery, ContentQuery } from './types'
import { resources } from './resources'
import { queryKeys } from './query-keys'
export { queryKeys } from './query-keys'

export const queries = {
  runtime: () => queryOptions({ queryKey: queryKeys.runtime, queryFn: ({ signal }) => resources.runtime(signal), staleTime: 10_000 }),
  settings: () => queryOptions({ queryKey: queryKeys.settings, queryFn: ({ signal }) => resources.settings(signal), staleTime: 30_000 }),
  accounts: () => queryOptions({ queryKey: queryKeys.accounts, queryFn: ({ signal }) => resources.accounts(signal), staleTime: 10_000 }),
  zsxqGroups: (enabled = true) => queryOptions({ queryKey: queryKeys.zsxqGroups, queryFn: ({ signal }) => resources.zsxqGroups(signal), staleTime: 0, enabled }),
  sources: (platform = '') => queryOptions({ queryKey: queryKeys.sources(platform), queryFn: ({ signal }) => resources.sources(platform, signal), staleTime: 10_000 }),
  contents: (query: ContentQuery) => queryOptions({ queryKey: queryKeys.contents(query), queryFn: ({ signal }) => resources.contents(query, signal), staleTime: 10_000 }),
  content: (id: string) => queryOptions({ queryKey: queryKeys.content(id), queryFn: ({ signal }) => resources.content(id, signal), enabled: Boolean(id) }),
  contentComments: (id: string) => queryOptions({ queryKey: queryKeys.contentComments(id), queryFn: ({ signal }) => resources.contentComments(id, signal), enabled: Boolean(id) }),
  channels: () => queryOptions({ queryKey: queryKeys.channels, queryFn: ({ signal }) => resources.channels(signal), staleTime: 30_000 }),
  deliveries: (after = '') => queryOptions({ queryKey: queryKeys.deliveries(after), queryFn: ({ signal }) => resources.deliveries(after, signal), staleTime: 10_000 }),
  biliLogin: () => queryOptions({ queryKey: queryKeys.biliLogin, queryFn: ({ signal }) => resources.biliLogin(signal), staleTime: 2_000, refetchInterval: query => query.state.data && !['success', 'expired'].includes(query.state.data.status) ? 2_000 : false }),
  microsoftLogins: () => queryOptions({ queryKey: queryKeys.microsoftLogins, queryFn: ({ signal }) => resources.microsoftLogins(signal), staleTime: 2_000, refetchInterval: query => query.state.data?.some(login => login.status === 'pending') ? 2_000 : false }),
  auditLogs: (query: AuditQuery) => queryOptions({ queryKey: queryKeys.auditLogs(query), queryFn: ({ signal }) => resources.auditLogs(query, signal), staleTime: 10_000 }),
  aiStatus: () => queryOptions({ queryKey: queryKeys.aiStatus, queryFn: ({ signal }) => resources.aiStatus(signal), staleTime: 5_000, refetchInterval: 10_000 }),
  aiProfiles: () => queryOptions({ queryKey: queryKeys.aiProfiles, queryFn: ({ signal }) => resources.aiProfiles(signal), staleTime: 30_000 }),
  aiPrompts: () => queryOptions({ queryKey: queryKeys.aiPrompts, queryFn: ({ signal }) => resources.aiPrompts(signal), staleTime: 30_000 }),
  aiJobs: (query: { kind?: string; state?: string; limit?: number; offset?: number } = {}) => queryOptions({ queryKey: queryKeys.aiJobs(query), queryFn: ({ signal }) => resources.aiJobs(query, signal), staleTime: 2_000, refetchInterval: 5_000 }),
  aiJob: (id: string) => queryOptions({ queryKey: queryKeys.aiJob(id), queryFn: ({ signal }) => resources.aiJob(id, signal), enabled: Boolean(id), refetchInterval: query => query.state.data && ['queued', 'running'].includes(query.state.data.state) ? 2_000 : false }),
}
