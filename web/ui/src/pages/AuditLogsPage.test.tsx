import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { AdminAPI } from '../api'
import { makeAudit, renderRoute } from '../test/fixtures'
import { AuditLogsPage } from './AuditLogsPage'

describe('AuditLogsPage', () => {
  it('queries URL filters, paginates, and reveals safe details', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const query = vi.spyOn(api, 'queryAuditLogs').mockResolvedValue({ items: [makeAudit({ details: { changed_setting_keys: ['password'] } })], total: 30, limit: 20, offset: 0 })
    renderRoute(<AuditLogsPage api={api} timeZone="Asia/Shanghai" refresh={0} />, '/audit-logs?outcome=success')
    await waitFor(() => expect(query).toHaveBeenCalledWith(expect.objectContaining({ outcome: 'success', limit: 20, offset: 0 })))
    await user.click(screen.getByRole('button', { name: '查看详情' }))
    expect(screen.getByText('request-42')).toBeVisible(); expect(screen.getByText(/password/)).toBeVisible()
    await user.click(screen.getByRole('button', { name: '下一页' }))
    await waitFor(() => expect(query).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 20 })))
  })

  it.each([
    { name: 'empty', result: { items: [], total: 0, limit: 20, offset: 0 }, error: undefined, want: '当前筛选下没有操作日志' },
    { name: 'error', result: undefined, error: new Error('query failed'), want: 'query failed' },
  ])('renders $name state', async ({ result, error, want }) => {
    const api = new AdminAPI('csrf'); const spy = vi.spyOn(api, 'queryAuditLogs'); if (error) spy.mockRejectedValue(error); else spy.mockResolvedValue(result!)
    renderRoute(<AuditLogsPage api={api} timeZone="" refresh={0} />, '/audit-logs')
    expect(await screen.findByText(want)).toBeVisible()
  })
})
