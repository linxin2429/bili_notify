import type { BiliLogin, Channel, DashboardSnapshot, Delivery, MicrosoftLogin, UP } from './types'

export function applyUpdate(current: DashboardSnapshot | null, event: string, data: unknown): DashboardSnapshot | null {
  if (!current) return current
  const updated = { ...current, updated_at: new Date().toISOString() }
  if (event === 'status.updated') updated.status = data as DashboardSnapshot['status']
  if (event === 'settings.updated') updated.settings = data as DashboardSnapshot['settings']
  if (event === 'ups.updated') updated.ups = data as UP[]
  if (event === 'channels.updated') updated.channels = data as Channel[]
  if (event === 'deliveries.updated') updated.deliveries = data as Delivery[]
  if (event === 'bilibili.login.updated') updated.bili_login = data as BiliLogin | null
  if (event === 'microsoft.login.updated') updated.microsoft_logins = data as MicrosoftLogin[]
  return updated
}

export function readinessMessage(snapshot: DashboardSnapshot) {
  if (!snapshot.status.auth_valid) return '请先完成 B站扫码登录。'
  if (!snapshot.channels.some(channel => channel.enabled)) return '请配置并启用至少一个通知渠道。'
  if (!snapshot.ups.some(up => up.enabled)) return '请添加并启用至少一个 UP 主。'
  if (snapshot.status.risk_paused_until) return '采集因 B站风控暂时暂停。'
  if (!snapshot.status.last_success_at) return '配置已完成，正在等待首次采集。'
  return '登录、采集目标和通知渠道均正常。'
}
