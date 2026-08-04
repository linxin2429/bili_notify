import type { BiliLogin, Channel, DashboardSnapshot, Delivery, MicrosoftLogin, RuntimeSettings, UP } from './types'

function changed(snapshot: DashboardSnapshot): DashboardSnapshot {
  return { ...snapshot, updated_at: new Date().toISOString() }
}

function replaceBy<T>(items: T[], value: T, same: (item: T) => boolean): T[] {
  const index = items.findIndex(same)
  if (index < 0) return [...items, value]
  return items.map((item, itemIndex) => itemIndex === index ? value : item)
}

function withResources(snapshot: DashboardSnapshot, ups: UP[], channels: Channel[]): DashboardSnapshot {
  const lastSuccess = snapshot.status.last_success_at ? Date.parse(snapshot.status.last_success_at) : Number.NaN
  const staleAfter = Math.max(120, 2 * snapshot.settings.poll_interval_sec) * 1000
  const ready = snapshot.status.auth_valid
    && ups.some(item => item.enabled)
    && channels.some(item => item.enabled)
    && !snapshot.status.risk_paused_until
    && Number.isFinite(lastSuccess)
    && Date.now() - lastSuccess <= staleAfter
  return {
    ...changed(snapshot),
    ups,
    channels,
    status: { ...snapshot.status, up_count: ups.length, channel_count: channels.length, ready },
  }
}

export function applyUPMutation(snapshot: DashboardSnapshot, up: UP): DashboardSnapshot {
  return withResources(snapshot, replaceBy(snapshot.ups, up, item => item.uid === up.uid), snapshot.channels)
}

export function applyUPDeletion(snapshot: DashboardSnapshot, uid: string): DashboardSnapshot {
  return withResources(snapshot, snapshot.ups.filter(item => item.uid !== uid), snapshot.channels)
}

export function applyChannelMutation(snapshot: DashboardSnapshot, channel: Channel): DashboardSnapshot {
  return withResources(snapshot, snapshot.ups, replaceBy(snapshot.channels, channel, item => item.id === channel.id))
}

export function applyChannelDeletion(snapshot: DashboardSnapshot, id: string): DashboardSnapshot {
  return {
    ...withResources(snapshot, snapshot.ups, snapshot.channels.filter(item => item.id !== id)),
    microsoft_logins: snapshot.microsoft_logins.filter(item => item.channel_id !== id),
  }
}

export function applyBiliLoginMutation(snapshot: DashboardSnapshot, login: BiliLogin | null): DashboardSnapshot {
  return { ...changed(snapshot), bili_login: login }
}

export function applyMicrosoftLoginMutation(snapshot: DashboardSnapshot, login: MicrosoftLogin): DashboardSnapshot {
  return {
    ...changed(snapshot),
    microsoft_logins: replaceBy(snapshot.microsoft_logins, login, item => item.channel_id === login.channel_id),
  }
}

export function applyMicrosoftLoginDeletion(snapshot: DashboardSnapshot, channelID: string): DashboardSnapshot {
  return { ...changed(snapshot), microsoft_logins: snapshot.microsoft_logins.filter(item => item.channel_id !== channelID) }
}

export function applySettingsMutation(snapshot: DashboardSnapshot, settings: RuntimeSettings): DashboardSnapshot {
  return withResources({ ...snapshot, settings }, snapshot.ups, snapshot.channels)
}

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
