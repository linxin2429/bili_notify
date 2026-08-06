import { act, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { RealtimeCallbacks } from '../realtime'
import { AdminAPI } from '../api'
import { makeSnapshot, makeUP, renderRoute } from '../test/fixtures'
import { activateNavigation, canApplyDashboardRefresh, Console } from './Console'

const realtime = vi.hoisted(() => ({ callbacks: undefined as RealtimeCallbacks | undefined, start: vi.fn(), stop: vi.fn() }))
vi.mock('../realtime', () => ({ RealtimeClient: class { constructor(callbacks: RealtimeCallbacks) { realtime.callbacks = callbacks } start() { realtime.start() } stop() { realtime.stop() } } }))

afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); realtime.callbacks = undefined; realtime.start.mockReset(); realtime.stop.mockReset() })

describe('Console state helpers', () => {
  it.each([
    { active: '/history', target: '/history', navigations: 0 }, { active: '/overview', target: '/history', navigations: 1 },
  ])('activates $target from $active', ({ active, target, navigations }) => { const navigate = vi.fn(); const refresh = vi.fn(); activateNavigation(active, target, navigate, refresh); expect(navigate).toHaveBeenCalledTimes(navigations); expect(refresh).toHaveBeenCalledOnce() })
  it.each([
    { request: 2, latest: 2, version: 5, current: 5, want: true }, { request: 1, latest: 2, version: 5, current: 5, want: false }, { request: 2, latest: 2, version: 4, current: 5, want: false },
  ])('guards refresh response', ({ request, latest, version, current, want }) => expect(canApplyDashboardRefresh(request, latest, version, current)).toBe(want))
})

describe('Console', () => {
  it('renders the mobile navigation shell', async () => {
    vi.stubGlobal('matchMedia', (query: string) => ({ matches: true, media: query, onchange: null, addListener: vi.fn(), removeListener: vi.fn(), addEventListener: vi.fn(), removeEventListener: vi.fn(), dispatchEvent: vi.fn() }))
    const user = userEvent.setup()
    renderRoute(<Console csrf="csrf" themePreference="system" setThemePreference={vi.fn()} onAuthLost={vi.fn()} />, '/overview')
    act(() => realtime.callbacks?.onSnapshot(makeSnapshot()))
    expect(screen.getByLabelText('打开导航')).toBeVisible()
    await user.click(screen.getByLabelText('打开导航'))
    expect(screen.getAllByText('操作日志').length).toBeGreaterThan(0)
    expect(screen.getAllByRole('button', { name: '历史' }).length).toBeGreaterThan(0)
  })

  it('renders realtime snapshots, events, stale state, navigation, and theme changes', async () => {
    const user = userEvent.setup(); const setTheme = vi.fn(); const dashboard = vi.spyOn(AdminAPI.prototype, 'dashboard').mockResolvedValue(makeSnapshot()); const onAuthLost = vi.fn()
    renderRoute(<Console csrf="csrf" themePreference="system" setThemePreference={setTheme} onAuthLost={onAuthLost} />, '/overview')
    expect(realtime.start).toHaveBeenCalledOnce(); expect(screen.getByText('正在加载实时状态')).toBeVisible()
    act(() => realtime.callbacks?.onSnapshot(makeSnapshot()))
    expect(await screen.findByRole('heading', { name: '运行概览' })).toBeVisible()
    act(() => realtime.callbacks?.onState('live')); expect(screen.getByText('实时', { exact: true })).toBeVisible()
    act(() => realtime.callbacks?.onEvent('ups.updated', [makeUP({ name: '事件 UP' })], 2))
    expect(makeUP({ name: '事件 UP' }).name).toBe('事件 UP')
    await user.click(screen.getByLabelText('切换主题')); expect(setTheme).toHaveBeenCalledWith('light')
    act(() => realtime.callbacks?.onState('stale')); expect(screen.getByText(/实时连接已中断/)).toBeVisible()
    await user.click(screen.getByRole('button', { name: /^UP 主$/ })); await waitFor(() => expect(dashboard).toHaveBeenCalledOnce()); expect(await screen.findByText('测试 UP')).toBeVisible()
  })

  it('reports realtime errors, forwards auth loss, logs out, and stops on unmount', async () => {
    const user = userEvent.setup(); vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(null, { status: 204 }))); const onAuthLost = vi.fn(); const view = renderRoute(<Console csrf="csrf" themePreference="dark" setThemePreference={vi.fn()} onAuthLost={onAuthLost} />, '/overview')
    act(() => realtime.callbacks?.onError('bad event')); expect(screen.getByText('bad event')).toBeVisible(); act(() => realtime.callbacks?.onAuthLost()); expect(onAuthLost).toHaveBeenCalledOnce()
    await user.click(screen.getByLabelText('退出登录')); await waitFor(() => expect(onAuthLost).toHaveBeenCalledTimes(2)); expect(fetch).toHaveBeenCalledWith('/api/v1/session', expect.objectContaining({ method: 'DELETE' }))
    const stops = realtime.stop.mock.calls.length; view.unmount(); expect(realtime.stop).toHaveBeenCalledTimes(stops + 1)
  })
})
