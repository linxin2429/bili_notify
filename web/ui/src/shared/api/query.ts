import { queryOptions } from '@tanstack/react-query'
import type { AuditQuery, ContentQuery } from './types'
import { resources } from './resources'
import { queryKeys } from './query-keys'
export { queryKeys } from './query-keys'

export const queries = {
  runtime: () => queryOptions({ queryKey: queryKeys.runtime, queryFn: ({ signal }) => resources.runtime(signal), staleTime: 10_000 }),
  settings: () => queryOptions({ queryKey: queryKeys.settings, queryFn: ({ signal }) => resources.settings(signal), staleTime: 30_000 }),
  ups: () => queryOptions({ queryKey: queryKeys.ups, queryFn: ({ signal }) => resources.ups(signal), staleTime: 30_000 }),
  channels: () => queryOptions({ queryKey: queryKeys.channels, queryFn: ({ signal }) => resources.channels(signal), staleTime: 30_000 }),
  deliveries: (after = '') => queryOptions({ queryKey: queryKeys.deliveries(after), queryFn: ({ signal }) => resources.deliveries(after, signal), staleTime: 10_000 }),
  biliLogin: () => queryOptions({ queryKey: queryKeys.biliLogin, queryFn: ({ signal }) => resources.biliLogin(signal), staleTime: 2_000, refetchInterval: query => query.state.data && !['success', 'expired'].includes(query.state.data.status) ? 2_000 : false }),
  microsoftLogins: () => queryOptions({ queryKey: queryKeys.microsoftLogins, queryFn: ({ signal }) => resources.microsoftLogins(signal), staleTime: 2_000, refetchInterval: query => query.state.data?.some(login => login.status === 'pending') ? 2_000 : false }),
  dynamics: (query: ContentQuery) => queryOptions({ queryKey: queryKeys.dynamics(query), queryFn: ({ signal }) => resources.dynamics(query, signal), staleTime: 10_000 }),
  comments: (query: ContentQuery) => queryOptions({ queryKey: queryKeys.comments(query), queryFn: ({ signal }) => resources.comments(query, signal), staleTime: 10_000 }),
  comment: (rpid: string) => queryOptions({ queryKey: queryKeys.comment(rpid), queryFn: ({ signal }) => resources.comment(rpid, signal), enabled: Boolean(rpid) }),
  auditLogs: (query: AuditQuery) => queryOptions({ queryKey: queryKeys.auditLogs(query), queryFn: ({ signal }) => resources.auditLogs(query, signal), staleTime: 10_000 }),
}
