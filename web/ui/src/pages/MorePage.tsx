import { NavLink } from 'react-router-dom'

const moreNav = [
  { path: '/history', label: '历史', icon: '◷' },
  { path: '/ai', label: 'AI 工作台', icon: '✦' },
  { path: '/ai-settings', label: 'AI 设置', icon: '⚒' },
  { path: '/audit-logs', label: '操作日志', icon: '≡' },
  { path: '/settings', label: '设置', icon: '⚙' },
] as const

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
            <span aria-hidden="true">{item.icon}</span>
            <span>{item.label}</span>
          </NavLink>
        ))}
      </nav>
    </section>
  )
}
