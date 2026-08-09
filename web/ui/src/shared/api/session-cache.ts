import type { QueryClient } from '@tanstack/react-query'
import type { components } from './generated/schema'
import { queryKeys } from './query-keys'

type Session = components['schemas']['Session']
let replacementsInFlight = 0

export function replaceSessionState(client: QueryClient, session: Session) {
  client.removeQueries({ predicate: query => query.queryKey[0] !== queryKeys.session[0] })
  client.setQueryData(queryKeys.session, session)
}

export async function duringSessionReplacement<T>(replace: () => Promise<T>) {
  replacementsInFlight += 1
  try { return await replace() } finally { replacementsInFlight -= 1 }
}

export function isSessionReplacementPending() { return replacementsInFlight > 0 }
