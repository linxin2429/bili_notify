import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { queries } from './query'
import { invalidateTopics, queryPrefixes } from './query-keys'

describe('query consistency', () => {
  it('maps realtime topics to resource query prefixes', () => {
    const client = new QueryClient(); const invalidate = vi.spyOn(client, 'invalidateQueries')
    invalidateTopics(client, ['runtime', 'deliveries', 'deliveries'])
    expect(invalidate).toHaveBeenCalledTimes(2)
    expect(invalidate).toHaveBeenNthCalledWith(1, { queryKey: ['runtime'] })
    expect(invalidate).toHaveBeenNthCalledWith(2, { queryKey: queryPrefixes.deliveries })
  })

  it('only invalidates queries present in the reconnect baseline', () => {
    const client = new QueryClient(); const invalidate = vi.spyOn(client, 'invalidateQueries')
    const include = (query: { queryHash: string }) => query.queryHash === 'baseline'
    invalidateTopics(client, ['runtime'], include)
    expect(invalidate).toHaveBeenCalledWith({ queryKey: ['runtime'], predicate: include })
  })

  it('stops AI polling while the websocket is live', () => {
    expect(queries.aiStatus(true).refetchInterval).toBe(false)
    expect(queries.aiStatus(false).refetchInterval).toBe(10_000)
    expect(queries.aiJobs({}, true).refetchInterval).toBe(false)
    expect(queries.aiJobs({}, false).refetchInterval).toBe(5_000)
  })

  it('does not let an old query overwrite state after a mutation cancels it', async () => {
    const client = new QueryClient(); let resolveOld: (value: string[]) => void = () => undefined
    const old = client.fetchQuery({ queryKey: ['ups'], queryFn: () => new Promise<string[]>(resolve => { resolveOld = resolve }) })
    await client.cancelQueries({ queryKey: ['ups'] })
    client.setQueryData(['ups'], ['authoritative mutation result'])
    resolveOld(['stale result'])
    await old.catch(() => undefined)
    await Promise.resolve()
    expect(client.getQueryData(['ups'])).toEqual(['authoritative mutation result'])
  })
})
