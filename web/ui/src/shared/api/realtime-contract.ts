export const realtimeTopics = [
  'runtime', 'settings', 'channels', 'deliveries', 'microsoft-logins', 'audit-logs',
  'ai-status', 'ai-jobs', 'accounts', 'sources', 'contents', 'backfills',
] as const

export type RealtimeEnvelope = {
  event: 'sync.required' | 'resources.invalidated'
  revision: number
  topics: (typeof realtimeTopics)[number][]
}

const realtimeTopicSet = new Set<string>(realtimeTopics)

export function parseRealtimeEnvelope(value: unknown): RealtimeEnvelope | null {
  if (!value || typeof value !== 'object') return null
  const data = value as Record<string, unknown>
  if (
    Object.keys(data).some(key => !['event', 'revision', 'topics'].includes(key))
    || (data.event !== 'sync.required' && data.event !== 'resources.invalidated')
    || !Number.isSafeInteger(data.revision)
    || (data.revision as number) < 0
    || !Array.isArray(data.topics)
    || !data.topics.every(topic => typeof topic === 'string' && realtimeTopicSet.has(topic))
  ) return null
  return data as RealtimeEnvelope
}
