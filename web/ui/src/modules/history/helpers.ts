import type { Attachment, UnifiedContent } from '../../shared/api/types'
import { bilibiliPlayerEmbedURL, safeBilibiliURL, safeHTTPURL } from '../../shared/lib/presentation'

export function platformLabel(platform: string) {
  return platform === 'zsxq' ? '知识星球' : 'B 站'
}

export function roleLabel(role?: string) {
  return ({ owner: '星球主', admin: '管理员', guest: '嘉宾', partner: '合伙人', up: 'UP 主' } as Record<string, string>)[role || ''] || ''
}

/** Prefer Bilibili dynamic-type labels; fall back to unified content type. */
export function historyTypeLabel(item: UnifiedContent, dynamicTypeLabel: (value: string) => string) {
  const upstream = dynamicTypeLabel(item.upstream_type)
  if (upstream && upstream !== item.upstream_type) return upstream
  return ({
    dynamic: '动态', video: '视频', article: '专栏', discussion: '讨论',
    question: '提问', answer: '回答', task: '作业', long_article: '长文',
  } as Record<string, string>)[item.type] || item.upstream_type || item.type || '内容'
}

export function formatBytes(value: number) {
  if (value < 1024) return `${value} B`
  if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`
  return `${(value / 1024 ** 2).toFixed(1)} MiB`
}

export function formatDuration(seconds?: number) {
  if (!seconds || seconds <= 0) return ''
  const total = Math.floor(seconds)
  const h = Math.floor(total / 3600)
  const m = Math.floor((total % 3600) / 60)
  const s = total % 60
  if (h > 0) return `${h}:${String(m).padStart(2, '0')}:${String(s).padStart(2, '0')}`
  return `${m}:${String(s).padStart(2, '0')}`
}

export function avatarText(value: string) {
  return Array.from(value.trim())[0] || '·'
}

/** Local attachment download path; empty when not localized. */
export function attachmentURL(contentID: string, attachment: Attachment) {
  if (!attachment.local_path) return ''
  return `/api/v3/contents/${encodeURIComponent(contentID)}/attachments/${encodeURIComponent(attachment.id)}`
}

export function originalContentURL(item: Pick<UnifiedContent, 'platform' | 'url'>) {
  if (item.platform === 'bilibili') return safeBilibiliURL(item.url) || safeHTTPURL(item.url)
  return safeHTTPURL(item.url)
}

export function isVideoLike(item: Pick<UnifiedContent, 'type' | 'upstream_type'>) {
  return item.type === 'video' || item.upstream_type === 'DYNAMIC_TYPE_AV'
}

export function isContentCardType(item: Pick<UnifiedContent, 'type' | 'upstream_type'>) {
  if (item.type === 'video' || item.type === 'article' || item.type === 'long_article') return true
  return ['DYNAMIC_TYPE_AV', 'DYNAMIC_TYPE_ARTICLE', 'DYNAMIC_TYPE_PGC', 'DYNAMIC_TYPE_COMMON_SQUARE'].includes(item.upstream_type)
}

export function videoEmbedURL(item: Pick<UnifiedContent, 'platform' | 'url' | 'type' | 'upstream_type'>) {
  if (item.platform !== 'bilibili' || !isVideoLike(item)) return ''
  return bilibiliPlayerEmbedURL(item.url)
}

export function imageAttachments(attachments: Attachment[]) {
  return attachments.filter(item => item.type === 'image' && item.local_path)
}

export function nonImageAttachments(attachments: Attachment[]) {
  return attachments.filter(item => item.type !== 'image' || !item.local_path)
}
