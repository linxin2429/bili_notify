import React from 'react'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it, vi } from 'vitest'
import { AdminAPI } from '../api'
import { makeDynamic, makeUP, renderRoute } from '../test/fixtures'
import { HistoryPage } from './HistoryPage'

describe('HistoryPage', () => {
  it('keeps the newest result when an older request resolves last', async () => {
    const api = new AdminAPI('csrf')
    let resolveOld!: (value: Awaited<ReturnType<AdminAPI['queryDynamics']>>) => void
    const old = new Promise<Awaited<ReturnType<AdminAPI['queryDynamics']>>>(resolve => { resolveOld = resolve })
    const query = vi.spyOn(api, 'queryDynamics').mockReturnValueOnce(old).mockResolvedValueOnce({ items: [makeDynamic({ id: 'new', summary: 'new result' })], total: 1, limit: 20, offset: 0 })
    function Harness() { const [refresh, setRefresh] = React.useState(0); return <><button onClick={() => setRefresh(1)}>refresh</button><HistoryPage ups={[]} timeZone="" api={api} refresh={refresh} /></> }
    renderRoute(<Harness />, '/history')
    await waitFor(() => expect(query).toHaveBeenCalledOnce())
    await userEvent.click(screen.getByRole('button', { name: 'refresh' }))
    expect(await screen.findByText('new result')).toBeVisible()
    resolveOld({ items: [makeDynamic({ id: 'old', summary: 'old result' })], total: 1, limit: 20, offset: 0 })
    await waitFor(() => expect(screen.queryByText('old result')).not.toBeInTheDocument())
    expect(screen.getByText('new result')).toBeVisible()
  })

  it('queries dynamics from URL filters and paginates', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const query = vi.spyOn(api, 'queryDynamics').mockResolvedValue({ items: [makeDynamic({ summary: 'archived' })], total: 21, limit: 20, offset: 0 })
    renderRoute(<HistoryPage ups={[makeUP()]} timeZone="Asia/Shanghai" api={api} refresh={0} />, '/history?uid=42')
    await waitFor(() => expect(query).toHaveBeenCalledWith(expect.objectContaining({ uid: '42', limit: 20, offset: 0 }))); expect(screen.getByText('archived')).toBeVisible()
    await user.click(screen.getByRole('button', { name: '下一页' })); await waitFor(() => expect(query).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 20 })))
  })

  it('debounces keyword filters and converts date inputs', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); const query = vi.spyOn(api, 'queryDynamics').mockResolvedValue({ items: [], total: 0, limit: 20, offset: 0 })
    renderRoute(<HistoryPage ups={[]} timeZone="" api={api} refresh={0} />, '/history')
    await user.type(screen.getByLabelText('关键字'), '测试')
    await waitFor(() => expect(query).toHaveBeenLastCalledWith(expect.objectContaining({ q: '测试' })), { timeout: 1_000 })
    fireEvent.change(screen.getByLabelText('开始时间'), { target: { value: '2026-08-06T08:00' } })
    await waitFor(() => expect(query).toHaveBeenLastCalledWith(expect.objectContaining({ from: expect.stringContaining('2026-08-06') })))
  })

  it('loads comment history and opens the thread detail', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); vi.spyOn(api, 'queryComments').mockResolvedValue({ items: [{ rpid: '1', up_uid: '42', up_name: 'UP', content_title: '回复目标', published_at: '2026-08-06T00:00:00Z', discovered_at: '2026-08-06T00:00:01Z', baseline: false, incomplete: true }], total: 1, limit: 20, offset: 0 }); const detail = vi.spyOn(api, 'getComment').mockResolvedValue({ rpid: '1', up_uid: '42', up_name: 'UP', content_type: 'video', content_id: 'BV', content_title: '回复目标', content_url: 'https://example.com', published_at: '2026-08-06T00:00:00Z', incomplete: true, thread: [{ rpid: '1', mid: '1', name: 'UP', message: '回复正文', time: '2026-08-06T00:00:00Z', is_up: true, is_trigger: true }] })
    renderRoute(<HistoryPage ups={[]} timeZone="" api={api} refresh={0} />, '/history?tab=comments')
    const open = await screen.findByRole('button', { name: '查看评论对话：回复目标' })
    open.focus(); await user.keyboard('{Enter}'); await waitFor(() => expect(detail).toHaveBeenCalledWith('1')); expect(screen.getByText('回复正文')).toBeVisible(); expect(screen.getByText('对话串可能不完整（翻页窗口外）。')).toBeVisible(); await user.keyboard('{Escape}'); await waitFor(() => expect(screen.queryByText('回复正文')).not.toBeInTheDocument()); expect(open).toHaveFocus()
  })

  it.each([
    { name: 'empty', error: undefined, want: '当前筛选下没有历史记录' },
    { name: 'query error', error: new Error('history failed'), want: 'history failed' },
  ])('renders $name state', async ({ error, want }) => {
    const api = new AdminAPI('csrf'); const query = vi.spyOn(api, 'queryDynamics'); if (error) query.mockRejectedValue(error); else query.mockResolvedValue({ items: [], total: 0, limit: 20, offset: 0 })
    renderRoute(<HistoryPage ups={[]} timeZone="" api={api} refresh={0} />, '/history')
    expect(await screen.findByText(want)).toBeVisible()
  })

  it('reports comment detail errors', async () => {
    const user = userEvent.setup(); const api = new AdminAPI('csrf'); vi.spyOn(api, 'queryComments').mockResolvedValue({ items: [{ rpid: '1', up_uid: '42', up_name: 'UP', content_title: '回复条目', published_at: '2026-08-06T00:00:00Z', discovered_at: '2026-08-06T00:00:01Z', baseline: false }], total: 1, limit: 20, offset: 0 }); vi.spyOn(api, 'getComment').mockRejectedValue(new Error('detail failed'))
    renderRoute(<HistoryPage ups={[]} timeZone="" api={api} refresh={0} />, '/history?tab=comments')
    await user.click(await screen.findByText('回复条目')); expect(await screen.findByText('detail failed')).toBeVisible()
  })
})
