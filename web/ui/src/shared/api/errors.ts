export type ApiErrorKind = 'http' | 'network' | 'timeout' | 'contract' | 'aborted'

export class ApiError extends Error {
  constructor(
    message: string,
    readonly kind: ApiErrorKind,
    readonly options: { status?: number; code?: string; requestId?: string; fields?: Record<string, string>; retryable?: boolean } = {},
  ) {
    super(message)
    this.name = 'ApiError'
  }

  get status() { return this.options.status }
  get code() { return this.options.code }
  get requestId() { return this.options.requestId }
  get fields() { return this.options.fields }
  get retryable() { return this.options.retryable ?? (this.kind === 'network' || this.kind === 'timeout' || (this.status ?? 0) >= 500) }
}

export function apiErrorMessage(error: unknown): string {
  if (error instanceof ApiError) return error.requestId ? `${error.message}（请求 ${error.requestId}）` : error.message
  return error instanceof Error ? error.message : '发生未知错误'
}

export function parseErrorBody(body: unknown): { success: true; data: { error: { code?: string; message?: string; fields?: Record<string, string> } } } | { success: false } {
  if (!body || typeof body !== 'object' || !('error' in body)) return { success: false }
  const error = (body as { error?: unknown }).error
  if (!error || typeof error !== 'object') return { success: false }
  const value = error as Record<string, unknown>
  const fields = value.fields && typeof value.fields === 'object' ? Object.fromEntries(Object.entries(value.fields).filter((entry): entry is [string, string] => typeof entry[1] === 'string')) : undefined
  return { success: true, data: { error: { ...(typeof value.code === 'string' && { code: value.code }), ...(typeof value.message === 'string' && { message: value.message }), ...(fields && { fields }) } } }
}
