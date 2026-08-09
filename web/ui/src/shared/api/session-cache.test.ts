import { QueryClient, QueryObserver } from '@tanstack/react-query'
import { describe, expect, it } from 'vitest'
import { queryKeys } from './query-keys'
import { duringSessionReplacement, isSessionReplacementPending, replaceSessionState } from './session-cache'

describe('session cache transitions', () => {
  it('notifies the active session observer while clearing protected resources', () => {
    const client = new QueryClient()
    client.setQueryData(queryKeys.session, { setup_required: false, authenticated: true, csrf_token: 'old-csrf' })
    client.setQueryData(queryKeys.runtime, { private: true })
    const observer = new QueryObserver(client, { queryKey: queryKeys.session, queryFn: async () => ({ setup_required: false, authenticated: true, csrf_token: 'old-csrf' }) })
    const tokens: Array<string | undefined> = []
    const unsubscribe = observer.subscribe(result => tokens.push(result.data?.csrf_token))

    replaceSessionState(client, { setup_required: false, authenticated: true, csrf_token: 'replacement-csrf' })

    expect(client.getQueryData(queryKeys.runtime)).toBeUndefined()
    expect(client.getQueryData(queryKeys.session)).toEqual({ setup_required: false, authenticated: true, csrf_token: 'replacement-csrf' })
    expect(tokens).toContain('replacement-csrf')
    unsubscribe()
  })

  it('marks only the lifetime of an in-flight session replacement', async () => {
    let release!: () => void
    const replacement = duringSessionReplacement(() => new Promise<void>(resolve => { release = resolve }))
    expect(isSessionReplacementPending()).toBe(true)
    release()
    await replacement
    expect(isSessionReplacementPending()).toBe(false)
  })
})
