import { queryOptions } from '@tanstack/react-query'
import { requestJSON } from '../../shared/api/client'

/** Minimal runtime projection for shell badges — avoids importing the full resources/contracts surface. */
const outboxSchema = {
  safeParse(value: unknown): { success: true; data: number } | { success: false } {
    if (!value || typeof value !== 'object') return { success: false }
    const status = (value as { status?: unknown }).status
    if (!status || typeof status !== 'object') return { success: false }
    const depth = (status as { outbox_depth?: unknown }).outbox_depth
    return typeof depth === 'number' && Number.isFinite(depth)
      ? { success: true, data: depth }
      : { success: false }
  },
}

export const shellOutboxQuery = () => queryOptions({
  queryKey: ['runtime', 'shell-outbox'] as const,
  queryFn: async ({ signal }) => requestJSON('/api/v4/runtime', outboxSchema, { signal }),
  staleTime: 10_000,
})
