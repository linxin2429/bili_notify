import { describe, expect, it } from 'vitest'
import {
  auditActionLabel, auditResult, channelTypeLabel, composePreviewBody, connectionLabel, deliverySummary,
  deliveryTitle, dynamicTypeLabel, errorMessage, followStateLabel, formatDate, formatInteractionCount,
  formatRelativeDate, historyMediaURL, localInputToRFC3339, loginLabel, nextTheme, normalizePreviewText,
  settingLabel, themeLabel, usableTimeZone,
} from './presentation'
import { makeAudit, makeDelivery } from './test/fixtures'

describe('presentation helpers', () => {
  it.each([
    ['followed', '当前账号已关注'], ['unfollowed', '当前账号未关注'], ['unknown', '关注关系未知'],
  ] as const)('labels follow state %s', (input, want) => expect(followStateLabel(input)).toBe(want))

  it.each([
    ['connecting', '重连中'], ['reconnecting', '重连中'], ['live', '实时'], ['stale', '数据过期'],
  ] as const)('labels connection %s', (input, want) => expect(connectionLabel(input)).toBe(want))

  it.each([
    ['system', 'light'], ['light', 'dark'], ['dark', 'system'],
  ] as const)('cycles theme %s', (input, want) => expect(nextTheme(input)).toBe(want))

  it('labels known and unknown values', () => {
    expect(themeLabel('dark')).toBe('深色'); expect(themeLabel('system')).toBe('跟随系统'); expect(themeLabel('light')).toBe('浅色')
    expect(channelTypeLabel('feishu')).toBe('飞书机器人'); expect(settingLabel('webhook')).toBe('Webhook'); expect(settingLabel('custom')).toBe('custom')
    expect(loginLabel('scanned')).toBe('已扫码，请确认'); expect(loginLabel('custom')).toBe('custom')
    expect(dynamicTypeLabel('DYNAMIC_TYPE_AV')).toBe('视频'); expect(dynamicTypeLabel('CUSTOM')).toBe('CUSTOM'); expect(dynamicTypeLabel('')).toBe('内容')
    expect(auditActionLabel('channel.update')).toBe('修改通知渠道'); expect(auditActionLabel('custom')).toBe('custom')
  })

  it.each([
    { value: 0, want: '点赞' }, { value: Number.NaN, want: '点赞' }, { value: 3980, want: '3,980' },
    { value: 12_500, want: '1.3万' }, { value: 2_000_000, want: '200万' }, { value: 120_000_000, want: '1.2亿' },
  ])('formats interaction $value', ({ value, want }) => expect(formatInteractionCount(value, '点赞')).toBe(want))

  it.each([
    { value: '2026-08-05T12:00:00Z', now: '2026-08-05T12:00:30Z', want: '刚刚' },
    { value: '2026-08-05T11:23:00Z', now: '2026-08-05T12:00:00Z', want: '37分钟前' },
    { value: '2026-08-05T08:00:00Z', now: '2026-08-05T12:00:00Z', want: '4小时前' },
    { value: '2026-08-03T12:00:00Z', now: '2026-08-05T12:00:00Z', want: '2天前' },
    { value: 'bad', now: '2026-08-05T12:00:00Z', want: '—' },
  ])('formats relative date $want', ({ value, now, want }) => expect(formatRelativeDate(value, new Date(now).valueOf())).toBe(want))

  it('formats absolute dates with explicit valid timezones', () => {
    expect(usableTimeZone('Asia/Shanghai')).toBe('Asia/Shanghai'); expect(usableTimeZone('UTC+8')).toBe(''); expect(usableTimeZone('bad/zone')).toBe('')
    expect(formatDate('bad')).toBe('—'); expect(formatDate('2026-08-06T00:00:00Z', 'Asia/Shanghai')).toContain('08:00:00')
    expect(localInputToRFC3339('')).toBe(''); expect(localInputToRFC3339('bad')).toBe('bad'); expect(localInputToRFC3339('2026-08-06T00:00')).toMatch(/^2026-08-0[56]T(00|16):00:00/)
  })

  it('normalizes preview bodies and media URLs', () => {
    expect(composePreviewBody()).toBe(''); expect(composePreviewBody('正文')).toBe('正文'); expect(composePreviewBody('同 一段', '同   一段')).toBe('同 一段'); expect(composePreviewBody('摘要', '简介')).toBe('摘要\n\n简介')
    expect(normalizePreviewText(' a \n b ')).toBe('a b')
    expect(historyMediaURL('https://i0.hdslb.com/bfs/a.jpg@100w', 240)).toBe('https://i0.hdslb.com/bfs/a.jpg@240w')
    expect(historyMediaURL('https://example.com/a.jpg', 240)).toBe('https://example.com/a.jpg'); expect(historyMediaURL('/api/v1/dynamics/1/media/0', 240)).toBe('/api/v1/dynamics/1/media/0'); expect(historyMediaURL(' ', 240)).toBe(''); expect(historyMediaURL('bad url', 0)).toBe('bad url')
  })

  it('formats delivery and audit variants', () => {
    expect(deliveryTitle(makeDelivery())).toBe('delivery'); expect(deliverySummary(makeDelivery())).toBe('')
    const comment = makeDelivery({ kind: 'comment', comment: { rpid: '1', up_uid: '42', up_name: '', content_type: 'video', content_id: 'BV', content_url: 'https://example.com', published_at: '2026-08-06T00:00:00Z' } })
    expect(deliveryTitle(comment)).toBe('42 · 评论回复'); expect(deliverySummary(comment)).toBe('https://example.com')
    expect(auditResult(makeAudit()).label).toBe('成功'); expect(auditResult(makeAudit({ outcome: 'denied' })).label).toBe('已拒绝'); expect(auditResult(makeAudit({ outcome: 'failure' })).label).toBe('失败')
    expect(errorMessage(new Error('broken'))).toBe('broken'); expect(errorMessage('broken')).toBe('发生未知错误')
  })
})
