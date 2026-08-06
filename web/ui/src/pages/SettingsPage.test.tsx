import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AdminAPI } from '../api'
import { renderRoute, settings } from '../test/fixtures'
import { SettingsPage } from './SettingsPage'

afterEach(() => vi.unstubAllGlobals())

describe('SettingsPage', () => {
  it('validates and submits collection settings', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const update = vi.spyOn(api, 'updateSettings').mockResolvedValue({ ...settings, poll_interval_sec: 45 })
    renderRoute(<SettingsPage csrf="csrf" preference="system" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    await user.clear(screen.getByLabelText('轮询间隔（秒）')); await user.type(screen.getByLabelText('轮询间隔（秒）'), '9'); await user.click(screen.getByRole('button', { name: '保存采集参数' })); expect(screen.getByText(/轮询间隔至少/)).toBeVisible(); expect(update).not.toHaveBeenCalled()
    await user.clear(screen.getByLabelText('轮询间隔（秒）')); await user.type(screen.getByLabelText('轮询间隔（秒）'), '45'); await user.click(screen.getByRole('button', { name: '保存采集参数' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.objectContaining({ poll_interval_sec: 45, comment_enabled: true })))
  })

  it('disables comment inputs and changes theme', async () => {
    const user = userEvent.setup(); const setPreference = vi.fn(); const api = new AdminAPI('csrf')
    renderRoute(<SettingsPage csrf="csrf" preference="system" setPreference={setPreference} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    await user.click(screen.getByRole('checkbox', { name: '启用 UP 评论回复监控' })); expect(screen.getByLabelText('每 UP 跟踪内容数 N')).toBeDisabled(); await user.click(screen.getByRole('button', { name: '深色' })); expect(setPreference).toHaveBeenCalledWith('dark')
  })

  it('rejects mismatched passwords without a request', async () => {
    const user = userEvent.setup(); const fetchMock = vi.fn(); vi.stubGlobal('fetch', fetchMock); const api = new AdminAPI('csrf')
    renderRoute(<SettingsPage csrf="csrf" preference="light" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={vi.fn()} />)
    await user.type(screen.getByLabelText('当前密码'), 'old-password'); await user.type(screen.getByLabelText('新密码'), 'new-password-one'); await user.type(screen.getByLabelText('确认新密码'), 'new-password-two'); await user.click(screen.getByRole('button', { name: '修改密码' }))
    expect(screen.getByText('两次输入的新密码不一致')).toBeVisible(); expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([
    { name: 'success', response: new Response(null, { status: 204 }), changed: 1, message: undefined },
    { name: 'failure', response: new Response(JSON.stringify({ error: { message: 'wrong password' } }), { status: 400 }), changed: 0, message: 'wrong password' },
  ])('handles password $name', async ({ response, changed, message }) => {
    const user = userEvent.setup(); vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response)); const onChanged = vi.fn(); const api = new AdminAPI('csrf')
    renderRoute(<SettingsPage csrf="csrf" preference="light" setPreference={vi.fn()} settings={settings} api={api} runMutation={request => request()} onChanged={onChanged} />)
    await user.type(screen.getByLabelText('当前密码'), 'old-password'); await user.type(screen.getByLabelText('新密码'), 'new-password'); await user.type(screen.getByLabelText('确认新密码'), 'new-password'); await user.click(screen.getByRole('button', { name: '修改密码' }))
    await waitFor(() => expect(onChanged).toHaveBeenCalledTimes(changed)); if (message) expect(screen.getByText(message)).toBeVisible()
  })
})
