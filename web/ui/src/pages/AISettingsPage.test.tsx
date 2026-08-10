import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SessionProvider } from '../modules/session'
import { NotificationProvider } from '../shared/ui'
import { AISettingsPage } from './AISettingsPage'

const api = vi.hoisted(() => ({
  aiStatus: vi.fn(), aiProfiles: vi.fn(), aiPrompts: vi.fn(), updateAIProfile: vi.fn(),
  createAIProfile: vi.fn(), deleteAIProfile: vi.fn(), testAIProfile: vi.fn(),
  createAIPrompt: vi.fn(), updateAIPrompt: vi.fn(), deleteAIPrompt: vi.fn(),
}))
vi.mock('../shared/api/resources', () => ({ resources: api }))

const profile = {
  id: 'profile', name: '转写', kind: 'transcription' as const, base_url: 'https://example.com/v1',
  model: 'gpt-transcribe', language: 'zh', prompt: '', temperature: 0.2, timeout_sec: 600, default: true,
  configured_secrets: ['api_key'], created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T11:00:00Z',
}

describe('AI profile editor', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.aiStatus.mockResolvedValue({ connected: true, yt_dlp_available: true, ffmpeg_available: true, active_transcriptions: 0, active_summaries: 0, cache_bytes: 0 })
    api.aiProfiles.mockResolvedValue([profile])
    api.aiPrompts.mockResolvedValue([])
    api.updateAIProfile.mockResolvedValue(profile)
  })

  it('submits only editable fields when updating a profile', async () => {
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole('button', { name: '编辑' }))
    await user.click(screen.getAllByRole('button', { name: '保存' })[0])

    await waitFor(() => expect(api.updateAIProfile).toHaveBeenCalledWith('csrf', {
      id: 'profile',
      name: '转写',
      kind: 'transcription',
      base_url: 'https://example.com/v1',
      model: 'gpt-transcribe',
      api_key: '',
      language: 'zh',
      prompt: '',
      temperature: 0.2,
      max_output_tokens: undefined,
      context_window_chars: undefined,
      timeout_sec: 600,
      default: true,
    }))
    expect(api.updateAIProfile.mock.calls[0]?.[1]).not.toHaveProperty('configured_secrets')
    expect(api.updateAIProfile.mock.calls[0]?.[1]).not.toHaveProperty('created_at')
    expect(api.updateAIProfile.mock.calls[0]?.[1]).not.toHaveProperty('updated_at')
  })
})

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><NotificationProvider><SessionProvider value={{ csrf: 'csrf' }}><AISettingsPage /></SessionProvider></NotificationProvider></QueryClientProvider>)
}
