import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SessionProvider } from '../modules/session'
import { ThemeProvider } from '../shared/ui/theme'
import { NotificationProvider } from '../shared/ui'
import { settings, makeAudit, makeDelivery } from '../test/fixtures'
import { OverviewPage } from './OverviewPage'
import { UPsPage } from './UPsPage'
import { ChannelsPage } from './ChannelsPage'
import { DeliveriesPage } from './DeliveriesPage'
import { HistoryPage } from './HistoryPage'
import { AuditLogsPage } from './AuditLogsPage'
import { SettingsPage } from './SettingsPage'

const api = vi.hoisted(() => ({
  runtime: vi.fn(), settings: vi.fn(), ups: vi.fn(), channels: vi.fn(), deliveries: vi.fn(), biliLogin: vi.fn(), microsoftLogins: vi.fn(), dynamics: vi.fn(), comments: vi.fn(), comment: vi.fn(), auditLogs: vi.fn(),
  createUP: vi.fn(), updateUP: vi.fn(), deleteUP: vi.fn(), createChannel: vi.fn(), updateChannel: vi.fn(), deleteChannel: vi.fn(), testChannel: vi.fn(), retryDelivery: vi.fn(), startBiliLogin: vi.fn(), cancelBiliLogin: vi.fn(), startMicrosoftLogin: vi.fn(), cancelMicrosoftLogin: vi.fn(), updateSettings: vi.fn(),
}))
vi.mock('../shared/api/resources', () => ({ resources: api }))

const runtime = { status: { auth_valid: true, bili_account: { uid: '1', name: '测试账号' }, up_count: 1, channel_count: 1, outbox_depth: 1, ready: true }, timezone: 'Asia/Shanghai', updated_at: '2026-08-09T10:00:00Z' }
const up = { uid: '42', name: '测试 UP', enabled: true, baseline_ready: true, consecutive_fail: 0, follow_state: 'followed' as const, collection_route: 'feed_all' as const }
const channel = { id: 'channel', name: '邮件', type: 'email' as const, enabled: true, settings: { host: 'smtp.example.com' }, configured_secrets: ['password'], created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' }
const dynamic = { id: 'dynamic', uid: '42', up_name: '测试 UP', type: 'DYNAMIC_TYPE_WORD', published_at: '2026-08-09T10:00:00Z', discovered_at: '2026-08-09T10:00:01Z', baseline: false, summary: '一条测试动态' }

describe('resource pages', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.runtime.mockResolvedValue(runtime); api.settings.mockResolvedValue(settings); api.ups.mockResolvedValue([up]); api.channels.mockResolvedValue([channel])
    api.deliveries.mockResolvedValue({ items: [makeDelivery({ state: 'blocked' })], page: { next_cursor: '', has_more: false } }); api.biliLogin.mockResolvedValue(null); api.microsoftLogins.mockResolvedValue([])
    api.dynamics.mockResolvedValue({ items: [dynamic], page: { next_cursor: '', has_more: false } }); api.comments.mockResolvedValue({ items: [], page: { next_cursor: '', has_more: false } }); api.auditLogs.mockResolvedValue({ items: [makeAudit()], page: { next_cursor: '', has_more: false } })
    api.startBiliLogin.mockResolvedValue({ id: 'login', status: 'waiting', expires_at: '2026-08-09T10:05:00Z' }); api.createUP.mockResolvedValue(up); api.retryDelivery.mockResolvedValue({ status: 'queued' }); api.updateSettings.mockResolvedValue(settings)
  })

  it.each([
    { name: 'overview', path: '/overview', heading: '运行概览', view: <OverviewPage /> },
    { name: 'ups', path: '/ups', heading: 'UP 主', view: <UPsPage /> },
    { name: 'channels', path: '/channels', heading: '通知渠道', view: <ChannelsPage /> },
    { name: 'deliveries', path: '/deliveries', heading: '投递队列', view: <DeliveriesPage /> },
    { name: 'history', path: '/history', heading: '历史内容', view: <HistoryPage /> },
    { name: 'audit', path: '/audit-logs', heading: '操作日志', view: <AuditLogsPage /> },
    { name: 'settings', path: '/settings', heading: '设置', view: <SettingsPage /> },
  ])('renders $name from its own resource queries', async ({ path, heading, view }) => {
    renderPage(view, path)
    expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument()
  })

  it('creates an UP and invalidates the UP resource', async () => {
    const user = userEvent.setup(); renderPage(<UPsPage />, '/ups')
    await user.click(await screen.findByRole('button', { name: /添加 UP 主/ }))
    await user.type(screen.getByLabelText('UID'), '99')
    await user.type(screen.getByLabelText('备注名'), '新 UP')
    await user.click(screen.getByRole('button', { name: '保存' }))
    expect(api.createUP).toHaveBeenCalledWith('csrf', { uid: '99', name: '新 UP', enabled: true })
  })

  it('retries a blocked delivery through a mutation', async () => {
    const user = userEvent.setup(); renderPage(<DeliveriesPage />, '/deliveries')
    await user.click(await screen.findByRole('button', { name: /立即重试/ }))
    expect(api.retryDelivery).toHaveBeenCalledWith('csrf', 'delivery')
  })

  it('renders archived dynamic content without a global dashboard snapshot', async () => {
    renderPage(<HistoryPage />, '/history')
    expect(await screen.findByText('一条测试动态')).toBeInTheDocument()
    expect(screen.getAllByText('测试 UP')).toHaveLength(2)
  })
})

function renderPage(view: React.ReactNode, path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><ThemeProvider><NotificationProvider><SessionProvider value={{ csrf: 'csrf' }}><MemoryRouter initialEntries={[path]}>{view}</MemoryRouter></SessionProvider></NotificationProvider></ThemeProvider></QueryClientProvider>)
}
