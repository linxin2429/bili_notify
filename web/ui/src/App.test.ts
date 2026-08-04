import { describe, expect, it } from 'vitest'
import { applyUpdate, readinessMessage } from './dashboard'
import type { DashboardSnapshot } from './types'

const snapshot: DashboardSnapshot = {
  status: { auth_valid: false, up_count: 0, channel_count: 0, outbox_depth: 0, ready: false },
  settings: {
    poll_interval_sec: 30, request_rate: 2, request_concurrency: 4,
    comment_enabled: true, comment_track_n: 10, comment_root_pages: 2,
    comment_reply_pages: 5, comment_batch_interval_sec: 120,
  },
  ups: [], channels: [], deliveries: [], microsoft_logins: [], timezone: 'Asia/Shanghai', updated_at: '2026-01-01T00:00:00+08:00',
}

describe('dashboard state', () => {
  it('explains the first readiness blocker', () => {
    expect(readinessMessage(snapshot)).toContain('扫码登录')
  })

  it('applies a domain update without discarding other state', () => {
    const next = applyUpdate(snapshot, 'ups.updated', [{ uid: '1' }])
    expect(next?.ups).toEqual([{ uid: '1' }])
    expect(next?.channels).toEqual([])
    expect(next?.settings.poll_interval_sec).toBe(30)
  })

  it('applies settings updates', () => {
    const next = applyUpdate(snapshot, 'settings.updated', {
      poll_interval_sec: 45,
      request_rate: 1.5,
      request_concurrency: 3,
      comment_enabled: false,
      comment_track_n: 5,
      comment_root_pages: 1,
      comment_reply_pages: 3,
      comment_batch_interval_sec: 60,
    })
    expect(next?.settings).toEqual({
      poll_interval_sec: 45,
      request_rate: 1.5,
      request_concurrency: 3,
      comment_enabled: false,
      comment_track_n: 5,
      comment_root_pages: 1,
      comment_reply_pages: 3,
      comment_batch_interval_sec: 60,
    })
    expect(next?.status.ready).toBe(false)
  })
})
