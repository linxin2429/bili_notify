import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AdminAPI } from '../api'
import { makeUP, renderRoute } from '../test/fixtures'
import { UPsPage } from './UPsPage'

afterEach(() => vi.unstubAllGlobals())

describe('UPsPage', () => {
  it('creates an UP from the empty state', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const create = vi.spyOn(api, 'createUP').mockResolvedValue(makeUP())
    renderRoute(<UPsPage ups={[]} timeZone="" api={api} runMutation={request => request()} />)
    await user.click(screen.getByRole('button', { name: '添加第一个 UP 主' }))
    await user.type(screen.getByLabelText(/UID/), '42'); await user.type(screen.getByLabelText('备注名'), '测试 UP'); await user.click(screen.getByRole('button', { name: '保存' }))
    await waitFor(() => expect(create).toHaveBeenCalledWith({ uid: '42', name: '测试 UP', enabled: true }))
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('edits existing state and displays mutation errors', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); vi.spyOn(api, 'updateUP').mockRejectedValue(new Error('duplicate'))
    renderRoute(<UPsPage ups={[makeUP({ last_error: 'poll failed', consecutive_fail: 2 })]} timeZone="Asia/Shanghai" api={api} runMutation={request => request()} />)
    expect(screen.getByText('poll failed')).toBeVisible(); expect(screen.getByText('连续失败 2 次')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '编辑' })); expect(screen.getByLabelText(/UID/)).toBeDisabled(); await user.clear(screen.getByLabelText('备注名')); await user.type(screen.getByLabelText('备注名'), '修改'); await user.click(screen.getByRole('button', { name: '保存' }))
    expect(await screen.findByText('duplicate')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '取消' })); await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it.each([
    { confirmed: false, calls: 0 }, { confirmed: true, calls: 1 },
  ])('handles delete confirmation=$confirmed', async ({ confirmed, calls }) => {
    const user = userEvent.setup(); vi.stubGlobal('confirm', vi.fn(() => confirmed)); const api = new AdminAPI('csrf'); const remove = vi.spyOn(api, 'deleteUP').mockResolvedValue(undefined)
    renderRoute(<UPsPage ups={[makeUP()]} timeZone="" api={api} runMutation={request => request()} />)
    await user.click(screen.getByRole('button', { name: '删除' })); await waitFor(() => expect(remove).toHaveBeenCalledTimes(calls))
  })
})
