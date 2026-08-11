import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useThemePreference } from '../../shared/ui/theme'
import { useSession } from '../../modules/session'
import { sessionAPI } from '../../shared/api/session'
import { replaceSessionState } from '../../shared/api/session-cache'
import { useConnectionState } from '../../shared/realtime/RealtimeSync'
import { Badge, IconButton } from '../../shared/ui'
import type { ConnectionState, ThemePreference } from '../../shared/api/types'
import { shellOutboxQuery } from './outbox'

type NavItem = { path: string; label: string; icon: string; shortLabel?: string }

const primaryNav: NavItem[] = [
  { path: '/overview', label: '概览', icon: '◫' },
  { path: '/sources', label: '采集源', icon: '♟' },
  { path: '/channels', label: '通知渠道', icon: '◉', shortLabel: '渠道' },
  { path: '/deliveries', label: '投递队列', icon: '↗', shortLabel: '队列' },
  { path: '/history', label: '历史', icon: '◷' },
]

const systemNav: NavItem[] = [
  { path: '/ai', label: 'AI 工作台', icon: '✦' },
  { path: '/ai-settings', label: 'AI 设置', icon: '⚒' },
  { path: '/audit-logs', label: '操作日志', icon: '≡' },
  { path: '/settings', label: '设置', icon: '⚙' },
]

const morePaths = ['/history', '/ai', '/ai-settings', '/audit-logs', '/settings']
const mobilePrimaryNav: NavItem[] = primaryNav.filter(item => item.path !== '/history')
const themes: ThemePreference[] = ['system', 'light', 'dark']
const themeLabels: Record<ThemePreference, string> = { system: '跟随系统', light: '亮色', dark: '暗色' }

function pathMatches(pathname: string, path: string) {
  return pathname === path || pathname.startsWith(`${path}/`)
}

function connectionLabel(state: ConnectionState) {
  if (state === 'live') return '实时'
  if (state === 'connecting' || state === 'reconnecting') return '重连中'
  return 'REST 轮询'
}

function NavEntry({ item, badge }: { item: NavItem; badge?: number }) {
  const showBadge = typeof badge === 'number' && badge > 0
  return (
    <NavLink
      to={item.path}
      aria-label={item.label}
      className={({ isActive }) => `nav-item${isActive ? ' nav-item--active' : ''}`}
    >
      <span aria-hidden="true">{item.icon}</span>
      <span className="nav-item__label">{item.label}</span>
      {showBadge && <span className="nav-item__badge" aria-hidden="true">{badge}</span>}
    </NavLink>
  )
}

function MobileNavEntry({ item, badge, isActive }: { item: NavItem; badge?: number; isActive?: boolean }) {
  const showBadge = typeof badge === 'number' && badge > 0
  return (
    <NavLink
      to={item.path}
      aria-label={item.label}
      className={({ isActive: routeActive }) => {
        const active = isActive ?? routeActive
        return `mobile-nav__item${active ? ' mobile-nav__item--active' : ''}`
      }}
    >
      <span className="mobile-nav__icon" aria-hidden="true">
        {item.icon}
        {showBadge && <span className="mobile-nav__badge">{badge}</span>}
      </span>
      <small>{item.shortLabel || item.label}</small>
    </NavLink>
  )
}

export function AppShell() {
  const { csrf } = useSession()
  const { preference, setPreference } = useThemePreference()
  const connection = useConnectionState()
  const client = useQueryClient()
  const location = useLocation()
  const runtime = useQuery(shellOutboxQuery())
  const outboxDepth = runtime.data ?? 0
  const logout = useMutation({
    mutationFn: () => sessionAPI.logout(csrf),
    onSettled: () => replaceSessionState(client, { setup_required: false, authenticated: false }),
  })
  const cycleTheme = () => setPreference(themes[(themes.indexOf(preference) + 1) % themes.length])
  const live = connection === 'live'
  const connLabel = connectionLabel(connection)
  const moreActive = location.pathname === '/more' || morePaths.some(path => pathMatches(location.pathname, path))

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand">
          <span className="brand__mark">BN</span>
          <strong>Bili Notify</strong>
        </div>
        <div className="topbar__actions">
          <Badge tone={live ? 'success' : 'warning'}>{connLabel}</Badge>
          <IconButton label={`主题：${themeLabels[preference]}`} onPress={cycleTheme}>
            {preference === 'dark' ? '☾' : preference === 'light' ? '☀' : '◐'}
          </IconButton>
          <IconButton label="退出登录" onPress={() => logout.mutate()}>⇥</IconButton>
        </div>
      </header>

      <aside className="sidebar">
        <nav aria-label="主导航">
          <div className="nav-section">
            {primaryNav.map(item => (
              <NavEntry
                key={item.path}
                item={item}
                badge={item.path === '/deliveries' ? outboxDepth : undefined}
              />
            ))}
          </div>
          <div className="nav-section">
            <div className="nav-section__label">系统</div>
            {systemNav.map(item => (
              <NavEntry key={item.path} item={item} />
            ))}
          </div>
        </nav>
      </aside>

      <main className="main-content">
        {!live && (
          <div className="degraded-banner" role="status">
            实时连接已中断，管理台继续通过 REST 获取权威状态。
          </div>
        )}
        <div className="page-container">
          <Outlet />
        </div>
      </main>

      <nav className="mobile-nav" aria-label="移动端主导航">
        {mobilePrimaryNav.map(item => (
          <MobileNavEntry
            key={item.path}
            item={item}
            badge={item.path === '/deliveries' ? outboxDepth : undefined}
          />
        ))}
        <MobileNavEntry item={{ path: '/more', label: '更多', icon: '⋯' }} isActive={moreActive} />
      </nav>
    </div>
  )
}
