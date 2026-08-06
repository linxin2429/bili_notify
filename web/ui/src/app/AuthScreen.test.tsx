import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { AuthScreen } from './AuthScreen'
import { renderRoute } from '../test/fixtures'

afterEach(() => vi.unstubAllGlobals())

describe('AuthScreen', () => {
  it('validates setup confirmation before requesting', async () => {
    const user = userEvent.setup(); const fetchMock = vi.fn(); vi.stubGlobal('fetch', fetchMock)
    renderRoute(<AuthScreen setup onAuthenticated={vi.fn()} />)
    await user.type(screen.getByLabelText('初始化码'), 'abc123'); expect(screen.getByLabelText('初始化码')).toHaveValue('ABC123'); await user.type(screen.getByLabelText('设置管理员密码'), 'password-one'); await user.type(screen.getByLabelText('确认密码'), 'password-two'); await user.click(screen.getByRole('button', { name: '初始化并登录' }))
    expect(screen.getByText('两次输入的密码不一致')).toBeVisible(); expect(fetchMock).not.toHaveBeenCalled()
  })

  it.each([
    { setup: false, button: '登录', passwordLabel: '管理员密码', path: '/api/v1/session' },
    { setup: true, button: '初始化并登录', passwordLabel: '设置管理员密码', path: '/api/v1/setup' },
  ])('submits setup=$setup', async ({ setup, button, passwordLabel, path }) => {
    const user = userEvent.setup(); vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ csrf_token: 'token' })))); const authenticated = vi.fn()
    renderRoute(<AuthScreen setup={setup} onAuthenticated={authenticated} />)
    if (setup) { await user.type(screen.getByLabelText('初始化码'), 'code'); await user.type(screen.getByLabelText('确认密码'), 'password') }
    await user.type(screen.getByLabelText(passwordLabel), 'password'); await user.click(screen.getByRole('button', { name: button }))
    await waitFor(() => expect(authenticated).toHaveBeenCalledWith({ csrf_token: 'token' })); expect(fetch).toHaveBeenCalledWith(path, expect.objectContaining({ method: 'POST' }))
  })

  it('submits login with Enter and displays API errors', async () => {
    const user = userEvent.setup(); vi.stubGlobal('fetch', vi.fn().mockResolvedValue(new Response(JSON.stringify({ error: { message: 'bad login' } }), { status: 401 })))
    renderRoute(<AuthScreen setup={false} onAuthenticated={vi.fn()} />)
    await user.type(screen.getByLabelText('管理员密码'), 'wrong{Enter}')
    expect(await screen.findByText('bad login')).toBeVisible()
  })
})
