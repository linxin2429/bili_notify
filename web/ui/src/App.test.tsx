import { describe, expect, it } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { MemoryRouter } from 'react-router-dom'
import { vi } from 'vitest'
import {
  activateNavigation, canApplyDashboardRefresh, composePreviewBody, DeliveriesPage, DynamicHistoryCard,
  formatInteractionCount, formatRelativeDate, historyMediaURL,
} from './App'
import { AdminAPI, parseDynamicHistoryPage } from './api'
import { applyBiliLoginMutation, applyUPMutation, applyUpdate, readinessMessage } from './dashboard'
import { nextRevision } from './realtime'
import type { DashboardSnapshot, Delivery, DynamicHistoryItem } from './types'

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

describe('navigation refresh', () => {
  it.each([
    { name: 'refreshes the selected page without replacing its URL', active: '/history', target: '/history', navigates: false },
    { name: 'navigates and refreshes a different page', active: '/overview', target: '/history', navigates: true },
  ])('$name', ({ active, target, navigates }) => {
    const navigate = vi.fn()
    const refresh = vi.fn()

    activateNavigation(active, target, navigate, refresh)

    expect(refresh).toHaveBeenCalledOnce()
    expect(navigate).toHaveBeenCalledTimes(navigates ? 1 : 0)
    if (navigates) expect(navigate).toHaveBeenCalledWith(target)
  })

  it.each([
    { name: 'accepts the latest unchanged request', request: 2, latestRequest: 2, version: 5, latestVersion: 5, want: true },
    { name: 'rejects an older request', request: 1, latestRequest: 2, version: 5, latestVersion: 5, want: false },
    { name: 'rejects a response older than realtime state', request: 2, latestRequest: 2, version: 4, latestVersion: 5, want: false },
  ])('$name', ({ request, latestRequest, version, latestVersion, want }) => {
    expect(canApplyDashboardRefresh(request, latestRequest, version, latestVersion)).toBe(want)
  })
})

describe('delivery retry', () => {
  const deliveries: Delivery[] = [
    { id: 'blocked', channel_id: 'channel', state: 'blocked', attempts: 1, next_at: '2026-08-05T15:00:00Z', last_error: 'upload failed', created_at: '2026-08-05T14:00:00Z' },
    { id: 'pending', channel_id: 'channel', state: 'pending', attempts: 2, next_at: '2026-08-05T15:01:00Z', created_at: '2026-08-05T14:00:00Z' },
  ]

  it('shows retry only for blocked deliveries and refreshes after acceptance', async () => {
    const api = new AdminAPI('csrf')
    const retry = vi.spyOn(api, 'retryDelivery').mockResolvedValue({ status: 'queued' })
    const refreshDashboard = vi.fn().mockResolvedValue(undefined)
    const runMutation = async <T,>(request: () => Promise<T>) => request()

    render(<MemoryRouter><DeliveriesPage deliveries={deliveries} channels={[]} total={2} api={api} runMutation={runMutation} refreshDashboard={refreshDashboard} /></MemoryRouter>)
    expect(screen.getAllByRole('button', { name: /立即重试/ })).toHaveLength(1)

    fireEvent.click(screen.getByRole('button', { name: /立即重试/ }))

    await waitFor(() => expect(retry).toHaveBeenCalledWith('blocked'))
    await waitFor(() => expect(refreshDashboard).toHaveBeenCalledOnce())
  })
})

