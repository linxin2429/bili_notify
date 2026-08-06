import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import App from './App'

vi.mock('./app/Console', () => ({
  Console: ({ csrf, themePreference, setThemePreference }: { csrf: string; themePreference: string; setThemePreference: (value: 'dark') => void }) => <div><span>console {csrf} {themePreference}</span><button onClick={() => setThemePreference('dark')}>mock theme</button></div>,
}))

afterEach(() => { vi.unstubAllGlobals(); window.localStorage.clear() })

describe('App', () => {
  it('loads an unauthenticated session and completes login', async () => {
    const user = userEvent.setup(); const fetchMock = vi.fn().mockResolvedValueOnce(new Response(JSON.stringify({ setup_required: false, authenticated: false }))).mockResolvedValueOnce(new Response(JSON.stringify({ csrf_token: 'csrf' }))); vi.stubGlobal('fetch', fetchMock)
    render(<App />)
    await user.type(await screen.findByLabelText('管理员密码'), 'password'); await user.click(screen.getByRole('button', { name: '登录' }))
    expect(await screen.findByText('console csrf system')).toBeVisible()
  })

  it('loads an authenticated session and persists theme changes', async () => {
    window.localStorage.setItem('theme', 'light'); vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ setup_required: false, authenticated: true, csrf_token: 'csrf' }))))
    render(<App />); expect(await screen.findByText('console csrf light')).toBeVisible(); await userEvent.click(screen.getByRole('button', { name: 'mock theme' })); expect(window.localStorage.getItem('theme')).toBe('dark')
  })

  it('keeps the loading screen and reports a session request error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new Error('offline')))
    render(<App />); expect(screen.getByText('正在连接 Bili Notify')).toBeVisible(); expect(await screen.findByText('offline')).toBeVisible()
  })
})
