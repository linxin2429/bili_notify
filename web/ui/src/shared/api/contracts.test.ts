import { describe, expect, it } from 'vitest'
import { auditLogPageSchema, contentPageSchema, runtimeSchema, runtimeSettingsSchema, websocketEnvelopeSchema } from './contracts'
import { makeAudit, settings } from '../../test/fixtures'

describe('v3 transport contracts', () => {
  it('accepts authoritative runtime state without deriving readiness in the browser', () => {
    const value = { status: { auth_valid: false, up_count: 0, channel_count: 0, outbox_depth: 0, ready: false }, timezone: 'Asia/Shanghai', updated_at: '2026-08-09T10:00:00Z' }
    expect(runtimeSchema.parse(value)).toEqual(value)
  })

  it('rejects an invalid content item instead of silently shrinking a page', () => {
    const value = { items: [{ id: 'missing-required-fields' }], page: { next_cursor: '', has_more: false } }
    expect(contentPageSchema.safeParse(value).success).toBe(false)
  })

  it('requires opaque cursor metadata for every page', () => {
    expect(auditLogPageSchema.parse({ items: [makeAudit()], page: { next_cursor: 'opaque', has_more: true } }).page.next_cursor).toBe('opaque')
    expect(auditLogPageSchema.safeParse({ items: [], total: 0, offset: 0 }).success).toBe(false)
  })

  it.each([
    { name: 'sync', value: { event: 'sync.required', revision: 1, topics: ['runtime', 'sources'] }, valid: true },
    { name: 'legacy snapshot', value: { event: 'snapshot', revision: 1, data: {} }, valid: false },
    { name: 'extra field', value: { event: 'resources.invalidated', revision: 2, topics: [], data: {} }, valid: false },
  ])('validates $name websocket envelopes', ({ value, valid }) => expect(websocketEnvelopeSchema.safeParse(value).success).toBe(valid))

  it('validates the complete persisted settings resource', () => {
    expect(runtimeSettingsSchema.parse(settings)).toEqual(settings)
    expect(runtimeSettingsSchema.safeParse({ ...settings, delivery_retry_delays_sec: [30, 20, 120, 600, 3600] }).success).toBe(false)
  })
})
