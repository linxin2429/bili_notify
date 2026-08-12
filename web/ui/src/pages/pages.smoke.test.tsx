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
import { SourcesPage } from './SourcesPage'
import { ChannelsPage } from './ChannelsPage'
import { DeliveriesPage } from './DeliveriesPage'
import { HistoryPage } from './HistoryPage'
import { AuditLogsPage } from './AuditLogsPage'
import { SettingsPage } from './SettingsPage'

const api = vi.hoisted(() => ({
  runtime: vi.fn(), settings: vi.fn(), accounts: vi.fn(), zsxqGroups: vi.fn(), sources: vi.fn(), contents: vi.fn(), content: vi.fn(), contentComments: vi.fn(), channels: vi.fn(), deliveries: vi.fn(), biliLogin: vi.fn(), microsoftLogins: vi.fn(), auditLogs: vi.fn(),
  createBilibiliSource: vi.fn(), createZSXQSource: vi.fn(), updateSource: vi.fn(), deleteSource: vi.fn(), deleteZSXQSession: vi.fn(), createChannel: vi.fn(), updateChannel: vi.fn(), deleteChannel: vi.fn(), testChannel: vi.fn(), retryDelivery: vi.fn(), startBiliLogin: vi.fn(), cancelBiliLogin: vi.fn(), deleteBilibiliSession: vi.fn(), startMicrosoftLogin: vi.fn(), cancelMicrosoftLogin: vi.fn(), updateSettings: vi.fn(),
}))
const session = vi.hoisted(() => ({ changePassword: vi.fn() }))
vi.mock('../shared/api/resources', () => ({ resources: api }))
vi.mock('../shared/api/session', () => ({ sessionAPI: session }))

