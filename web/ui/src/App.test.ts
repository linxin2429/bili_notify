import { describe, expect, it } from 'vitest'
import { applyUpdate, readinessMessage } from './App'
import type { DashboardSnapshot } from './types'

const snapshot: DashboardSnapshot = {
  status: { auth_valid: false, up_count: 0, channel_count: 0, outbox_depth: 0, ready: false },
  ups: [], channels: [], deliveries: [], microsoft_logins: [], updated_at: '2026-01-01T00:00:00Z',
}

describe('dashboard state', () => {
  it('explains the first readiness blocker', () => {
    expect(readinessMessage(snapshot)).toContain('扫码登录')
  })

  it('applies a domain update without discarding other state', () => {
    const next = applyUpdate(snapshot, 'ups.updated', [{ uid: '1' }])
    expect(next?.ups).toEqual([{ uid: '1' }])
    expect(next?.channels).toEqual([])
  })
})
