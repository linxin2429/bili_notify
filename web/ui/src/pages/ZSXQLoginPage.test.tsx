import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SessionProvider } from '../modules/session'
import { ApiError } from '../shared/api/errors'
import { NotificationProvider } from '../shared/ui'
import { ThemeProvider } from '../shared/ui/theme'
import { ZSXQLoginPage } from './ZSXQLoginPage'

const api = vi.hoisted(() => ({ importZSXQToken: vi.fn() }))
vi.mock('../shared/api/resources', () => ({ resources: api }))

describe('Knowledge Planet session import', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.importZSXQToken.mockResolvedValue({ platform: 'zsxq', external_id: '7', display_name: '星球用户', status: 'connected' })
  })

  it('submits the Cookie header and clears it after success', async () => {
    const user = userEvent.setup(); renderLogin()
    expect(screen.getByText(/api\.zsxq\.com/)).toBeInTheDocument()
    const input = screen.getByLabelText('Cookie 请求头值')
    await user.type(input, 'foo=bar; zsxq_access_token=secret')
    await user.click(screen.getByRole('button', { name: '导入 Session' }))
    await waitFor(() => expect(api.importZSXQToken).toHaveBeenCalledWith('csrf', 'foo=bar; zsxq_access_token=secret'))
    expect(input).toHaveValue('')
    expect(await screen.findByRole('heading', { name: '采集源列表' })).toBeInTheDocument()
  })

  it('shows the safe field-level Cookie error', async () => {
    const user = userEvent.setup()
    api.importZSXQToken.mockRejectedValue(new ApiError('invalid', 'http', { fields: { cookie: 'Cookie 中的 token 无效' } }))
    renderLogin()
    await user.type(screen.getByLabelText('Cookie 请求头值'), 'bad-cookie')
    await user.click(screen.getByRole('button', { name: '导入 Session' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Cookie 中的 token 无效')
    expect(screen.getByLabelText('Cookie 请求头值')).toHaveValue('bad-cookie')
  })
})

function renderLogin() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><ThemeProvider><NotificationProvider><SessionProvider value={{ csrf: 'csrf' }}><MemoryRouter initialEntries={['/integrations/zsxq-login']}><Routes><Route path="/integrations/zsxq-login" element={<ZSXQLoginPage />} /><Route path="/sources" element={<h1>采集源列表</h1>} /></Routes></MemoryRouter></SessionProvider></NotificationProvider></ThemeProvider></QueryClientProvider>)
}