const runtime = { status: { auth_valid: true, bili_account: { uid: '1', name: '测试账号' }, up_count: 1, channel_count: 1, outbox_depth: 1, ready: true }, timezone: 'Asia/Shanghai', updated_at: '2026-08-09T10:00:00Z' }
const channel = { id: 'channel', name: '邮件', type: 'email' as const, enabled: true, settings: { host: 'smtp.example.com' }, configured_secrets: ['password'], created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' }
const source = { id: 'bilibili:up:42', platform: 'bilibili' as const, type: 'up' as const, external_id: '42', name: '测试 UP', enabled: true, baseline_state: 'complete' as const, backfill_done: 0, backfill_total: 0, sync_lag_sec: 0, consecutive_fails: 0 }
const content = { id: 'bilibili:content:dynamic', platform: 'bilibili' as const, source_id: source.id, external_id: 'dynamic', author_id: '42', author_name: '测试 UP', upstream_type: 'DYNAMIC_TYPE_WORD', type: 'dynamic' as const, title: '一条测试动态', text: '正文', published_at: '2026-08-09T10:00:00Z', first_seen_at: '2026-08-09T10:00:01Z', last_synced_at: '2026-08-09T10:00:01Z', baseline: false }

describe('resource pages', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.runtime.mockResolvedValue(runtime); api.settings.mockResolvedValue(settings); api.accounts.mockResolvedValue([{ platform: 'bilibili', status: 'connected' }, { platform: 'zsxq', status: 'disconnected' }]); api.channels.mockResolvedValue([channel])
    api.sources.mockResolvedValue([source]); api.contents.mockResolvedValue({ items: [content], page: { next_cursor: '', has_more: false } }); api.content.mockResolvedValue({ content, attachments: [] }); api.contentComments.mockResolvedValue({ children: [], incomplete: false })
    api.deliveries.mockResolvedValue({ items: [makeDelivery({ state: 'blocked' })], page: { next_cursor: '', has_more: false } }); api.biliLogin.mockResolvedValue(null); api.microsoftLogins.mockResolvedValue([])
    api.auditLogs.mockResolvedValue({ items: [makeAudit()], page: { next_cursor: '', has_more: false } })
    api.zsxqGroups.mockResolvedValue([]); api.startBiliLogin.mockResolvedValue({ id: 'login', status: 'waiting', expires_at: '2026-08-09T10:05:00Z' }); api.createBilibiliSource.mockResolvedValue(source); api.createZSXQSource.mockResolvedValue(source); api.retryDelivery.mockResolvedValue({ status: 'queued' }); api.updateSettings.mockResolvedValue(settings)
    api.updateSource.mockResolvedValue(source); api.deleteSource.mockResolvedValue(undefined); api.createChannel.mockResolvedValue(channel); api.updateChannel.mockResolvedValue(channel); api.deleteChannel.mockResolvedValue(undefined); api.testChannel.mockResolvedValue({ status: 'sent' }); api.deleteBilibiliSession.mockResolvedValue(undefined); session.changePassword.mockResolvedValue({ csrf_token: 'replacement-csrf' })
  })

  it('starts Bilibili QR login and confirms logout from overview', async () => {
    const user = userEvent.setup()
    renderPage(<OverviewPage />, '/overview')
    expect(await screen.findByRole('heading', { name: '运行概览' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '生成登录二维码' }))
    expect(api.startBiliLogin).toHaveBeenCalledWith('csrf')
    await user.click(screen.getByRole('button', { name: '退出登录' }))
    await user.click(screen.getByRole('button', { name: '确认退出' }))
    await waitFor(() => expect(api.deleteBilibiliSession).toHaveBeenCalledWith('csrf'))
    expect(await screen.findByText('已退出 B 站登录')).toBeInTheDocument()
  })

  it('keeps overview usable when secondary resources fail', async () => {
    api.accounts.mockRejectedValue(new Error('accounts down'))
    api.sources.mockRejectedValue(new Error('sources down'))
    api.channels.mockRejectedValue(new Error('channels down'))
    api.settings.mockRejectedValue(new Error('settings down'))
    renderPage(<OverviewPage />, '/overview')
    expect(await screen.findByRole('heading', { name: '运行概览' })).toBeInTheDocument()
    expect(await screen.findByText(/部分检查项依赖的数据加载失败/)).toBeInTheDocument()
    expect(screen.getByText(/运行参数加载失败/)).toBeInTheDocument()
  })

  it.each([
    { name: 'overview', path: '/overview', heading: '运行概览', view: <OverviewPage /> },
    { name: 'sources', path: '/sources', heading: '采集源', view: <SourcesPage /> },
    { name: 'channels', path: '/channels', heading: '通知渠道', view: <ChannelsPage /> },
    { name: 'deliveries', path: '/deliveries', heading: '投递队列', view: <DeliveriesPage /> },
    { name: 'history', path: '/history', heading: '历史内容', view: <HistoryPage /> },
    { name: 'audit', path: '/audit-logs', heading: '操作日志', view: <AuditLogsPage /> },
    { name: 'settings', path: '/settings', heading: '设置', view: <SettingsPage /> },
  ])('renders $name from its own resource queries', async ({ path, heading, view }) => {
    renderPage(view, path)
    expect(await screen.findByRole('heading', { name: heading })).toBeInTheDocument()
  })

  it('creates a Bilibili source', async () => {
    const user = userEvent.setup(); renderPage(<SourcesPage />, '/sources')
    await user.click(await screen.findByRole('button', { name: '添加 B 站采集源' }))
    await user.type(screen.getByLabelText('UID'), '99')
    await user.type(screen.getByLabelText('来源名称'), '新 UP')
    await user.click(screen.getByRole('button', { name: '保存' }))
    expect(api.createBilibiliSource).toHaveBeenCalledWith('csrf', { uid: '99', name: '新 UP', note: '', enabled: true })
  })

  it('creates a Knowledge Planet source from the connected account groups', async () => {
    api.accounts.mockResolvedValue([{ platform: 'zsxq', status: 'connected', display_name: '星球号' }])
    api.zsxqGroups.mockResolvedValue([{ id: '28882581855851', name: '账号星球', owner_id: '8', owner_name: '星主' }])
    const user = userEvent.setup(); renderPage(<SourcesPage />, '/sources')
    await user.click(await screen.findByRole('button', { name: '添加知识星球采集源' }))
    await user.selectOptions(await screen.findByLabelText('星球'), '28882581855851')
    expect(screen.queryByLabelText('平台')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('星球 ID')).not.toBeInTheDocument()
    expect(screen.queryByLabelText('来源名称')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '保存' }))
    expect(api.createZSXQSource).toHaveBeenCalledWith('csrf', { group_id: '28882581855851', note: '', enabled: true })
  })

  it('shows already added Knowledge Planet groups but disables them', async () => {
    const existing = { ...source, id: 'zsxq:planet:9', platform: 'zsxq' as const, type: 'planet' as const, external_id: '9', name: '已添加星球' }
    api.accounts.mockResolvedValue([{ platform: 'zsxq', status: 'connected', display_name: '星球号' }])
    api.sources.mockResolvedValue([existing])
    api.zsxqGroups.mockResolvedValue([{ id: '9', name: '已添加星球' }, { id: '10', name: '可添加星球' }])
    const user = userEvent.setup(); renderPage(<SourcesPage />, '/sources')
    await user.click(await screen.findByRole('button', { name: '添加知识星球采集源' }))
    expect(await screen.findByRole('option', { name: /已添加星球.*已添加/ })).toBeDisabled()
    expect(screen.getByRole('option', { name: /可添加星球/ })).toBeEnabled()
  })

  it('retries loading Knowledge Planet groups after an upstream error', async () => {
    api.accounts.mockResolvedValue([{ platform: 'zsxq', status: 'connected', display_name: '星球号' }])
    api.zsxqGroups.mockRejectedValueOnce(new Error('星球列表加载失败')).mockResolvedValueOnce([{ id: '10', name: '可添加星球' }])
    const user = userEvent.setup(); renderPage(<SourcesPage />, '/sources')
    await user.click(await screen.findByRole('button', { name: '添加知识星球采集源' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('星球列表加载失败')
    await user.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByRole('option', { name: /可添加星球/ })).toBeEnabled()
    expect(api.zsxqGroups).toHaveBeenCalledTimes(2)
  })

  it('shows empty source CTAs and logs out the Knowledge Planet account', async () => {
    api.sources.mockResolvedValue([])
    api.accounts.mockResolvedValue([{ platform: 'zsxq', status: 'connected', display_name: '星球号', masked_phone: '138****0000' }])
    api.deleteZSXQSession.mockResolvedValue(undefined)
    const user = userEvent.setup()
    renderPage(<SourcesPage />, '/sources')
    expect(await screen.findByText('尚未添加 B 站 UP')).toBeInTheDocument()
    expect(screen.getByText('尚未添加知识星球')).toBeInTheDocument()
    expect(screen.getByText('已连接')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '退出登录' }))
    await user.click(screen.getByRole('button', { name: '确认退出' }))
    await waitFor(() => expect(api.deleteZSXQSession).toHaveBeenCalledWith('csrf'))
  })

  it('retries a blocked delivery through a mutation', async () => {
    const user = userEvent.setup(); renderPage(<DeliveriesPage />, '/deliveries')
    await user.click(await screen.findByRole('button', { name: /立即重试/ }))
    expect(api.retryDelivery).toHaveBeenCalledWith('csrf', 'delivery')
  })

  it('filters deliveries by state and pages with a cursor stack', async () => {
    api.deliveries.mockResolvedValue({
      items: [makeDelivery({ id: 'blocked', state: 'blocked' }), makeDelivery({ id: 'pending', state: 'pending', next_at: '2026-08-09T11:00:00Z' })],
      page: { next_cursor: 'cursor-2', has_more: true },
    })
    const user = userEvent.setup()
    renderPage(<DeliveriesPage />, '/deliveries')
    expect(await screen.findByRole('button', { name: /立即重试/ })).toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '等待重试' }))
    expect(screen.queryByRole('button', { name: /立即重试/ })).not.toBeInTheDocument()
    await user.click(screen.getByRole('tab', { name: '全部' }))
    await user.click(screen.getByRole('button', { name: '下一页' }))
    await waitFor(() => expect(api.deliveries).toHaveBeenLastCalledWith('cursor-2', expect.anything()))
  })

  it('renders archived dynamic content as a readable feed card', async () => {
    renderPage(<HistoryPage />, '/history')
    expect(await screen.findByRole('heading', { level: 2, name: '一条测试动态' })).toBeInTheDocument()
    expect(screen.getByText('正文')).toBeInTheDocument()
    expect(screen.getByText(/B 站 · 测试 UP · 文字/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '媒体与附件' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '查看评论：一条测试动态' })).toBeInTheDocument()
  })

  it('keeps keyword search visible and clears progressive history filters', async () => {
    const user = userEvent.setup()
    renderPage(<HistoryPage />, '/history?platform=bilibili&q=正文')

    const keyword = await screen.findByLabelText('关键字')
    expect(keyword).toHaveValue('正文')
    expect(screen.getByLabelText('平台')).toHaveValue('bilibili')
    expect(screen.getByRole('button', { name: /更多筛选/ })).toHaveTextContent('1')

    await user.click(screen.getByRole('button', { name: '清除筛选' }))
    expect(keyword).toHaveValue('')
    await waitFor(() => {
      const lastQuery = api.contents.mock.calls[api.contents.mock.calls.length - 1]?.[0]
      expect(lastQuery).not.toHaveProperty('platform')
      expect(lastQuery).not.toHaveProperty('q')
    })
  })

  it('expands a nested cross-platform comment tree on the feed card', async () => {
    api.contentComments.mockResolvedValue({ children: [{ id: 'bilibili:comment:root', platform: 'bilibili', content_id: content.id, rpid: 'root', mid: '7', name: '观众', message: '问题', time: '2026-08-09T10:00:00Z', author_role: 'member', children: [{ id: 'bilibili:comment:reply', platform: 'bilibili', content_id: content.id, rpid: 'reply', mid: '42', name: '测试 UP', message: '回答', time: '2026-08-09T10:01:00Z', author_role: 'up', children: [] }] }], incomplete: false })
    const user = userEvent.setup(); renderPage(<HistoryPage />, '/history')

    await user.click(await screen.findByRole('button', { name: '查看评论：一条测试动态' }))
    expect(await screen.findByText('问题')).toBeInTheDocument()
    expect(api.content).toHaveBeenCalledWith(content.id, expect.anything())
    expect(api.contentComments).toHaveBeenCalledWith(content.id, expect.anything())
    expect(screen.getByText('回答')).toBeInTheDocument()
    expect(screen.getByText('UP 主')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '收起' }))
    expect(screen.queryByText('问题')).not.toBeInTheDocument()
  })

  it('shows Knowledge Planet archive state, attachment outcomes and an incomplete owner thread', async () => {
    const planet = { ...source, id: 'zsxq:planet:9', platform: 'zsxq' as const, type: 'planet' as const, external_id: '9', name: '测试星球', owner_id: 'owner', owner_name: '星球主' }
    const topic = { ...content, id: 'zsxq:content:topic', platform: 'zsxq' as const, source_id: planet.id, external_id: 'topic', author_id: 'member', author_name: '成员', upstream_type: 'talk', type: 'discussion' as const, title: '', baseline: true, deleted_at: '2026-08-09T11:00:00Z', url: 'https://wx.zsxq.com/topic' }
    api.sources.mockResolvedValue([planet])
    api.contents.mockResolvedValue({ items: [topic], page: { next_cursor: '', has_more: false } })
    api.content.mockResolvedValue({ content: topic, attachments: [
      { id: 'local', content_id: topic.id, external_id: 'file-1', type: 'file', file_name: '资料.pdf', size: 2048, localized: true },
      { id: 'remote', content_id: topic.id, external_id: 'file-2', type: 'file', file_name: '', size: 0, localize_error: '超过下载预算' },
    ] })
    api.contentComments.mockResolvedValue({ children: [{ id: 'zsxq:comment:owner', platform: 'zsxq', content_id: topic.id, rpid: 'owner', mid: 'owner', name: '星球主', message: '补充说明', time: '2026-08-09T10:00:00Z', author_role: 'owner', is_trigger: true, deleted_at: '2026-08-09T11:00:00Z', children: [] }], incomplete: true })
    const user = userEvent.setup()
    renderPage(<HistoryPage />, '/history?platform=zsxq')

    expect(await screen.findByText('正文')).toBeInTheDocument()
    expect(screen.getByText('已删除')).toBeInTheDocument()
    expect(screen.getByText('基线')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /查看原内容/ })).toHaveAttribute('href', topic.url)

    await user.click(screen.getByRole('button', { name: '媒体与附件' }))
    expect(await screen.findByRole('link', { name: '资料.pdf' })).toHaveAttribute('href', expect.stringContaining('/attachments/local'))
    expect(screen.getByText(/超过下载预算/)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: '查看评论：正文' }))
    expect(await screen.findByText('上游分页或父子关系不完整，当前树可能缺少节点。')).toBeInTheDocument()
    expect(screen.getAllByText('星球主').length).toBeGreaterThan(0)
    expect(screen.getByText('新增触发')).toBeInTheDocument()
    expect(screen.getByText('补充说明')).toBeInTheDocument()
  })

  it('filters, paginates and inspects audit records', async () => {
    api.auditLogs.mockResolvedValue({ items: [makeAudit({ details: { changed: ['enabled'] }, error_code: 'TEST' })], page: { next_cursor: 'cursor-2', has_more: true } })
    const user = userEvent.setup(); renderPage(<AuditLogsPage />, '/audit-logs')
    expect(await screen.findByRole('button', { name: '查看详情' })).toBeInTheDocument()

    await user.selectOptions(screen.getByLabelText('操作'), 'source.create')
    await screen.findByRole('button', { name: '查看详情' })
    await user.selectOptions(screen.getByLabelText('结果'), 'failure')
    await screen.findByRole('button', { name: '查看详情' })
    fireEvent.change(screen.getByLabelText('资源类型'), { target: { value: 'channel' } })
    await screen.findByRole('button', { name: '查看详情' })
    fireEvent.change(screen.getByLabelText('开始时间'), { target: { value: '2026-08-01T08:00' } })
    await screen.findByRole('button', { name: '查看详情' })
    await user.type(screen.getByLabelText('关键字'), 'request')
    await waitFor(() => expect(api.auditLogs).toHaveBeenLastCalledWith(expect.objectContaining({ action: 'source.create', outcome: 'failure', resource_type: 'channel', q: 'request' }), expect.anything()))

    await user.click(screen.getByRole('button', { name: '下一页' }))
    await waitFor(() => expect(api.auditLogs).toHaveBeenLastCalledWith(expect.objectContaining({ after: 'cursor-2' }), expect.anything()))
    await user.click(screen.getByRole('button', { name: '查看详情' }))
    expect(screen.getByRole('heading', { name: '安全变更摘要' })).toBeInTheDocument()
    expect(screen.getByText('TEST')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '关闭' }))
    expect(screen.queryByRole('heading', { name: '安全变更摘要' })).not.toBeInTheDocument()
  })

  it('edits, disables and deletes an existing source through confirmation', async () => {
    const user = userEvent.setup(); renderPage(<SourcesPage />, '/sources')
    await user.click((await screen.findAllByRole('button', { name: /编辑/ }))[0]); await user.type(screen.getByLabelText('备注'), '已修改')
    await user.click(screen.getByLabelText('启用采集')); await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(api.updateSource).toHaveBeenCalledWith('csrf', { id: source.id, name: source.name, note: '已修改', enabled: false }))
    await user.click((await screen.findAllByRole('button', { name: /删除/ }))[0]); await user.click(screen.getByRole('button', { name: '删除采集源' }))
    await waitFor(() => expect(api.deleteSource).toHaveBeenCalledWith('csrf', source.id))
  })

  it.each([
    { type: 'email', fields: { 'SMTP 主机': 'smtp.test', '发件人': 'from@test', '收件人': 'to@test', '密码': 'secret' }, expected: { settings: { host: 'smtp.test', port: '465', tls: 'tls', username: '', from: 'from@test', to: 'to@test' }, secrets: { password: 'secret' } } },
    { type: 'microsoft', fields: { '应用程序（客户端）ID': 'client', '收件人': 'to@test' }, expected: { enabled: false, settings: { client_id: 'client', tenant: 'common', to: 'to@test' } } },
    { type: 'dingtalk', fields: { 'Webhook URL': 'https://robot.test', '签名密钥': 'secret' }, expected: { settings: {}, secrets: { webhook: 'https://robot.test', secret: 'secret' } } },
    { type: 'feishu', fields: { '应用 App ID': 'app', '应用 App Secret': 'app-secret', '目标群 Chat ID': 'oc_group' }, expected: { settings: { app_id: 'app', chat_id: 'oc_group' }, secrets: { app_secret: 'app-secret' } } },
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
    // Open advanced knobs used later in the scenario.
    await user.click(screen.getByText('高级'))
    for (const input of screen.getAllByRole('textbox')) fireEvent.change(input, { target: { value: input.getAttribute('inputmode') === 'decimal' ? '10' : input.getAttribute('value') || '' } })
    fireEvent.click(screen.getByLabelText('启用 B 站评论监控')); await user.selectOptions(screen.getByLabelText('日志级别'), 'debug'); await user.click(screen.getByRole('button', { name: '保存运行设置' }))
    expect(api.updateSettings).not.toHaveBeenCalled(); expect(screen.getByRole('alert')).toBeInTheDocument()
    fireEvent.change(screen.getByLabelText('动态轮询（秒）'), { target: { value: '30' } })
    fireEvent.change(screen.getByLabelText('知识星球动态轮询（秒）'), { target: { value: '60' } })
    fireEvent.change(screen.getByLabelText('关注关系刷新（秒）'), { target: { value: '3600' } })
    fireEvent.change(screen.getByLabelText('空间校验间隔（秒）'), { target: { value: '3600' } })
    const pauses = screen.getAllByLabelText('风控暂停（秒）'); fireEvent.change(pauses[0], { target: { value: '300' } }); fireEvent.change(pauses[1], { target: { value: '600' } })
    fireEvent.change(screen.getByLabelText('评论跟踪内容数 N'), { target: { value: '10' } })
    fireEvent.change(screen.getByLabelText('评论同步间隔（秒）'), { target: { value: '60' } })
    fireEvent.change(screen.getByLabelText('评论同步（秒）'), { target: { value: '600' } })
    fireEvent.change(screen.getByLabelText('积压时长阈值（秒）'), { target: { value: '600' } })
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
