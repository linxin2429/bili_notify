import type { AuditLog, ChannelType, ConnectionState, Delivery, ThemePreference } from './types'

export function followStateLabel(state: 'unknown' | 'followed' | 'unfollowed') {
  if (state === 'followed') return '当前账号已关注'
  if (state === 'unfollowed') return '当前账号未关注'
  return '关注关系未知'
}

export function connectionLabel(state: ConnectionState) {
  if (state === 'live') return '实时'
  if (state === 'connecting' || state === 'reconnecting') return '重连中'
  return '数据过期'
}

export function nextTheme(value: ThemePreference): ThemePreference {
  return value === 'system' ? 'light' : value === 'light' ? 'dark' : 'system'
}

export function themeLabel(value: ThemePreference) {
  return value === 'system' ? '跟随系统' : value === 'light' ? '浅色' : '深色'
}

export function channelTypeLabel(value: ChannelType) {
  return ({ email: 'SMTP 邮件', microsoft: 'Microsoft Graph', dingtalk: '钉钉机器人', feishu: '飞书机器人', wecom: '企业微信机器人' })[value]
}

export function settingLabel(value: string) {
  return ({ host: '主机', port: '端口', tls: 'TLS', from: '发件人', to: '收件人', username: '用户名', password: '密码', webhook: 'Webhook', secret: '签名密钥', client_id: '客户端 ID', tenant: '租户', access_token: '访问令牌', refresh_token: '刷新令牌', app_id: '应用 App ID', app_secret: '应用 App Secret' } as Record<string, string>)[value] || value
}

export function loginLabel(value: string) {
  return ({ waiting: '等待扫码', scanned: '已扫码，请确认', success: '登录成功', expired: '二维码已过期' } as Record<string, string>)[value] || value
}

export function deliveryTitle(delivery: Delivery) {
  if (delivery.kind === 'comment' && delivery.comment) return `${delivery.comment.up_name || delivery.comment.up_uid} · 评论回复`
  return delivery.dynamic?.up_name || delivery.dynamic?.uid || delivery.id
}

export function deliverySummary(delivery: Delivery) {
  if (delivery.kind === 'comment' && delivery.comment) return delivery.comment.content_title || delivery.comment.content_url || `评论 ${delivery.comment.rpid}`
  return delivery.dynamic?.summary || ''
}

export function usableTimeZone(value?: string) {
  if (!value || value === 'Local' || value.startsWith('UTC')) return ''
  try {
    new Intl.DateTimeFormat('zh-CN', { timeZone: value }).format(new Date())
    return value
  } catch {
    return ''
  }
}

export function formatDate(value: string, timeZone = '') {
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'short',
    timeStyle: 'medium',
    ...(usableTimeZone(timeZone) ? { timeZone } : {}),
  }).format(date)
}

export function formatRelativeDate(value: string, now = Date.now(), timeZone = '') {
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return '—'
  const elapsed = now - date.valueOf()
  if (elapsed < 0 || elapsed >= 7 * 24 * 60 * 60 * 1000) return formatDate(value, timeZone)
  if (elapsed < 60 * 1000) return '刚刚'
  if (elapsed < 60 * 60 * 1000) return `${Math.floor(elapsed / (60 * 1000))}分钟前`
  if (elapsed < 24 * 60 * 60 * 1000) return `${Math.floor(elapsed / (60 * 60 * 1000))}小时前`
  return `${Math.floor(elapsed / (24 * 60 * 60 * 1000))}天前`
}

export function formatInteractionCount(value: number, emptyLabel: string) {
  if (!Number.isFinite(value) || value <= 0) return emptyLabel
  if (value < 10_000) return Math.floor(value).toLocaleString('zh-CN')
  const scaled = value < 100_000_000 ? value / 10_000 : value / 100_000_000
  const suffix = value < 100_000_000 ? '万' : '亿'
  return `${scaled.toFixed(scaled >= 100 ? 0 : 1).replace(/\.0$/, '')}${suffix}`
}

export function normalizePreviewText(value: string) {
  return value.trim().replace(/\s+/g, ' ')
}

export function composePreviewBody(summary?: string, description?: string) {
  const parts = [summary, description].map(value => (value || '').trim()).filter(Boolean)
  if (parts.length === 0) return ''
  if (parts.length === 1 || normalizePreviewText(parts[0]) === normalizePreviewText(parts[1])) return parts[0]
  return parts.join('\n\n')
}

export function historyMediaURL(url: string, width: number) {
  const value = url.trim()
  if (!value || width <= 0 || value.startsWith('/api/v1/dynamics/')) return value
  try {
    const parsed = new URL(value, 'https://www.bilibili.com')
    if (!/(^|\.)hdslb\.com$/i.test(parsed.hostname) || !parsed.pathname.includes('/bfs/')) return value
    parsed.pathname = parsed.pathname.replace(/@[^/]*$/, '') + `@${Math.round(width)}w`
    return parsed.toString()
  } catch {
    return value
  }
}

export function localInputToRFC3339(value: string) {
  if (!value) return ''
  const date = new Date(value)
  return Number.isNaN(date.valueOf()) ? value : date.toISOString()
}

export function dynamicTypeLabel(value: string) {
  return ({
    DYNAMIC_TYPE_WORD: '文字', DYNAMIC_TYPE_DRAW: '图文', DYNAMIC_TYPE_AV: '视频',
    DYNAMIC_TYPE_ARTICLE: '专栏', DYNAMIC_TYPE_FORWARD: '转发', DYNAMIC_TYPE_PGC: '番剧',
    DYNAMIC_TYPE_COMMON_SQUARE: '通用卡片',
  } as Record<string, string>)[value] || value || '内容'
}

export function auditActionLabel(action: string) {
  const labels: Record<string, string> = {
    'auth.setup': '初始化管理员', 'auth.login': '管理员登录', 'auth.logout': '管理员退出', 'auth.password.change': '修改管理员密码',
    'up.create': '添加 UP 主', 'up.update': '修改 UP 主', 'up.delete': '删除 UP 主',
    'channel.create': '添加通知渠道', 'channel.update': '修改通知渠道', 'channel.delete': '删除通知渠道', 'channel.test': '测试通知渠道',
    'delivery.retry': '重试投递', 'bilibili.login.start': '开始 B 站登录', 'bilibili.login.cancel': '取消 B 站登录',
    'microsoft.login.start': '开始 Microsoft 授权', 'microsoft.login.cancel': '取消 Microsoft 授权', 'settings.update': '修改采集参数',
  }
  return labels[action] || action
}

export function auditResult(item: AuditLog) {
  if (item.outcome === 'success') return { label: '成功', color: 'success' as const }
  if (item.outcome === 'denied') return { label: '已拒绝', color: 'warning' as const }
  return { label: '失败', color: 'error' as const }
}

export function errorMessage(error: unknown) {
  return error instanceof Error ? error.message : '发生未知错误'
}
