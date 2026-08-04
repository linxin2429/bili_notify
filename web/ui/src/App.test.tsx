import { describe, expect, it } from 'vitest'
import { render, screen } from '@testing-library/react'
import { composePreviewBody, DynamicHistoryCard, historyMediaURL } from './App'
import { parseDynamicHistoryPage } from './api'
import { applyBiliLoginMutation, applyUPMutation, applyUpdate, readinessMessage } from './dashboard'
import { nextRevision } from './realtime'
import type { DashboardSnapshot, DynamicHistoryItem } from './types'

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

  it('applies successful HTTP mutation payloads without waiting for realtime', () => {
    const withUP = applyUPMutation(snapshot, {
      uid: '42', name: 'tester', enabled: true, baseline_ready: false, consecutive_fail: 0,
    })
    const withQR = applyBiliLoginMutation(withUP, {
      id: 'login', status: 'waiting', expires_at: '2026-01-01T00:05:00+08:00', qr_data_url: 'data:image/png;base64,qr',
    })

    expect(withQR.ups).toHaveLength(1)
    expect(withQR.status.up_count).toBe(1)
    expect(withQR.bili_login?.qr_data_url).toBe('data:image/png;base64,qr')
  })

  it('accepts a lower snapshot revision after the server restarts', () => {
    expect(nextRevision(100, 'snapshot', 0)).toBe(0)
    expect(nextRevision(100, 'status.updated', 1)).toBeNull()
  })
})

describe('dynamic history previews', () => {
  const base: DynamicHistoryItem = {
    id: '1', uid: '42', up_name: '测试 UP', type: 'DYNAMIC_TYPE_WORD',
    published_at: '2026-01-01T00:00:00Z', discovered_at: '2026-01-01T00:00:01Z', baseline: false,
  }
  const cases: { name: string; item: DynamicHistoryItem; text: string | RegExp; images: number }[] = [
    { name: '纯文本', item: { ...base, summary: '完整的纯文本正文' }, text: '完整的纯文本正文', images: 0 },
    { name: '单图', item: { ...base, type: 'DYNAMIC_TYPE_DRAW', media: [{ kind: 'image', url: 'https://example.com/1.jpg', width: 800, height: 600 }] }, text: '1', images: 1 },
    { name: '多图', item: { ...base, type: 'DYNAMIC_TYPE_DRAW', media: [{ kind: 'image', url: 'https://example.com/1.jpg' }, { kind: 'image', url: 'https://example.com/2.jpg' }] }, text: '1', images: 2 },
    { name: '视频', item: { ...base, type: 'DYNAMIC_TYPE_AV', title: '视频标题', summary: '今天的vlog', description: '官方视频简介', media: [{ kind: 'cover', url: 'https://example.com/cover.jpg' }] }, text: /今天的vlog[\s\S]*官方视频简介/, images: 1 },
    { name: '图文混合', item: { ...base, type: 'DYNAMIC_TYPE_DRAW', description: '图片前的正文', media: [{ kind: 'image', url: 'https://example.com/3.jpg' }] }, text: '图片前的正文', images: 1 },
    { name: '无媒体降级', item: { ...base, summary: '', media: [] }, text: '（该归档没有可预览的正文或媒体）', images: 0 },
  ]

  it.each(cases)('$name', ({ item, text, images }) => {
    const { container, unmount } = render(<DynamicHistoryCard item={item} onOpen={() => undefined} />)
    expect(screen.getAllByText(text).length).toBeGreaterThan(0)
    expect(container.querySelectorAll('img')).toHaveLength(images)
    unmount()
  })

  it('keeps summary-only posts identifiable in the heading', () => {
    render(<DynamicHistoryCard item={{ ...base, summary: '只有摘要的动态' }} onOpen={() => undefined} />)
    expect(screen.getAllByText('只有摘要的动态').length).toBeGreaterThanOrEqual(1)
  })

  it('composes summary and description without dropping either field', () => {
    expect(composePreviewBody('今天的vlog', '官方视频简介')).toBe('今天的vlog\n\n官方视频简介')
    expect(composePreviewBody('同一段', '同一段')).toBe('同一段')
    expect(composePreviewBody('', '仅简介')).toBe('仅简介')
  })

  it('rewrites bilibili bfs assets to list-size thumbnails', () => {
    expect(historyMediaURL('https://i0.hdslb.com/bfs/album/demo.jpg', 240)).toBe('https://i0.hdslb.com/bfs/album/demo.jpg@240w')
    expect(historyMediaURL('https://i0.hdslb.com/bfs/album/demo.jpg@1000w_1000h', 240)).toBe('https://i0.hdslb.com/bfs/album/demo.jpg@240w')
    expect(historyMediaURL('https://example.com/photo.jpg', 240)).toBe('https://example.com/photo.jpg')
  })
})

describe('dynamic history response validation', () => {
  it.each([
    { name: 'accepts media preview', item: { id: '1', uid: '42', up_name: 'UP', type: 'DYNAMIC_TYPE_DRAW', published_at: '2026-01-01T00:00:00Z', discovered_at: '2026-01-01T00:00:01Z', baseline: false, media: [{ kind: 'image', url: 'https://example.com/1.jpg' }] }, keep: true },
    { name: 'accepts original preview', item: { id: '2', uid: '42', up_name: 'UP', type: 'DYNAMIC_TYPE_FORWARD', published_at: '2026-01-01T00:00:00Z', discovered_at: '2026-01-01T00:00:01Z', baseline: false, original: { summary: '原文' } }, keep: true },
    { name: 'drops malformed media dimensions', item: { id: '3', uid: '42', up_name: 'UP', type: 'DYNAMIC_TYPE_DRAW', published_at: '2026-01-01T00:00:00Z', discovered_at: '2026-01-01T00:00:01Z', baseline: false, media: [{ kind: 'image', url: 'https://example.com/1.jpg', width: 'wide' }] }, keep: false },
  ])('$name', ({ item, keep }) => {
    const page = parseDynamicHistoryPage({
      items: [item, { id: 'good', uid: '42', up_name: 'UP', type: 'DYNAMIC_TYPE_WORD', published_at: '2026-01-01T00:00:00Z', discovered_at: '2026-01-01T00:00:01Z', baseline: false, summary: 'ok' }],
      total: 2, limit: 20, offset: 0,
    })
    expect(page.total).toBe(2)
    expect(page.items.some(entry => entry.id === 'good')).toBe(true)
    expect(page.items.some(entry => entry.id === item.id)).toBe(keep)
  })
})
