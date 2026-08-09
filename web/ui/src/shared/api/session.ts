import { queryOptions } from '@tanstack/react-query'
import { requestJSON } from './client'
import { queryKeys } from './query-keys'

interface Session { setup_required: boolean; authenticated: boolean; csrf_token?: string }
const sessionSchema = simpleSchema<Session>(value => {
  if (!record(value) || typeof value.setup_required !== 'boolean' || typeof value.authenticated !== 'boolean' || (value.csrf_token !== undefined && typeof value.csrf_token !== 'string')) return null
  return { setup_required: value.setup_required, authenticated: value.authenticated, ...(typeof value.csrf_token === 'string' && { csrf_token: value.csrf_token }) }
})
const csrfSchema = simpleSchema<{ csrf_token: string }>(value => record(value) && typeof value.csrf_token === 'string' ? { csrf_token: value.csrf_token } : null)
const emptySchema = simpleSchema<undefined>(value => value === undefined ? undefined : null)
const root = '/api/v2'
export const sessionAPI = {
  get: (signal?: AbortSignal) => requestJSON(`${root}/session`, sessionSchema, { signal }),
  setup: (code: string, password: string) => requestJSON(`${root}/setup`, csrfSchema, { method: 'POST', body: JSON.stringify({ setup_code: code, password }) }),
  login: (password: string) => requestJSON(`${root}/session`, csrfSchema, { method: 'POST', body: JSON.stringify({ password }) }),
  logout: (csrf: string) => requestJSON(`${root}/session`, emptySchema, { method: 'DELETE', csrf }),
  changePassword: (csrf: string, current: string, replacement: string) => requestJSON(`${root}/session/password`, emptySchema, { method: 'PUT', csrf, body: JSON.stringify({ current_password: current, new_password: replacement }) }),
}
export const sessionQuery = () => queryOptions({ queryKey: queryKeys.session, queryFn: ({ signal }) => sessionAPI.get(signal), staleTime: 15_000, retry: 1 })

function record(value: unknown): value is Record<string, unknown> { return Boolean(value) && typeof value === 'object' }
function simpleSchema<T>(parse: (value: unknown) => T | null | undefined) {
  return { safeParse(value: unknown): { success: true; data: T } | { success: false } { const data = parse(value); return data === null ? { success: false } : { success: true, data: data as T } } }
}
