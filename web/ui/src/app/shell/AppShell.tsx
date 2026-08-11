import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  BellRing,
  Bot,
  Database,
  History,
  LayoutDashboard,
  LogOut,
  Moon,
  Monitor,
  MoreHorizontal,
  ScrollText,
  Send,
  Settings,
  SlidersHorizontal,
  Sun,
  type LucideIcon,
} from 'lucide-react'
import { NavLink, Outlet, useLocation } from 'react-router-dom'
import { useThemePreference } from '../../shared/ui/theme'
import { useSession } from '../../modules/session'
import { sessionAPI } from '../../shared/api/session'
import { replaceSessionState } from '../../shared/api/session-cache'
import { useConnectionState } from '../../shared/realtime/RealtimeSync'
import { IconButton } from '../../shared/ui'
import type { ConnectionState, ThemePreference } from '../../shared/api/types'
import { shellOutboxQuery } from './outbox'

type NavItem = { path: string; label: string; icon: LucideIcon; shortLabel?: string }

const primaryNav: NavItem[] = [
  { path: '/overview', label: '概览', icon: LayoutDashboard },
  { path: '/sources', label: '采集源', icon: Database },
  { path: '/channels', label: '通知渠道', icon: BellRing, shortLabel: '渠道' },
  { path: '/deliveries', label: '投递队列', icon: Send, shortLabel: '队列' },
  { path: '/history', label: '历史', icon: History },
]

const systemNav: NavItem[] = [
  { path: '/ai', label: 'AI 工作台', icon: Bot },
  { path: '/ai-settings', label: 'AI 设置', icon: SlidersHorizontal },
  { path: '/audit-logs', label: '操作日志', icon: ScrollText },
  { path: '/settings', label: '设置', icon: Settings },
]

const morePaths = ['/channels', '/ai', '/ai-settings', '/audit-logs', '/settings']
const mobilePrimaryNav: NavItem[] = [primaryNav[0], primaryNav[4], primaryNav[3], primaryNav[1]]
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
  const ItemIcon = item.icon
  return (
    <NavLink
      to={item.path}
      aria-label={item.label}
      className={({ isActive }) => `nav-item${isActive ? ' nav-item--active' : ''}`}
    >
      <ItemIcon className="nav-item__icon" aria-hidden="true" />
      <span className="nav-item__label">{item.label}</span>
      {showBadge && <span className="nav-item__badge" aria-hidden="true">{badge}</span>}
    </NavLink>
  )
}

function MobileNavEntry({ item, badge, isActive }: { item: NavItem; badge?: number; isActive?: boolean }) {
  const showBadge = typeof badge === 'number' && badge > 0
  const ItemIcon = item.icon
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
        <ItemIcon />
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
  const ThemeIcon = preference === 'dark' ? Moon : preference === 'light' ? Sun : Monitor

  return (
    <div className="app-shell">
      <header className="topbar">
        <div className="brand topbar__brand">
          <span className="brand__mark">BN</span>
          <strong>Bili Notify</strong>
        </div>
        <div className="topbar__actions">
          <IconButton label={`主题：${themeLabels[preference]}`} onPress={cycleTheme}>
            <ThemeIcon aria-hidden="true" />
          </IconButton>
          <IconButton label="退出登录" onPress={() => logout.mutate()}><LogOut aria-hidden="true" /></IconButton>
        </div>
      </header>

      <aside className="sidebar">
        <div className="brand sidebar__brand">
          <span className="brand__mark">BN</span>
          <span><strong>Bili Notify</strong><small>采集与投递管理台</small></span>
        </div>
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
        <div className="sidebar__footer">
          <small>{live ? '状态实时同步' : '通过 REST 轮询'}</small>
        </div>
      </aside>
      <aside className={`connection-state shell-connection connection-state--${live ? 'live' : 'degraded'}`} aria-label="连接状态"><span aria-hidden="true" />{connLabel}</aside>

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
        <MobileNavEntry item={{ path: '/more', label: '更多', icon: MoreHorizontal }} isActive={moreActive} />
      </nav>
    </div>
  )
}
