import { describe, expect, it } from 'vitest'
import { parseRuntimeSettingsForm, runtimeSettingsToForm, type RuntimeSettingsForm } from './settings-form'
import { settings } from '../test/fixtures'

const valid: RuntimeSettingsForm = runtimeSettingsToForm(settings)

describe('parseRuntimeSettingsForm', () => {
  it.each([
    { field: 'pollSec', value: '9', want: '轮询间隔' }, { field: 'pollSec', value: '86401', want: '轮询间隔' },
    { field: 'requestRate', value: '0', want: '请求速率' }, { field: 'requestRate', value: '11', want: '请求速率' },
    { field: 'concurrency', value: '0', want: '请求并发数' }, { field: 'concurrency', value: '2.5', want: '请求并发数' },
    { field: 'commentTrackN', value: '51', want: '评论跟踪条数' }, { field: 'commentBatchSec', value: '29', want: '评论间隔' },
    { field: 'auditRetentionDays', value: '0', want: '日志保留' },
    { field: 'relationRefreshSec', value: '59', want: '关注关系' }, { field: 'spaceReconcileSec', value: '299', want: '空间校验' },
    { field: 'maxDynamicPages', value: '21', want: '动态翻页' }, { field: 'riskPauseSec', value: '59', want: '风控暂停' },
    { field: 'deliveryConcurrency', value: '33', want: '投递并发' }, { field: 'backlogAlertCount', value: '0', want: '积压告警' },
    { field: 'backlogAlertAgeSec', value: '59', want: '积压告警' },
  ] as const)('rejects $field=$value', ({ field, value, want }) => {
    const result = parseRuntimeSettingsForm({ ...valid, [field]: value })
    expect(result).toMatchObject({ ok: false, error: expect.stringContaining(want) })
  })

  it.each([
    { name: 'out of range', value: ['0', '30', '120', '600', '3600'], want: '非递减整数' },
    { name: 'decreasing', value: ['5', '30', '20', '600', '3600'], want: '非递减整数' },
  ])('rejects retry delays that are $name', ({ value, want }) => {
    const result = parseRuntimeSettingsForm({ ...valid, retryDelaysSec: value as RuntimeSettingsForm['retryDelaysSec'] })
    expect(result).toMatchObject({ ok: false, error: expect.stringContaining(want) })
  })

  it('returns the complete typed runtime settings', () => {
    expect(parseRuntimeSettingsForm(valid)).toEqual({ ok: true, value: settings })
  })
})
