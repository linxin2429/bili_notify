import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AdminAPI } from '../api'
import { makeChannel, renderRoute } from '../test/fixtures'
import { ChannelsPage } from './ChannelsPage'

afterEach(() => vi.unstubAllGlobals())

describe('ChannelsPage', () => {
  it('creates a write-only WeCom channel', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const create = vi.spyOn(api, 'createChannel').mockResolvedValue(makeChannel())
    renderRoute(<ChannelsPage channels={[]} logins={[]} api={api} runMutation={request => request()} />)
    await user.click(screen.getByRole('button', { name: '添加渠道' })); await user.type(screen.getByLabelText(/渠道名称/), '机器人'); await user.click(screen.getByLabelText('渠道类型')); await user.click(screen.getByRole('option', { name: '企业微信机器人' })); await user.type(screen.getByLabelText(/Webhook URL/), 'https://example.com/hook'); await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(create).toHaveBeenCalledWith({ id: undefined, name: '机器人', type: 'wecom', enabled: true, settings: {}, secrets: { webhook: 'https://example.com/hook' } }))
  })

  it('edits a channel without resending an unchanged secret', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const update = vi.spyOn(api, 'updateChannel').mockResolvedValue(makeChannel({ name: '修改' }))
    renderRoute(<ChannelsPage channels={[makeChannel({ settings: { custom: 'value' } })]} logins={[]} api={api} runMutation={request => request()} />)
    expect(screen.getByText('已安全保存')).toBeVisible(); await user.click(screen.getByRole('button', { name: '编辑' })); expect(screen.getByText('已安全保存；留空表示保留原值')).toBeVisible(); await user.clear(screen.getByLabelText(/渠道名称/)); await user.type(screen.getByLabelText(/渠道名称/), '修改'); await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(update).toHaveBeenCalledWith(expect.not.objectContaining({ secrets: expect.anything() })))
  })

  it('starts Microsoft authorization and opens the verification URL', async () => {
    const user = userEvent.setup(); const open = vi.fn(); vi.stubGlobal('open', open); const api = new AdminAPI('csrf'); vi.spyOn(api, 'startMicrosoftLogin').mockResolvedValue({ channel_id: 'ms', status: 'pending', verification_uri_complete: 'https://microsoft.example/login' })
    renderRoute(<ChannelsPage channels={[makeChannel({ id: 'ms', type: 'microsoft', settings: { authorized: 'false' }, configured_secrets: [] })]} logins={[]} api={api} runMutation={request => request()} />)
    await user.click(screen.getByRole('button', { name: '开始授权' }))
    await waitFor(() => expect(open).toHaveBeenCalledWith('https://microsoft.example/login', '_blank', 'noopener,noreferrer'))
  })

  it('cancels pending authorization and sends a channel test', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const cancel = vi.spyOn(api, 'cancelMicrosoftLogin').mockResolvedValue(undefined); const test = vi.spyOn(api, 'testChannel').mockResolvedValue({ status: 'sent' }); const channel = makeChannel({ id: 'ms', type: 'microsoft', settings: {}, configured_secrets: [] })
    renderRoute(<ChannelsPage channels={[channel]} logins={[{ channel_id: 'ms', status: 'pending', user_code: 'CODE' }]} api={api} runMutation={request => request()} />)
    expect(screen.getByText(/CODE/)).toBeVisible(); await user.click(screen.getByRole('button', { name: '取消' })); await user.click(screen.getByRole('button', { name: '发送测试' }))
    await waitFor(() => expect(cancel).toHaveBeenCalledWith('ms')); expect(test).toHaveBeenCalledWith('ms')
  })

  it.each([{ confirmed: false, calls: 0 }, { confirmed: true, calls: 1 }])('handles deletion confirmation=$confirmed', async ({ confirmed, calls }) => {
    const user = userEvent.setup(); vi.stubGlobal('confirm', vi.fn(() => confirmed)); const api = new AdminAPI('csrf'); const remove = vi.spyOn(api, 'deleteChannel').mockResolvedValue(undefined)
    renderRoute(<ChannelsPage channels={[makeChannel()]} logins={[]} api={api} runMutation={request => request()} />)
    await user.click(screen.getByRole('button', { name: '删除' })); await waitFor(() => expect(remove).toHaveBeenCalledTimes(calls))
  })
})
