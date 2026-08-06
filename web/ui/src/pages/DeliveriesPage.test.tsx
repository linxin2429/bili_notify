import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { AdminAPI } from '../api'
import { makeChannel, makeDelivery, renderRoute } from '../test/fixtures'
import { DeliveriesPage } from './DeliveriesPage'

describe('DeliveriesPage', () => {
  it('filters by URL and retries only blocked deliveries', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const retry = vi.spyOn(api, 'retryDelivery').mockResolvedValue({ status: 'queued' }); const refresh = vi.fn().mockResolvedValue(undefined)
    const deliveries = [makeDelivery(), makeDelivery({ id: 'pending', state: 'pending' })]
    renderRoute(<DeliveriesPage deliveries={deliveries} channels={[makeChannel()]} total={2} timeZone="Asia/Shanghai" api={api} runMutation={request => request()} refreshDashboard={refresh} />, '/deliveries?state=blocked')
    expect(screen.getByText('delivery')).toBeVisible(); expect(screen.queryByText('pending')).not.toBeInTheDocument(); expect(screen.getAllByRole('button', { name: /立即重试/ })).toHaveLength(1)
    await user.click(screen.getByRole('button', { name: /立即重试/ }))
    await waitFor(() => expect(retry).toHaveBeenCalledWith('delivery')); expect(refresh).toHaveBeenCalledOnce()
  })

  it('keeps retry usable after a failed mutation and does not refresh', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); vi.spyOn(api, 'retryDelivery').mockRejectedValue(new Error('failed')); const refresh = vi.fn()
    renderRoute(<DeliveriesPage deliveries={[makeDelivery()]} channels={[]} total={1} timeZone="" api={api} runMutation={request => request()} refreshDashboard={refresh} />, '/deliveries')
    await user.click(screen.getByRole('button', { name: /立即重试/ }))
    await waitFor(() => expect(screen.getByRole('button', { name: /立即重试/ })).toBeEnabled())
    expect(refresh).not.toHaveBeenCalled()
  })

  it('shows empty state for a filter without matching deliveries', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf')
    renderRoute(<DeliveriesPage deliveries={[makeDelivery({ state: 'pending' })]} channels={[]} total={1} timeZone="" api={api} runMutation={request => request()} refreshDashboard={vi.fn()} />, '/deliveries')
    await user.click(screen.getByRole('tab', { name: '已阻塞' }))
    expect(screen.getByText('当前筛选下没有待投递任务')).toBeVisible()
  })
})
