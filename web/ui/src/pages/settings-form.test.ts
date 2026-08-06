import { describe, expect, it } from 'vitest'
import { parseRuntimeSettingsForm, type RuntimeSettingsForm } from './settings-form'

const valid: RuntimeSettingsForm = { pollSec: '30', requestRate: '2', concurrency: '4', commentEnabled: true, commentTrackN: '10', commentRootPages: '2', commentReplyPages: '5', commentBatchSec: '120' }

describe('parseRuntimeSettingsForm', () => {
  it.each([
    { field: 'pollSec', value: '9', want: '轮询间隔' }, { field: 'pollSec', value: '10.5', want: '轮询间隔' },
    { field: 'requestRate', value: '0', want: '请求速率' }, { field: 'requestRate', value: '11', want: '请求速率' },
    { field: 'concurrency', value: '0', want: '并发数' }, { field: 'concurrency', value: '2.5', want: '并发数' },
    { field: 'commentTrackN', value: '51', want: '评论跟踪条数' }, { field: 'commentRootPages', value: '0', want: '根评论页数' },
    { field: 'commentReplyPages', value: '21', want: '子评论页数' }, { field: 'commentBatchSec', value: '29', want: '评论批次间隔' },
  ])('rejects $field=$value', ({ field, value, want }) => {
    const result = parseRuntimeSettingsForm({ ...valid, [field]: value })
    expect(result).toMatchObject({ ok: false, error: expect.stringContaining(want) })
  })

  it('returns the typed runtime settings', () => {
    expect(parseRuntimeSettingsForm(valid)).toEqual({ ok: true, value: { poll_interval_sec: 30, request_rate: 2, request_concurrency: 4, comment_enabled: true, comment_track_n: 10, comment_root_pages: 2, comment_reply_pages: 5, comment_batch_interval_sec: 120 } })
  })
})
