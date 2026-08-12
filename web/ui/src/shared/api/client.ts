import { ApiError, parseErrorBody } from './errors'

export interface ResponseSchema<T> { safeParse(value: unknown): { success: true; data: T } | { success: false } }

const timeoutMS = 25_000
let authenticationLost: (() => void) | undefined

export function setAuthenticationLostHandler(handler: (() => void) | undefined) { authenticationLost = handler }

export async function requestJSON<T>(path: string, schema: ResponseSchema<T>, init: RequestInit & { csrf?: string } = {}): Promise<T> {
  const headers = new Headers(init.headers)
  if (init.body) headers.set('Content-Type', 'application/json')
  if (init.csrf) headers.set('X-CSRF-Token', init.csrf)
  const controller = new AbortController()
  const callerSignal = init.signal
  const abortFromCaller = () => controller.abort(callerSignal?.reason)
  if (callerSignal?.aborted) abortFromCaller()
  else callerSignal?.addEventListener('abort', abortFromCaller, { once: true })
  let timedOut = false
  const timeout = window.setTimeout(() => { timedOut = true; controller.abort() }, timeoutMS)
  try {
    const requestInit = { ...init }
    delete requestInit.csrf
    const response = await fetch(path, { ...requestInit, headers, credentials: 'same-origin', signal: controller.signal })
    const requestId = response.headers.get('X-Request-ID') || undefined
    if (response.status === 204) return parseContract(schema, undefined, requestId)
    const contentType = response.headers.get('Content-Type') || ''
    const body: unknown = contentType.includes('json') ? await response.json() : await response.text()
    if (!response.ok) {
      const parsed = parseErrorBody(body)
      const detail = parsed.success ? parsed.data.error : undefined
      const message = detail?.message || (typeof body === 'string' && body.trim()) || response.statusText || `HTTP ${response.status}`
      const error = new ApiError(message, 'http', { status: response.status, code: detail?.code, fields: detail?.fields, requestId })
      // Only admin-session failures force a global logout. An upstream integration
      // may also return 401 with a different code and must stay on the current page.
      if (response.status === 401 && isAdminSessionFailure(detail?.code)) authenticationLost?.()
      throw error
    }
    return parseContract(schema, body, requestId)
  } catch (error) {
    if (error instanceof ApiError) throw error
    if (timedOut) throw new ApiError('操作超时，结果未知，请刷新状态后重试', 'timeout')
    if (controller.signal.aborted) throw new ApiError('请求已取消', 'aborted')
    throw new ApiError(error instanceof Error ? error.message : '无法连接服务器', 'network')
  } finally {
    window.clearTimeout(timeout)
    callerSignal?.removeEventListener('abort', abortFromCaller)
  }
}

function parseContract<T>(schema: ResponseSchema<T>, body: unknown, requestId?: string): T {
  const parsed = schema.safeParse(body)
  if (!parsed.success) throw new ApiError('服务器响应不符合 API 契约', 'contract', { requestId, retryable: false })
  return parsed.data
}

function isAdminSessionFailure(code?: string): boolean {
  return code === 'authentication_required' || code === 'invalid_csrf' || code === 'session_expired'
}

export function queryString(values: object): string {
  const params = new URLSearchParams()
  for (const [key, value] of Object.entries(values as Record<string, unknown>)) {
    if ((typeof value === 'string' || typeof value === 'number') && value !== '') params.set(key, String(value))
  }
  const encoded = params.toString()
  return encoded ? `?${encoded}` : ''
}
