import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
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
const session = vi.hoisted(() => ({ changePassword: vi.fn() }))
vi.mock('../shared/api/resources', () => ({ resources: api }))
vi.mock('../shared/api/session', () => ({ sessionAPI: session }))

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
    api.updateUP.mockResolvedValue(up); api.deleteUP.mockResolvedValue(undefined); api.createChannel.mockResolvedValue(channel); api.updateChannel.mockResolvedValue(channel); api.deleteChannel.mockResolvedValue(undefined); api.testChannel.mockResolvedValue({ status: 'sent' }); session.changePassword.mockResolvedValue({ csrf_token: 'replacement-csrf' })
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

  it('filters, paginates and inspects audit records', async () => {
    api.auditLogs.mockResolvedValue({ items: [makeAudit({ details: { changed: ['enabled'] }, error_code: 'TEST' })], page: { next_cursor: 'cursor-2', has_more: true } })
    const user = userEvent.setup(); renderPage(<AuditLogsPage />, '/audit-logs')
    expect(await screen.findByRole('button', { name: '查看详情' })).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('操作'), 'up.create')
    await screen.findByRole('button', { name: '查看详情' })
    await user.selectOptions(screen.getByLabelText('结果'), 'failure')
    await screen.findByRole('button', { name: '查看详情' })
    fireEvent.change(screen.getByLabelText('资源类型'), { target: { value: 'channel' } })
    await screen.findByRole('button', { name: '查看详情' })
    fireEvent.change(screen.getByLabelText('开始时间'), { target: { value: '2026-08-01T08:00' } })
    await screen.findByRole('button', { name: '查看详情' })
    await user.type(screen.getByLabelText('关键字'), 'request')
    await waitFor(() => expect(api.auditLogs).toHaveBeenLastCalledWith(expect.objectContaining({ action: 'up.create', outcome: 'failure', resource_type: 'channel', q: 'request' }), expect.anything()))

    await user.click(screen.getByRole('button', { name: '下一页 →' }))
    await waitFor(() => expect(api.auditLogs).toHaveBeenLastCalledWith(expect.objectContaining({ after: 'cursor-2' }), expect.anything()))
    await user.click(screen.getByRole('button', { name: '查看详情' }))
    expect(screen.getByRole('heading', { name: '安全变更摘要' })).toBeInTheDocument()
    expect(screen.getByText('TEST')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '关闭' }))
    expect(screen.queryByRole('heading', { name: '安全变更摘要' })).not.toBeInTheDocument()
  })

  it('edits, disables and deletes an existing UP through confirmation', async () => {
    const user = userEvent.setup(); renderPage(<UPsPage />, '/ups')
    await user.click(await screen.findByRole('button', { name: /编辑/ })); await user.clear(screen.getByLabelText('备注名')); await user.type(screen.getByLabelText('备注名'), '已修改')
    await user.click(screen.getByLabelText('启用轮询')); await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(api.updateUP).toHaveBeenCalledWith('csrf', { uid: '42', name: '已修改', enabled: false }))
    await user.click(screen.getByRole('button', { name: /删除/ })); await user.click(screen.getByRole('button', { name: '确认删除' }))
    await waitFor(() => expect(api.deleteUP).toHaveBeenCalledWith('csrf', '42'))
  })

  it.each([
    { type: 'email', fields: { 'SMTP 主机': 'smtp.test', '发件人': 'from@test', '收件人': 'to@test', '密码': 'secret' }, expected: { settings: { host: 'smtp.test', port: '465', tls: 'tls', username: '', from: 'from@test', to: 'to@test' }, secrets: { password: 'secret' } } },
    { type: 'microsoft', fields: { '应用程序（客户端）ID': 'client', '收件人': 'to@test' }, expected: { enabled: false, settings: { client_id: 'client', tenant: 'common', to: 'to@test' } } },
    { type: 'dingtalk', fields: { 'Webhook URL': 'https://robot.test', '签名密钥': 'secret' }, expected: { settings: {}, secrets: { webhook: 'https://robot.test', secret: 'secret' } } },
    { type: 'feishu', fields: { 'Webhook URL': 'https://feishu.test', '签名密钥': 'secret', '应用 App ID': 'app', '应用 App Secret': 'app-secret' }, expected: { settings: { app_id: 'app' }, secrets: { webhook: 'https://feishu.test', secret: 'secret', app_secret: 'app-secret' } } },
    { type: 'wecom', fields: { 'Webhook URL': 'https://wecom.test' }, expected: { settings: {}, secrets: { webhook: 'https://wecom.test' } } },
  ] as const)('builds a typed $type channel draft from its dedicated fields', async ({ type, fields, expected }) => {
    api.channels.mockResolvedValue([]); const user = userEvent.setup(); const view = renderPage(<ChannelsPage />, '/channels')
    await user.click(await screen.findByRole('button', { name: /添加渠道/ })); await user.type(screen.getByLabelText('渠道名称'), `${type} channel`)
    if (type !== 'email') await user.selectOptions(screen.getByLabelText('渠道类型'), type)
    for (const [label, value] of Object.entries(fields)) await user.type(screen.getByLabelText(new RegExp(label)), value)
    await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(api.createChannel).toHaveBeenCalledWith('csrf', expect.objectContaining({ name: `${type} channel`, type, ...expected })))
    view.unmount()
  })

  it('tests, deletes and completes Microsoft authorization workflows', async () => {
    const microsoft = { ...channel, id: 'ms', name: 'Microsoft', type: 'microsoft' as const, settings: { authorized: 'false', client_id: 'client' } }
    api.channels.mockResolvedValue([microsoft]); api.microsoftLogins.mockResolvedValue([{ channel_id: 'ms', status: 'pending', user_code: 'CODE' }]); api.cancelMicrosoftLogin.mockResolvedValue(undefined)
    const user = userEvent.setup(); const view = renderPage(<ChannelsPage />, '/channels'); expect(await screen.findByText('CODE')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '取消' })); expect(api.cancelMicrosoftLogin).toHaveBeenCalledWith('csrf', 'ms')
    await user.click(screen.getByRole('button', { name: /发送测试/ })); expect(await screen.findByText('测试通知已发送')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /删除/ })); await user.click(screen.getByRole('button', { name: '确认删除' })); expect(api.deleteChannel).toHaveBeenCalledWith('csrf', 'ms'); view.unmount()

    api.microsoftLogins.mockResolvedValue([]); api.startMicrosoftLogin.mockResolvedValue({ channel_id: 'ms', status: 'pending', verification_uri_complete: 'https://login.test' }); const open = vi.spyOn(window, 'open').mockImplementation(() => null)
    renderPage(<ChannelsPage />, '/channels'); await user.click(await screen.findByRole('button', { name: '开始授权' })); await waitFor(() => expect(open).toHaveBeenCalledWith('https://login.test', '_blank', 'noopener,noreferrer'))
  })

  it('updates every settings group, validates bad input and changes password', async () => {
    const user = userEvent.setup(); renderPage(<SettingsPage />, '/settings'); expect(await screen.findByRole('heading', { name: '设置' })).toBeInTheDocument()
    for (const input of screen.getAllByRole('textbox')) fireEvent.change(input, { target: { value: input.getAttribute('inputmode') === 'decimal' ? '10' : input.getAttribute('value') || '' } })
    fireEvent.click(screen.getByLabelText('启用评论监控')); await user.selectOptions(screen.getByLabelText('日志级别'), 'debug'); await user.click(screen.getByRole('button', { name: '保存运行设置' }))
    expect(api.updateSettings).not.toHaveBeenCalled(); expect(screen.getByRole('alert')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('轮询间隔（秒）'), { target: { value: '30' } }); fireEvent.change(screen.getByLabelText('关注关系刷新（秒）'), { target: { value: '3600' } }); fireEvent.change(screen.getByLabelText('空间校验间隔（秒）'), { target: { value: '3600' } }); fireEvent.change(screen.getByLabelText('风控暂停（秒）'), { target: { value: '300' } }); fireEvent.change(screen.getByLabelText('评论批次间隔（秒）'), { target: { value: '60' } }); fireEvent.change(screen.getByLabelText('积压时长阈值（秒）'), { target: { value: '600' } })
    const retryFields = screen.getAllByLabelText(/阶段重试/); ['5', '30', '120', '600', '3600'].forEach((value, index) => fireEvent.change(retryFields[index], { target: { value } }))
    await user.click(screen.getByRole('button', { name: '保存运行设置' })); await waitFor(() => expect(api.updateSettings).toHaveBeenCalled())
    await user.type(screen.getByLabelText('当前密码'), 'current'); await user.type(screen.getByLabelText(/^新密码/), 'replacement'); await user.type(screen.getByLabelText('确认新密码'), 'different'); await user.click(screen.getByRole('button', { name: '修改密码' })); expect(await screen.findByText('两次输入的新密码不一致')).toBeInTheDocument()
    await user.clear(screen.getByLabelText('确认新密码')); await user.type(screen.getByLabelText('确认新密码'), 'replacement'); await user.click(screen.getByRole('button', { name: '修改密码' })); await waitFor(() => expect(session.changePassword).toHaveBeenCalledWith('csrf', 'current', 'replacement'))
    expect(await screen.findByText('管理员密码已修改，其他设备的会话已失效')).toBeInTheDocument()
    expect(screen.getByLabelText('当前密码')).toHaveValue('')
  })
})

function renderPage(view: React.ReactNode, path: string) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><ThemeProvider><NotificationProvider><SessionProvider value={{ csrf: 'csrf' }}><MemoryRouter initialEntries={[path]}>{view}</MemoryRouter></SessionProvider></NotificationProvider></ThemeProvider></QueryClientProvider>)
}
