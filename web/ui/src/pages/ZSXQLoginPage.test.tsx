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

const api = vi.hoisted(() => ({ updateZSXQCredential: vi.fn() }))
vi.mock('../shared/api/resources', () => ({ resources: api }))

describe('Knowledge Planet MCP credential', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.updateZSXQCredential.mockResolvedValue({ platform: 'zsxq', external_id: '7', display_name: '星球用户', status: 'connected' })
  })

  it('submits the Jasmine API key and clears it after success', async () => {
    const user = userEvent.setup(); renderLogin()
    expect(screen.getByRole('link', { name: 'Jasmine 密钥管理' })).toHaveAttribute('href', 'https://garden.zsxq.com/jasmine/')
    const input = screen.getByLabelText('Jasmine API 密钥')
    await user.type(input, 'opaque-secret')
    await user.click(screen.getByRole('button', { name: '连接或更新密钥' }))
    await waitFor(() => expect(api.updateZSXQCredential).toHaveBeenCalledWith('csrf', 'opaque-secret'))
    expect(input).toHaveValue('')
    expect(await screen.findByRole('heading', { name: '采集源列表' })).toBeInTheDocument()
  })

  it('shows the safe field-level API key error and preserves input', async () => {
    const user = userEvent.setup()
    api.updateZSXQCredential.mockRejectedValue(new ApiError('invalid', 'http', { fields: { api_key: '密钥无效或已过期' } }))
    renderLogin()
    await user.type(screen.getByLabelText('Jasmine API 密钥'), 'bad-key')
    await user.click(screen.getByRole('button', { name: '连接或更新密钥' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('密钥无效或已过期')
    expect(screen.getByLabelText('Jasmine API 密钥')).toHaveValue('bad-key')
  })
})

function renderLogin() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><ThemeProvider><NotificationProvider><SessionProvider value={{ csrf: 'csrf' }}><MemoryRouter initialEntries={['/integrations/zsxq-login']}><Routes><Route path="/integrations/zsxq-login" element={<ZSXQLoginPage />} /><Route path="/sources" element={<h1>采集源列表</h1>} /></Routes></MemoryRouter></SessionProvider></NotificationProvider></ThemeProvider></QueryClientProvider>)
}
