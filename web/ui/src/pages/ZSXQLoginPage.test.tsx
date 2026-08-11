import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SessionProvider } from '../modules/session'
import { NotificationProvider } from '../shared/ui'
import { ThemeProvider } from '../shared/ui/theme'
import { ZSXQLoginPage } from './ZSXQLoginPage'

const api = vi.hoisted(() => ({
  sendZSXQCode: vi.fn(),
  createZSXQSession: vi.fn(),
}))

vi.mock('../shared/api/resources', () => ({ resources: api }))

describe('Knowledge Planet login', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    window.__zsxqCaptcha = { verify: vi.fn().mockResolvedValue('captcha-token') }
    api.sendZSXQCode.mockResolvedValue({ id: 'transaction', masked_phone: '+852 5****678', expires_at: '2026-08-10T10:10:00Z', retry_after_sec: 120, attempts_remaining: 5 })
    api.createZSXQSession.mockResolvedValue({ platform: 'zsxq', external_id: '7', display_name: '星球用户', status: 'connected' })
  })

  it('requires agreement, sends the captcha result and completes SMS login', async () => {
    const user = userEvent.setup()
    renderLogin()
    const send = screen.getByRole('button', { name: '启动滑块并发送验证码' })
    expect(send).toBeDisabled()

    await user.selectOptions(screen.getByLabelText('国家或地区码'), '+852')
    await user.type(screen.getByLabelText('手机号'), '512345678')
    await user.click(screen.getByRole('checkbox'))
    await user.click(send)

    await waitFor(() => expect(api.sendZSXQCode).toHaveBeenCalledWith('csrf', {
      country_code: '+852',
      phone: '512345678',
      captcha_verify_param: 'captcha-token',
      agreement_accepted: true,
    }))
    expect(await screen.findByText('验证码已发送至 +852 5****678')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: /重新发送/ })).toBeDisabled()
    expect(screen.getByRole('button', { name: '更换手机号' })).toBeInTheDocument()

    await user.type(screen.getByLabelText('短信验证码'), '123456')
    await user.click(screen.getByRole('button', { name: '登录并同步星球' }))
    await waitFor(() => expect(api.createZSXQSession).toHaveBeenCalledWith('csrf', 'transaction', '123456'))
    expect(await screen.findByRole('heading', { name: '采集源列表' })).toBeInTheDocument()
    expect(screen.getByText('知识星球登录成功')).toBeInTheDocument()
  })

  it('allows changing the phone number after SMS is sent', async () => {
    const user = userEvent.setup()
    renderLogin()
    await user.type(screen.getByLabelText('手机号'), '13800138000')
    await user.click(screen.getByRole('checkbox'))
    await user.click(screen.getByRole('button', { name: '启动滑块并发送验证码' }))
    expect(await screen.findByText(/验证码已发送至/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '更换手机号' }))
    expect(screen.getByLabelText('手机号')).toBeInTheDocument()
    expect(screen.queryByLabelText('短信验证码')).not.toBeInTheDocument()
  })

  it('shows captcha and upstream failures without advancing the transaction', async () => {
    const user = userEvent.setup()
    window.__zsxqCaptcha = { verify: vi.fn().mockRejectedValue(new Error('滑块失败')) }
    renderLogin()
    await user.type(screen.getByLabelText('手机号'), '13800138000')
    await user.click(screen.getByRole('checkbox'))
    await user.click(screen.getByRole('button', { name: '启动滑块并发送验证码' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('滑块失败')
    expect(api.sendZSXQCode).not.toHaveBeenCalled()

    window.__zsxqCaptcha = { verify: vi.fn().mockResolvedValue('captcha-token') }
    api.sendZSXQCode.mockRejectedValueOnce(new Error('号码未绑定'))
    await user.click(screen.getByRole('button', { name: '启动滑块并发送验证码' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('号码未绑定')
    expect(screen.getByLabelText('手机号')).toBeInTheDocument()
  })
})

function renderLogin() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <ThemeProvider><NotificationProvider><SessionProvider value={{ csrf: 'csrf' }}>
        <MemoryRouter initialEntries={['/integrations/zsxq-login']}>
          <Routes>
            <Route path="/integrations/zsxq-login" element={<ZSXQLoginPage />} />
            <Route path="/sources" element={<h1>采集源列表</h1>} />
          </Routes>
        </MemoryRouter>
      </SessionProvider></NotificationProvider></ThemeProvider>
    </QueryClientProvider>,
  )
}
