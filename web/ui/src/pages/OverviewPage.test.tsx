import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { AdminAPI } from '../api'
import { makeSnapshot, renderRoute } from '../test/fixtures'
import { OverviewPage } from './OverviewPage'

describe('OverviewPage', () => {
  it('shows readiness, account, risk, and queue evidence', () => {
    const snapshot = makeSnapshot({ status: { ...makeSnapshot().status, bili_account: { uid: '100', name: 'Account' }, risk_paused_until: '2026-08-07T00:00:00Z', outbox_depth: 2, oldest_delivery: '2026-08-06T00:00:00Z', ready: false } })
    renderRoute(<OverviewPage snapshot={snapshot} api={new AdminAPI('csrf')} runMutation={request => request()} />)
    expect(screen.getByText('服务尚未就绪')).toBeVisible(); expect(screen.getByText(/Account · UID 100/)).toBeVisible(); expect(screen.getByText(/风控暂停至/)).toBeVisible(); expect(screen.getByText('2')).toBeVisible()
  })

  it('starts and cancels a QR login', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const start = vi.spyOn(api, 'startBiliLogin').mockResolvedValue({ id: 'login', status: 'waiting', expires_at: '2026-08-06T12:05:00Z' }); const cancel = vi.spyOn(api, 'cancelBiliLogin').mockResolvedValue(undefined)
    const { rerender } = renderRoute(<OverviewPage snapshot={makeSnapshot({ bili_login: null })} api={api} runMutation={request => request()} />)
    await user.click(screen.getByRole('button', { name: '生成登录二维码' })); await waitFor(() => expect(start).toHaveBeenCalledOnce())
    rerender(<OverviewPage snapshot={makeSnapshot({ bili_login: { id: 'login', status: 'waiting', expires_at: '2026-08-06T12:05:00Z' } })} api={api} runMutation={request => request()} />)
    await user.click(screen.getByRole('button', { name: '取消本次登录' })); expect(cancel).toHaveBeenCalledWith('login')
  })
})
