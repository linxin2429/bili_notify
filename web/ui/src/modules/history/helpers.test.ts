import { describe, expect, it } from 'vitest'
import type { Attachment, UnifiedContent } from '../../shared/api/types'
import {
  attachmentURL,
  avatarText,
  formatBytes,
  formatDuration,
  historyTypeLabel,
  imageAttachments,
  isContentCardType,
  isVideoLike,
  nonImageAttachments,
  originalContentURL,
  platformLabel,
  roleLabel,
  videoEmbedURL,
} from './helpers'

const base: UnifiedContent = {
  id: 'bilibili:content:d',
  platform: 'bilibili',
  source_id: 'bilibili:up:1',
  external_id: 'd',
  upstream_type: 'DYNAMIC_TYPE_WORD',
  type: 'dynamic',
  published_at: '2026-08-09T10:00:00Z',
  first_seen_at: '2026-08-09T10:00:00Z',
  last_synced_at: '2026-08-09T10:00:00Z',
  baseline: false,
}

describe('history helpers', () => {
  it('labels platforms, roles and content types', () => {
    expect(platformLabel('zsxq')).toBe('知识星球')
    expect(platformLabel('bilibili')).toBe('B 站')
    expect(roleLabel('owner')).toBe('星球主')
    expect(roleLabel('up')).toBe('UP 主')
    expect(roleLabel('unknown')).toBe('')
    expect(historyTypeLabel(base, value => value === 'DYNAMIC_TYPE_WORD' ? '文字' : value)).toBe('文字')
    expect(historyTypeLabel({ ...base, upstream_type: 'talk', type: 'discussion' }, value => value)).toBe('讨论')
    expect(historyTypeLabel({ ...base, upstream_type: 'x', type: 'video' }, value => value)).toBe('视频')
    expect(historyTypeLabel({ ...base, upstream_type: 'raw', type: 'dynamic' }, value => value)).toBe('动态')
    expect(historyTypeLabel({ ...base, upstream_type: 'custom', type: 'unknown' as UnifiedContent['type'] }, value => value)).toBe('custom')
  })

  it('formats bytes, duration and avatar text', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2.0 KiB')
    expect(formatBytes(2 * 1024 ** 2)).toBe('2.0 MiB')
    expect(formatDuration(undefined)).toBe('')
    expect(formatDuration(0)).toBe('')
    expect(formatDuration(75)).toBe('1:15')
    expect(formatDuration(3661)).toBe('1:01:01')
    expect(avatarText('测试')).toBe('测')
    expect(avatarText('  ')).toBe('·')
  })

  it('builds attachment and original content URLs safely', () => {
    const local: Attachment = { id: 'a', content_id: 'c', external_id: 'e', type: 'image', localized: true }
    const remote: Attachment = { id: 'b', content_id: 'c', external_id: 'e', type: 'file', localized: false }
    expect(attachmentURL('c/1', local)).toBe('/api/v4/contents/c%2F1/attachments/a')
    expect(attachmentURL('c', remote)).toBe('')
    expect(originalContentURL({ platform: 'bilibili', url: 'https://www.bilibili.com/video/BV1xx411c7mD' })).toContain('BV1xx411c7mD')
    expect(originalContentURL({ platform: 'zsxq', url: 'https://wx.zsxq.com/topic' })).toBe('https://wx.zsxq.com/topic')
    expect(originalContentURL({ platform: 'zsxq', url: 'javascript:alert(1)' })).toBe('')
  })

  it('classifies video/content cards and filters image attachments', () => {
    expect(isVideoLike({ type: 'video', upstream_type: 'talk' })).toBe(true)
    expect(isVideoLike({ type: 'dynamic', upstream_type: 'DYNAMIC_TYPE_AV' })).toBe(true)
    expect(isVideoLike({ type: 'dynamic', upstream_type: 'DYNAMIC_TYPE_WORD' })).toBe(false)
    expect(isContentCardType({ type: 'article', upstream_type: 'x' })).toBe(true)
    expect(isContentCardType({ type: 'dynamic', upstream_type: 'DYNAMIC_TYPE_PGC' })).toBe(true)
    expect(isContentCardType({ type: 'dynamic', upstream_type: 'DYNAMIC_TYPE_WORD' })).toBe(false)
    expect(videoEmbedURL({ platform: 'bilibili', type: 'video', upstream_type: 'DYNAMIC_TYPE_AV', url: 'https://www.bilibili.com/video/BV1xx411c7mD' })).toContain('player.bilibili.com')
    expect(videoEmbedURL({ platform: 'zsxq', type: 'video', upstream_type: 'video', url: 'https://example.com' })).toBe('')
    const attachments: Attachment[] = [
      { id: '1', content_id: 'c', external_id: '1', type: 'image', localized: true },
      { id: '2', content_id: 'c', external_id: '2', type: 'image', localized: false },
      { id: '3', content_id: 'c', external_id: '3', type: 'file', localized: true },
    ]
    expect(imageAttachments(attachments)).toHaveLength(1)
    expect(nonImageAttachments(attachments).map(item => item.id)).toEqual(['2', '3'])
  })
})