describe('dynamic history previews', () => {
  const base: DynamicHistoryItem = {
    id: '1', uid: '42', up_name: '测试 UP', type: 'DYNAMIC_TYPE_WORD',
    published_at: '2026-01-01T00:00:00Z', discovered_at: '2026-01-01T00:00:01Z', baseline: false,
  }
  const cases: { name: string; item: DynamicHistoryItem; texts: Array<string | RegExp>; images: number }[] = [
    { name: '纯文本', item: { ...base, summary: '完整的纯文本正文' }, texts: ['完整的纯文本正文'], images: 0 },
    { name: '单图', item: { ...base, type: 'DYNAMIC_TYPE_DRAW', media: [{ kind: 'image', url: 'https://example.com/1.jpg', width: 800, height: 600 }] }, texts: [], images: 1 },
    { name: '多图', item: { ...base, type: 'DYNAMIC_TYPE_DRAW', media: [{ kind: 'image', url: 'https://example.com/1.jpg' }, { kind: 'image', url: 'https://example.com/2.jpg' }] }, texts: [], images: 2 },
    { name: '视频', item: { ...base, type: 'DYNAMIC_TYPE_AV', title: '视频标题', summary: '今天的vlog', description: '官方视频简介', media: [{ kind: 'cover', url: 'https://example.com/cover.jpg' }] }, texts: ['今天的vlog', '视频标题', '官方视频简介'], images: 1 },
    { name: '图文混合', item: { ...base, type: 'DYNAMIC_TYPE_DRAW', description: '图片前的正文', media: [{ kind: 'image', url: 'https://example.com/3.jpg' }] }, texts: ['图片前的正文'], images: 1 },
    { name: '无媒体降级', item: { ...base, summary: '', media: [] }, texts: ['（该归档没有可预览的正文或媒体）'], images: 0 },
  ]

  it.each(cases)('$name', ({ item, texts, images }) => {
    const { container, getAllByText } = render(<DynamicHistoryCard item={item} />)
    for (const text of texts) expect(getAllByText(text).length).toBeGreaterThan(0)
    expect(container.querySelectorAll('img')).toHaveLength(images)
  })

  it('shows summary-only posts as copyable body text without a clickable card', () => {
    const { container, getAllByText } = render(<DynamicHistoryCard item={{ ...base, summary: '只有摘要的动态' }} />)
    expect(getAllByText('只有摘要的动态')).toHaveLength(1)
    expect(container.querySelector('.history-copyable')).toBeInTheDocument()
    expect(container.querySelector('.history-card')).not.toHaveAttribute('role', 'button')
  })

  it('uses the content landing URL and renders archived interaction data', () => {
    render(<DynamicHistoryCard item={{
      ...base, url: 'https://t.bilibili.com/1', target_url: 'https://www.bilibili.com/video/BV1',
      stats: { forwards: 0, comments: 572, likes: 12_500 },
    }} />)
    expect(screen.getByRole('link', { name: /查看原内容/ })).toHaveAttribute('href', 'https://www.bilibili.com/video/BV1')
    expect(screen.getByText('转发')).toBeVisible()
    expect(screen.getByText('572')).toBeVisible()
    expect(screen.getByText('1.3万')).toBeVisible()
  })

  it('opens a group lightbox and supports keyboard navigation and Escape close', async () => {
    render(<DynamicHistoryCard item={{
      ...base, type: 'DYNAMIC_TYPE_DRAW', media: [
        { kind: 'image', url: 'https://example.com/1.jpg' },
        { kind: 'image', url: 'https://example.com/2.jpg' },
      ],
    }} />)
    fireEvent.click(screen.getByRole('button', { name: '放大第 1 张动态图片' }))
    const dialog = screen.getByRole('dialog', { name: '图片预览' })
    expect(screen.getByAltText('预览第 1 张图片')).toHaveAttribute('src', 'https://example.com/1.jpg')
    fireEvent.keyDown(dialog, { key: 'ArrowRight' })
    const secondImage = screen.getByAltText('预览第 2 张图片')
    expect(secondImage).toHaveAttribute('src', 'https://example.com/2.jpg')
    fireEvent.error(secondImage)
    expect(screen.getByText('图片加载失败')).toBeVisible()
    expect(screen.getByRole('button', { name: '上一张图片' })).toBeEnabled()
    fireEvent.keyDown(dialog, { key: 'Escape' })
    await waitFor(() => expect(screen.queryByRole('dialog', { name: '图片预览' })).not.toBeInTheDocument())
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
    expect(historyMediaURL('/api/v1/dynamics/10/media/0', 240)).toBe('/api/v1/dynamics/10/media/0')
  })

  it.each([
    { name: 'zero uses the action label', value: 0, label: '点赞', want: '点赞' },
    { name: 'small values stay exact', value: 3980, label: '点赞', want: '3,980' },
    { name: 'large values use wan units', value: 12_500, label: '点赞', want: '1.3万' },
  ])('$name', ({ value, label, want }) => {
    expect(formatInteractionCount(value, label)).toBe(want)
  })

  it.each([
    { name: 'just now', value: '2026-08-05T12:00:00Z', now: '2026-08-05T12:00:30Z', want: '刚刚' },
    { name: 'minutes ago', value: '2026-08-05T11:23:00Z', now: '2026-08-05T12:00:00Z', want: '37分钟前' },
    { name: 'hours ago', value: '2026-08-05T08:00:00Z', now: '2026-08-05T12:00:00Z', want: '4小时前' },
  ])('$name', ({ value, now, want }) => {
    expect(formatRelativeDate(value, new Date(now).valueOf())).toBe(want)
  })
})

describe('dynamic history response validation', () => {
  it.each([
    { name: 'accepts media preview', item: { id: '1', uid: '42', up_name: 'UP', type: 'DYNAMIC_TYPE_DRAW', published_at: '2026-01-01T00:00:00Z', discovered_at: '2026-01-01T00:00:01Z', baseline: false, media: [{ kind: 'image', url: 'https://example.com/1.jpg' }] }, keep: true },
    { name: 'accepts original preview', item: { id: '2', uid: '42', up_name: 'UP', type: 'DYNAMIC_TYPE_FORWARD', published_at: '2026-01-01T00:00:00Z', discovered_at: '2026-01-01T00:00:01Z', baseline: false, original: { summary: '原文' } }, keep: true },
    { name: 'accepts stats and video metadata', item: { id: '4', uid: '42', up_name: 'UP', type: 'DYNAMIC_TYPE_AV', published_at: '2026-01-01T00:00:00Z', discovered_at: '2026-01-01T00:00:01Z', baseline: false, stats: { forwards: 1, comments: 2, likes: 3 }, video: { duration: '01:09', views: '8468' } }, keep: true },
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
