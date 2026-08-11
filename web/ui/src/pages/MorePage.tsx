import { BellRing, Bot, ScrollText, Settings, SlidersHorizontal, type LucideIcon } from 'lucide-react'
import { NavLink } from 'react-router-dom'

const moreNav: Array<{ path: string; label: string; icon: LucideIcon }> = [
  { path: '/channels', label: '通知渠道', icon: BellRing },
  { path: '/ai', label: 'AI 工作台', icon: Bot },
  { path: '/ai-settings', label: 'AI 设置', icon: SlidersHorizontal },
  { path: '/audit-logs', label: '操作日志', icon: ScrollText },
  { path: '/settings', label: '设置', icon: Settings },
]

export function MorePage() {
  return (
    <section className="page-stack">
      <header className="page-header">
        <div>
          <h1>更多</h1>
          <p>历史与系统功能</p>
        </div>
      </header>
      <nav aria-label="更多导航" className="more-nav">
        {moreNav.map(item => (
          <NavLink
            key={item.path}
            to={item.path}
            aria-label={item.label}
            className={({ isActive }) => `nav-item${isActive ? ' nav-item--active' : ''}`}
          >
            <item.icon className="nav-item__icon" aria-hidden="true" />
            <span>{item.label}</span>
          </NavLink>
        ))}
      </nav>
    </section>
  )
}
