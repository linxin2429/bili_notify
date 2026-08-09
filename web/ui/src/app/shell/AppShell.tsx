import { useMutation, useQueryClient } from '@tanstack/react-query'
import { NavLink, Outlet } from 'react-router-dom'
import { useThemePreference } from '../theme'
import { useSession } from '../../modules/session/session'
import { sessionAPI } from '../../shared/api/session'
import { queryKeys } from '../../shared/api/query-keys'
import { useConnectionState } from '../../shared/realtime/RealtimeSync'
import { Badge, IconButton } from '../../shared/ui'
import type { ThemePreference } from '../../types'

const navigation = [
  { path: '/overview', label: '概览', icon: '◫' }, { path: '/ups', label: 'UP 主', icon: '♟' },
  { path: '/channels', label: '通知渠道', icon: '◉' }, { path: '/deliveries', label: '投递队列', icon: '↗' },
  { path: '/history', label: '历史', icon: '◷' }, { path: '/audit-logs', label: '操作日志', icon: '≡' },
  { path: '/settings', label: '设置', icon: '⚙' },
]
const themes: ThemePreference[] = ['system', 'light', 'dark']
const themeLabels: Record<ThemePreference, string> = { system: '跟随系统', light: '亮色', dark: '暗色' }

export function AppShell() {
  const { csrf } = useSession()
  const { preference, setPreference } = useThemePreference()
  const connection = useConnectionState()
  const client = useQueryClient()
  const logout = useMutation({
    mutationFn: () => sessionAPI.logout(csrf),
    onSettled: () => { client.removeQueries(); client.setQueryData(queryKeys.session, { setup_required: false, authenticated: false }) },
  })
  const cycleTheme = () => setPreference(themes[(themes.indexOf(preference) + 1) % themes.length])
  const live = connection === 'live'
  return <div className="app-shell">
    <header className="topbar"><div className="brand"><span className="brand__mark">BN</span><strong>Bili Notify</strong></div><div className="topbar__actions"><Badge tone={live ? 'success' : 'warning'}>{live ? '实时' : 'REST 轮询'}</Badge><IconButton label={`主题：${themeLabels[preference]}`} onPress={cycleTheme}>{preference === 'dark' ? '☾' : preference === 'light' ? '☀' : '◐'}</IconButton><IconButton label="退出登录" onPress={() => logout.mutate()}>⇥</IconButton></div></header>
    <aside className="sidebar"><nav aria-label="主导航">{navigation.map(item => <NavLink key={item.path} to={item.path} className={({ isActive }) => `nav-item${isActive ? ' nav-item--active' : ''}`}><span aria-hidden="true">{item.icon}</span><span>{item.label}</span></NavLink>)}</nav></aside>
    <main className="main-content">{!live && <div className="degraded-banner" role="status">实时连接已中断，管理台继续通过 REST 获取权威状态。</div>}<div className="page-container"><Outlet /></div></main>
    <nav className="mobile-nav" aria-label="移动端主导航">{navigation.slice(0, 5).map(item => <NavLink key={item.path} to={item.path} className={({ isActive }) => `mobile-nav__item${isActive ? ' mobile-nav__item--active' : ''}`}><span aria-hidden="true">{item.icon}</span><small>{item.label}</small></NavLink>)}</nav>
  </div>
}
