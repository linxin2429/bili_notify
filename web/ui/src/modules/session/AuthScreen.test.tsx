import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { NotificationProvider } from '../../shared/ui'
import { queryKeys } from '../../shared/api/query-keys'
import { ApiError } from '../../shared/api/errors'
import { AuthScreen } from './AuthScreen'

const api = vi.hoisted(() => ({ setup: vi.fn(), login: vi.fn() }))
vi.mock('../../shared/api/session', () => ({ sessionAPI: { ...api } }))

describe('AuthScreen', () => {
  beforeEach(() => { vi.clearAllMocks(); api.setup.mockResolvedValue({ csrf_token: 'setup-csrf' }); api.login.mockResolvedValue({ csrf_token: 'login-csrf' }) })

  it('validates setup confirmation and submits an uppercase one-time code', async () => {
    const user = userEvent.setup(); const { client } = renderAuth(true)
    await user.type(screen.getByLabelText('初始化码'), 'ab12')
    await user.type(screen.getByLabelText(/设置管理员密码/), 'long-password')
    await user.type(screen.getByLabelText('确认密码'), 'different')
    await user.click(screen.getByRole('button', { name: '初始化并登录' }))
    expect(screen.getByRole('alert')).toHaveTextContent('两次输入的密码不一致'); expect(api.setup).not.toHaveBeenCalled()
    await user.clear(screen.getByLabelText('确认密码')); await user.type(screen.getByLabelText('确认密码'), 'long-password')
    await user.click(screen.getByRole('button', { name: '初始化并登录' }))
    await waitFor(() => expect(api.setup).toHaveBeenCalledWith('AB12', 'long-password'))
    expect(client.getQueryData(queryKeys.session)).toEqual({ setup_required: false, authenticated: true, csrf_token: 'setup-csrf' })
  })

  it('logs in and exposes structured server failures', async () => {
    const user = userEvent.setup(); const { unmount } = renderAuth(false)
    expect(screen.getByRole('button', { name: '登录' })).toBeDisabled()
    await user.type(screen.getByLabelText('管理员密码'), 'password'); await user.click(screen.getByRole('button', { name: '登录' }))
    await waitFor(() => expect(api.login).toHaveBeenCalledWith('password'))
    unmount(); api.login.mockRejectedValue(new ApiError('密码错误', 'http', { requestId: 'req-login' })); renderAuth(false)
    await user.type(screen.getByLabelText('管理员密码'), 'wrong'); await user.click(screen.getByRole('button', { name: '登录' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('密码错误（请求 req-login）')
  })
})

function renderAuth(setup: boolean) {
  const client = new QueryClient({ defaultOptions: { mutations: { retry: false } } })
  return { client, ...render(<QueryClientProvider client={client}><NotificationProvider><AuthScreen setup={setup} /></NotificationProvider></QueryClientProvider>) }
}
