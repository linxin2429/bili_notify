import { describe, expect, it } from 'vitest'
import { advanceCursor, decodeCursorStack, encodeCursorStack, rewindCursor } from './cursor-params'

describe('cursor-params', () => {
  it('round-trips cursor stacks', () => {
    expect(decodeCursorStack('')).toEqual([])
    const encoded = encodeCursorStack(['', 'c1', 'c2'])
    expect(decodeCursorStack(encoded)).toEqual(['', 'c1', 'c2'])
  })

  it('advances and rewinds without browser history', () => {
    const next = advanceCursor('', '', 'cursor-1')
    expect(decodeCursorStack(next.stack)).toEqual([''])
    expect(next.after).toBe('cursor-1')

    const deeper = advanceCursor(next.after, next.stack, 'cursor-2')
    expect(deeper.after).toBe('cursor-2')
    expect(decodeCursorStack(deeper.stack)).toEqual(['', 'cursor-1'])

    const back = rewindCursor(deeper.stack)
    expect(back.after).toBe('cursor-1')
    expect(decodeCursorStack(back.stack || '')).toEqual([''])

    const first = rewindCursor(back.stack || '')
    expect(first.after).toBeUndefined()
    expect(first.stack).toBeUndefined()
  })
})
