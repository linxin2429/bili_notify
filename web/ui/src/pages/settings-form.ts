import type { RuntimeSettings } from '../types'

export interface RuntimeSettingsForm {
  pollSec: string
  requestRate: string
  concurrency: string
  commentEnabled: boolean
  commentTrackN: string
  commentRootPages: string
  commentReplyPages: string
  commentBatchSec: string
}

export type RuntimeSettingsParseResult = { ok: true; value: RuntimeSettings } | { ok: false; error: string }

export function parseRuntimeSettingsForm(input: RuntimeSettingsForm): RuntimeSettingsParseResult {
  const value: RuntimeSettings = {
    poll_interval_sec: Number(input.pollSec),
    request_rate: Number(input.requestRate),
    request_concurrency: Number(input.concurrency),
    comment_enabled: input.commentEnabled,
    comment_track_n: Number(input.commentTrackN),
    comment_root_pages: Number(input.commentRootPages),
    comment_reply_pages: Number(input.commentReplyPages),
    comment_batch_interval_sec: Number(input.commentBatchSec),
  }
  if (!Number.isInteger(value.poll_interval_sec) || value.poll_interval_sec < 10) return { ok: false, error: '轮询间隔至少为 10 秒的整数' }
  if (!(value.request_rate > 0 && value.request_rate <= 10)) return { ok: false, error: '请求速率必须在 (0, 10] 内' }
  if (!Number.isInteger(value.request_concurrency) || value.request_concurrency < 1 || value.request_concurrency > 16) return { ok: false, error: '并发数必须是 1 到 16 的整数' }
  if (!Number.isInteger(value.comment_track_n) || value.comment_track_n < 1 || value.comment_track_n > 50) return { ok: false, error: '评论跟踪条数必须是 1 到 50 的整数' }
  if (!Number.isInteger(value.comment_root_pages) || value.comment_root_pages < 1 || value.comment_root_pages > 10) return { ok: false, error: '根评论页数必须是 1 到 10 的整数' }
  if (!Number.isInteger(value.comment_reply_pages) || value.comment_reply_pages < 1 || value.comment_reply_pages > 20) return { ok: false, error: '子评论页数必须是 1 到 20 的整数' }
  if (!Number.isInteger(value.comment_batch_interval_sec) || value.comment_batch_interval_sec < 30) return { ok: false, error: '评论批次间隔至少为 30 秒' }
  return { ok: true, value }
}
