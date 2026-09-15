import { queryOptions } from '@tanstack/react-query'
import { requestJSON } from './client'
import { queryKeys } from './query-keys'
import type { Runtime, ServiceStatus } from './types'

const runtimeSchema = {
  safeParse(value: unknown): { success: true; data: Runtime } | { success: false } {
    const runtime = parseRuntime(value)
    return runtime ? { success: true, data: runtime } : { success: false }
  },
}

export const runtimeQuery = () => queryOptions({
  queryKey: queryKeys.runtime,
  queryFn: ({ signal }) => requestJSON('/api/v4/runtime', runtimeSchema, { signal }),
  staleTime: 10_000,
})

function parseRuntime(value: unknown): Runtime | null {
  if (!record(value) || typeof value.timezone !== 'string' || typeof value.updated_at !== 'string') return null
  const status = parseStatus(value.status)
  if (!status) return null
  return { timezone: value.timezone, updated_at: value.updated_at, status }
}

function parseStatus(value: unknown): ServiceStatus | null {
  if (!record(value) || typeof value.auth_valid !== 'boolean' || !nonnegativeInt(value.up_count) || !nonnegativeInt(value.channel_count) || !nonnegativeInt(value.outbox_depth) || typeof value.ready !== 'boolean') return null
  const account = value.bili_account
  if (account !== undefined) {
    if (!record(account) || typeof account.uid !== 'string' || typeof account.name !== 'string') return null
  }
  if (value.last_success_at !== undefined && typeof value.last_success_at !== 'string') return null
  if (value.oldest_delivery !== undefined && typeof value.oldest_delivery !== 'string') return null
  if (value.risk_paused_until !== undefined && typeof value.risk_paused_until !== 'string') return null
  return {
    auth_valid: value.auth_valid,
    up_count: value.up_count,
    channel_count: value.channel_count,
    outbox_depth: value.outbox_depth,
    ready: value.ready,
    ...(account && record(account) ? { bili_account: { uid: String(account.uid), name: String(account.name) } } : {}),
    ...(typeof value.last_success_at === 'string' ? { last_success_at: value.last_success_at } : {}),
    ...(typeof value.oldest_delivery === 'string' ? { oldest_delivery: value.oldest_delivery } : {}),
    ...(typeof value.risk_paused_until === 'string' ? { risk_paused_until: value.risk_paused_until } : {}),
  }
}

function nonnegativeInt(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0
}

function record(value: unknown): value is Record<string, unknown> {
  return Boolean(value) && typeof value === 'object'
}
