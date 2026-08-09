import { QueryClient } from '@tanstack/react-query'
import { describe, expect, it, vi } from 'vitest'
import { invalidateTopics } from './query-keys'

describe('query consistency', () => {
  it('maps realtime topics to resource query prefixes', () => {
    const client = new QueryClient(); const invalidate = vi.spyOn(client, 'invalidateQueries')
    invalidateTopics(client, ['runtime', 'deliveries', 'deliveries'])
    expect(invalidate).toHaveBeenCalledTimes(2)
    expect(invalidate).toHaveBeenNthCalledWith(1, { queryKey: ['runtime'] })
    expect(invalidate).toHaveBeenNthCalledWith(2, { queryKey: ['deliveries'] })
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
