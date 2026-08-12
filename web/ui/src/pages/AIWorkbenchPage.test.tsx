import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter } from 'react-router-dom'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { SessionProvider } from '../modules/session'
import { NotificationProvider } from '../shared/ui'
import type { AIJob } from '../shared/api/types'
import { AIWorkbenchPage } from './AIWorkbenchPage'

const api = vi.hoisted(() => ({
  aiStatus: vi.fn(), aiProfiles: vi.fn(), aiPrompts: vi.fn(), aiJobs: vi.fn(), aiJob: vi.fn(),
  createAITranscription: vi.fn(), createAISummary: vi.fn(), cancelAIJob: vi.fn(), retryAIJob: vi.fn(), deleteAIJob: vi.fn(),
}))
vi.mock('../shared/api/resources', () => ({ resources: api }))

const transcriptionProfile = {
  id: 'transcription-profile', name: '转写模型', kind: 'transcription' as const, base_url: 'https://example.com/v1', model: 'whisper',
  timeout_sec: 600, enabled: true, default: true, configured_secrets: ['api_key'], created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z',
}
const textProfile = { ...transcriptionProfile, id: 'text-profile', name: '总结模型', kind: 'text' as const, model: 'gpt', max_output_tokens: 4096 }
const prompt = { id: 'prompt', name: '默认总结', system_prompt: 'system', chunk_prompt: 'chunk', reduce_prompt: 'reduce', default: true, created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z' }

function makeJob(overrides: Partial<AIJob> = {}): AIJob {
  return {
    id: 'job', kind: 'transcription', state: 'queued', stage: '等待执行', progress: 0, profile_id: transcriptionProfile.id,
    origin: 'workbench', attempts: 0, created_at: '2026-08-09T10:00:00Z', updated_at: '2026-08-09T10:00:00Z', ...overrides,
  }
}

describe('AI workbench', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    vi.spyOn(crypto, 'randomUUID').mockReturnValue('00000000-0000-4000-8000-000000000001')
    api.aiStatus.mockResolvedValue({ connected: true, yt_dlp_available: true, ffmpeg_available: true, active_transcriptions: 0, active_summaries: 0, cache_bytes: 0 })
    api.aiProfiles.mockResolvedValue([transcriptionProfile, textProfile])
    api.aiPrompts.mockResolvedValue([prompt])
    api.aiJobs.mockResolvedValue({ items: [], total: 0, limit: 50, offset: 0 })
    api.createAITranscription.mockImplementation(async (_csrf: string, input: object) => makeJob({ id: 'created-transcription', transcription_input: input as { bvid: string; page?: number } }))
    api.createAISummary.mockResolvedValue(makeJob({ id: 'created-summary', kind: 'summary' }))
    api.cancelAIJob.mockResolvedValue({ status: 'canceled' })
    api.retryAIJob.mockResolvedValue({ status: 'queued' })
    api.deleteAIJob.mockResolvedValue(undefined)
  })

  it('submits a video transcription with the selected defaults', async () => {
    const user = userEvent.setup()
    renderPage()

    expect(await screen.findByText('还没有 AI 任务')).toBeInTheDocument()
    await user.type(screen.getByLabelText('BVID'), '  BV1TEST  ')
    await user.clear(screen.getByLabelText('分 P'))
    await user.type(screen.getByLabelText('分 P'), '2')
    await user.click(screen.getByRole('button', { name: '提交任务' }))

    await waitFor(() => expect(api.createAITranscription).toHaveBeenCalledWith('csrf', {
      client_request_id: '00000000-0000-4000-8000-000000000001', bvid: 'BV1TEST', page: 2, profile_id: transcriptionProfile.id,
    }))
    expect(await screen.findByText('任务已提交')).toBeInTheDocument()
  })

  it.each([
    { name: 'direct text', selectSource: false, expected: { text: '需要总结的正文' } },
    { name: 'completed transcription', selectSource: true, expected: { transcription_job_id: 'source-job' } },
  ])('submits a summary from $name', async ({ selectSource, expected }) => {
    api.aiJobs.mockResolvedValue({ items: [makeJob({ id: 'source-job', state: 'succeeded', progress: 100 })], total: 1, limit: 50, offset: 0 })
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole('tab', { name: '文本总结' }))
    if (selectSource) await user.selectOptions(screen.getByLabelText('来源转写（可选）'), 'source-job')
    else await user.type(screen.getByLabelText('待总结文本'), '  需要总结的正文  ')
    await user.click(screen.getByRole('button', { name: '提交任务' }))

    await waitFor(() => expect(api.createAISummary).toHaveBeenCalledWith('csrf', {
      client_request_id: '00000000-0000-4000-8000-000000000001', ...expected, profile_id: textProfile.id, prompt_id: prompt.id,
    }))
  })

  it('shows detailed results and runs cancel, retry, and delete actions', async () => {
    const jobs = [
      makeJob({ id: 'running', state: 'running', stage: '下载视频', progress: 30 }),
      makeJob({ id: 'failed', kind: 'summary', state: 'failed', stage: '调用模型', progress: 80, attempts: 2 }),
      makeJob({ id: 'succeeded', state: 'succeeded', stage: '完成', progress: 100 }),
    ]
    api.aiJobs.mockResolvedValue({ items: jobs, total: jobs.length, limit: 50, offset: 0 })
    api.aiJob.mockImplementation(async (id: string) => {
      if (id === 'failed') return makeJob({ ...jobs[1], error_code: 'provider_error', last_error: '模型调用失败' })
      if (id === 'succeeded') return makeJob({ ...jobs[2], transcription_result: { bvid: 'BV1RESULT', title: '视频标题', pages: [{ page: 1, title: '第一集', duration_ms: 90_000, segments: [{ start_ms: 65_000, end_ms: 70_000, text: '转写内容' }] }] } })
      return jobs[0]
    })
    const user = userEvent.setup()
    renderPage()

    await user.click(await screen.findByRole('button', { name: /处理中.*视频转写/ }))
    await user.click(await screen.findByRole('button', { name: '取消任务' }))
    await waitFor(() => expect(api.cancelAIJob).toHaveBeenCalledWith('csrf', 'running'))

    await user.click(screen.getByRole('button', { name: /文本总结/ }))
    expect(await screen.findByText('模型调用失败')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '重新执行' }))
    await waitFor(() => expect(api.retryAIJob).toHaveBeenCalledWith('csrf', 'failed'))
    await user.click(screen.getByRole('button', { name: '删除记录' }))
    expect(screen.getByRole('dialog', { name: '删除 AI 任务记录' })).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '确认删除' }))
    await waitFor(() => expect(api.deleteAIJob).toHaveBeenCalledWith('csrf', 'failed'))

    await user.click(screen.getByRole('button', { name: /已完成.*视频转写/ }))
    expect(await screen.findByText('转写内容')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '01:05' })).toHaveAttribute('href', 'https://www.bilibili.com/video/BV1RESULT?p=1&t=65')
  })

  it('explains unavailable dependencies and retries failed initial queries', async () => {
    api.aiStatus.mockResolvedValue({ connected: false, yt_dlp_available: false, ffmpeg_available: false, active_transcriptions: 0, active_summaries: 0, cache_bytes: 0 })
    api.aiProfiles.mockResolvedValue([])
    api.aiPrompts.mockResolvedValue([])
    const view = renderPage()

    expect(await screen.findByText(/AI Worker 当前不可用/)).toBeInTheDocument()
    expect(screen.getByText(/AI 设置/)).toBeInTheDocument()
    view.unmount()

    const callsBeforeRetryScenario = api.aiProfiles.mock.calls.length
    api.aiProfiles.mockRejectedValueOnce(new Error('配置档加载失败')).mockResolvedValueOnce([transcriptionProfile])
    const user = userEvent.setup()
    renderPage()
    expect(await screen.findByText('配置档加载失败')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '重试' }))
    expect(await screen.findByLabelText('BVID')).toBeInTheDocument()
    expect(api.aiProfiles).toHaveBeenCalledTimes(callsBeforeRetryScenario + 2)
  })
})

function renderPage() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } })
  return render(<QueryClientProvider client={client}><NotificationProvider><SessionProvider value={{ csrf: 'csrf' }}><MemoryRouter><AIWorkbenchPage /></MemoryRouter></SessionProvider></NotificationProvider></QueryClientProvider>)
}
