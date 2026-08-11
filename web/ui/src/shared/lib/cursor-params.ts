/** Cursor pagination helpers: URL holds current `after` and a stack of previous cursors. */

export function encodeCursorStack(stack: string[]): string {
  if (stack.length === 0) return ''
  const json = JSON.stringify(stack)
  if (typeof btoa === 'function') {
    return btoa(json).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '')
  }
  return json
}

export function decodeCursorStack(value: string): string[] {
  if (!value) return []
  try {
    let json = value
    if (typeof atob === 'function' && !value.startsWith('[')) {
      const padded = value.replace(/-/g, '+').replace(/_/g, '/')
      const pad = padded.length % 4 === 0 ? '' : '='.repeat(4 - (padded.length % 4))
      json = atob(padded + pad)
    }
    const parsed: unknown = JSON.parse(json)
    return Array.isArray(parsed) ? parsed.filter((item): item is string => typeof item === 'string') : []
  } catch {
    return []
  }
}

/** Build next-page params: push current after onto stack, set after to next cursor. */
export function advanceCursor(currentAfter: string, stackParam: string, nextCursor: string) {
  const stack = decodeCursorStack(stackParam)
  stack.push(currentAfter)
  return { after: nextCursor, stack: encodeCursorStack(stack) }
}

/** Build previous-page params: pop stack into after. */
export function rewindCursor(stackParam: string): { after?: string; stack?: string } {
  const stack = decodeCursorStack(stackParam)
  if (stack.length === 0) return { after: undefined, stack: undefined }
  const after = stack.pop() || undefined
  return { after: after || undefined, stack: stack.length ? encodeCursorStack(stack) : undefined }
}
