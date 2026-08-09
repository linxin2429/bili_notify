import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AdminAPI } from '../api'
import { renderRoute, settings } from '../test/fixtures'
import { SettingsPage } from './SettingsPage'

afterEach(() => {
  vi.unstubAllGlobals()
  window.localStorage.clear()
})

function fillPasswordFields(current: string, next: string, confirm = next) {
  fireEvent.change(screen.getByLabelText('当前密码'), { target: { value: current } })
  fireEvent.change(screen.getByLabelText('新密码'), { target: { value: next } })
  fireEvent.change(screen.getByLabelText('确认新密码'), { target: { value: confirm } })
}

describe('SettingsPage', () => {
  it('wires every runtime settings section to the submitted object', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const update = vi.spyOn(api, 'updateSettings').mockResolvedValue(settings)
    renderRoute(<SettingsPage csrf="csrf" preference="system" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    const values: Array<{ label: string; value: string }> = [
      { label: '请求速率（次/秒）', value: '3.5' }, { label: '请求并发数', value: '5' },
      { label: '每 UP 跟踪内容数 N', value: '12' }, { label: '根评论最大页数', value: '3' },
      { label: '子评论最大页数', value: '6' }, { label: '评论批次间隔（秒）', value: '180' },
    ]
    for (const item of values) fireEvent.change(screen.getByLabelText(item.label), { target: { value: item.value } })
    for (const item of [
      { label: '关注关系刷新间隔（秒）', value: '900' }, { label: '空间完整性校验间隔（秒）', value: '2400' },
      { label: '动态最大翻页数', value: '12' }, { label: '风控暂停时长（秒）', value: '420' },
    ]) fireEvent.change(await screen.findByLabelText(item.label), { target: { value: item.value } })
    for (const item of [
      { label: '投递并发数', value: '9' }, { label: '积压条数告警阈值', value: '150' }, { label: '积压时长告警阈值（秒）', value: '450' },
      { label: '第 1 段', value: '6' }, { label: '第 2 段', value: '36' }, { label: '第 3 段', value: '180' }, { label: '第 4 段', value: '900' }, { label: '第 5 段', value: '4000' },
    ]) fireEvent.change(await screen.findByLabelText(item.label), { target: { value: item.value } })
    await user.click(await screen.findByLabelText('日志级别')); await user.click(screen.getByRole('option', { name: 'warn' }))
    fireEvent.change(screen.getByLabelText('审计日志保留天数'), { target: { value: '365' } })
    await user.click(screen.getByRole('button', { name: '保存运行设置' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({
      request_rate: 3.5, request_concurrency: 5, comment_track_n: 12, comment_root_pages: 3, comment_reply_pages: 6,
      comment_batch_interval_sec: 180, relation_refresh_interval_sec: 900, space_reconcile_interval_sec: 2400,
      max_dynamic_pages: 12, risk_pause_sec: 420, delivery_concurrency: 9, backlog_alert_count: 150,
      backlog_alert_age_sec: 450, delivery_retry_delays_sec: [6, 36, 180, 900, 4000], log_level: 'warn', audit_log_retention_days: 365,
    })))
  }, 15_000)

  it('reports a settings mutation failure and restores the save action', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); vi.spyOn(api, 'updateSettings').mockRejectedValue(new Error('settings unavailable'))
    renderRoute(<SettingsPage csrf="csrf" preference="system" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    await user.click(screen.getByRole('button', { name: '保存运行设置' }))
    expect(await screen.findByText('settings unavailable')).toBeVisible()
    expect(screen.getByRole('button', { name: '保存运行设置' })).toBeEnabled()
  })

  it('validates and submits complete runtime settings', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const update = vi.spyOn(api, 'updateSettings').mockResolvedValue({ ...settings, poll_interval_sec: 45 })
    renderRoute(<SettingsPage csrf="csrf" preference="system" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    fireEvent.change(screen.getByLabelText('轮询间隔（秒）'), { target: { value: '9' } })
    await user.click(screen.getByRole('button', { name: '保存运行设置' }))
    expect(screen.getByText(/轮询间隔必须/)).toBeVisible(); expect(update).not.toHaveBeenCalled()
    fireEvent.change(screen.getByLabelText('轮询间隔（秒）'), { target: { value: '45' } })
    await user.click(screen.getByRole('button', { name: '保存运行设置' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({ poll_interval_sec: 45, comment_enabled: true, delivery_retry_delays_sec: [5, 30, 120, 600, 3600] })))
  }, 15_000)

  it('disables comment inputs and changes theme', async () => {
    const user = userEvent.setup(); const setPreference = vi.fn(); const api = new AdminAPI('csrf')
    renderRoute(<SettingsPage csrf="csrf" preference="system" setPreference={setPreference} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    await user.click(screen.getByRole('checkbox', { name: '启用 UP 评论回复监控' })); expect(screen.getByLabelText('每 UP 跟踪内容数 N')).toBeDisabled(); await user.click(screen.getByRole('button', { name: '深色' })); expect(setPreference).toHaveBeenCalledWith('dark')
  })

  it('collapses sections and restores the expanded state after remount', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf')
    const first = renderRoute(<SettingsPage csrf="csrf" preference="system" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    expect(screen.getByLabelText('轮询间隔（秒）')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '收起基础采集' }))
    expect(screen.queryByLabelText('轮询间隔（秒）')).not.toBeInTheDocument()
    const stored = JSON.parse(window.localStorage.getItem('settings.expanded') || '{}') as Record<string, boolean>
    expect(stored.basic).toBe(false)
    first.unmount()
    renderRoute(<SettingsPage csrf="csrf" preference="system" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    expect(screen.queryByLabelText('轮询间隔（秒）')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: '展开基础采集' })).toBeVisible()
  })

  it('still toggles sections when localStorage persistence fails', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf')
    const setItem = vi.spyOn(window.localStorage, 'setItem').mockImplementation(() => { throw new Error('quota') })
    renderRoute(<SettingsPage csrf="csrf" preference="system" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    expect(screen.getByLabelText('轮询间隔（秒）')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '收起基础采集' }))
    expect(screen.queryByLabelText('轮询间隔（秒）')).not.toBeInTheDocument()
    expect(setItem).toHaveBeenCalled()
  })

  it('rejects mismatched passwords without a request', async () => {
    const user = userEvent.setup(); const fetchMock = vi.fn(); vi.stubGlobal('fetch', fetchMock); const api = new AdminAPI('csrf')
    renderRoute(<SettingsPage csrf="csrf" preference="light" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    fillPasswordFields('old-password', 'new-password-one', 'new-password-two')
    await user.click(screen.getByRole('button', { name: '修改密码' }))
    expect(screen.getByText('两次输入的新密码不一致')).toBeVisible(); expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([
    { name: 'success', response: new Response(JSON.stringify({ csrf_token: 'replacement-csrf' }), { status: 200 }), changed: 1, message: undefined },
    { name: 'failure', response: new Response(JSON.stringify({ error: { message: 'wrong password' } }), { status: 400 }), changed: 0, message: 'wrong password' },
  ])('handles password $name', async ({ response, changed, message }) => {
    const user = userEvent.setup(); vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response)); const onChanged = vi.fn(); const api = new AdminAPI('csrf')
    renderRoute(<SettingsPage csrf="csrf" preference="light" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={onChanged} />)
    fillPasswordFields('old-password', 'new-password')
    await user.click(screen.getByRole('button', { name: '修改密码' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(changed)); if (message) expect(screen.getByText(message)).toBeVisible()
  })
})
