import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { UnifiedContent } from '../../shared/api/types'
import { HistoryCard } from './HistoryCard'

const api = vi.hoisted(() => ({
  content: vi.fn(),
  contentComments: vi.fn(),
}))
vi.mock('../../shared/api/resources', () => ({ resources: {
  content: (id: string, signal?: AbortSignal) => api.content(id, signal),
  contentComments: (id: string, signal?: AbortSignal) => api.contentComments(id, signal),
} }))

const base: UnifiedContent = {
  id: 'bilibili:content:dynamic',
  platform: 'bilibili',
  source_id: 'bilibili:up:42',
  external_id: 'dynamic',
  author_id: '42',
  author_name: '测试 UP',
  upstream_type: 'DYNAMIC_TYPE_WORD',
  type: 'dynamic',
  title: '一条测试动态',
  text: '正文',
  published_at: '2026-08-09T10:00:00Z',
  first_seen_at: '2026-08-09T10:00:01Z',
  last_synced_at: '2026-08-09T10:00:01Z',
  baseline: false,
  url: 'https://www.bilibili.com/video/BV1xx411c7mD',
  stats: { forwards: 1, comments: 2, likes: 3, rewards: 4 },
}

function renderCard(item: UnifiedContent = base) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <HistoryCard item={item} timeZone="Asia/Shanghai" sourceName="测试 UP" />
    </QueryClientProvider>,
  )
}

describe('HistoryCard', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    api.content.mockResolvedValue({ content: base, attachments: [
      { id: 'img', content_id: base.id, external_id: 'img', type: 'image', localized: true, file_name: 'a.jpg' },
      { id: 'file', content_id: base.id, external_id: 'file', type: 'file', file_name: '资料.pdf', size: 2048, localized: true, localize_error: '预算不足' },
    ] })
    api.contentComments.mockResolvedValue({ children: [], incomplete: false })
  })

  it('renders feed body, stats and expands media attachments', async () => {
    const user = userEvent.setup()
    renderCard()
    expect(screen.getByRole('heading', { level: 2, name: '一条测试动态' })).toBeInTheDocument()
    expect(screen.getByText('正文')).toBeInTheDocument()
    expect(screen.getByLabelText('转发 1')).toBeInTheDocument()
    expect(screen.getByLabelText('点赞 3')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /查看原内容/ })).toHaveAttribute('href', expect.stringContaining('BV1xx411c7mD'))
    await user.click(screen.getByRole('button', { name: '媒体与附件' }))
    expect(await screen.findByLabelText('放大第 1 张图片')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: '资料.pdf' })).toBeInTheDocument()
    expect(screen.getByText(/预算不足/)).toBeInTheDocument()
  })

  it('renders video landing cards and opens embed dialog', async () => {
    const user = userEvent.setup()
    const video: UnifiedContent = {
      ...base,
      id: 'bilibili:content:video',
      type: 'video',
      upstream_type: 'DYNAMIC_TYPE_AV',
      title: '测试视频',
      text: '视频简介',
      url: 'https://www.bilibili.com/video/BV1xx411c7mD',
      stats: { views: 12_000, likes: 8 },
    }
    renderCard(video)
    expect(screen.getByText('测试视频')).toBeInTheDocument()
    await user.click(screen.getByLabelText('预览视频'))
    expect(await screen.findByTitle('测试视频')).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /在 B 站打开/ })).toHaveAttribute('href', expect.stringContaining('BV1xx411c7mD'))
  })

  it('clamps long body text and expands on demand', async () => {
    const user = userEvent.setup()
    const preview = '字'.repeat(220)
    api.content.mockResolvedValue({ content: { ...base, text: `${preview}全文` }, attachments: [] })
    renderCard({ ...base, text: preview })
    expect(screen.getByRole('button', { name: '展开全文' })).toBeInTheDocument()
    expect(api.content).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: '展开全文' }))
    expect(screen.getByRole('button', { name: '收起' })).toBeInTheDocument()
    expect(api.content).toHaveBeenCalledWith(base.id, expect.anything())
    expect(await screen.findByText(`${preview}全文`)).toBeInTheDocument()
  })

  it('keeps emoji intact when the preview cutoff crosses a surrogate pair', () => {
    const preview = `${'字'.repeat(179)}😀`
    renderCard({ ...base, text: `${preview}${'字'.repeat(20)}` })
    const body = screen.getByText(preview)
    expect(body).toBeInTheDocument()
    expect(body.textContent).toBe(preview)
    expect(body.textContent).not.toMatch(/[\uD800-\uDBFF]$/)
  })

  it('shows an error instead of the list preview when expansion fails', async () => {
    const user = userEvent.setup()
    api.content.mockRejectedValue(new Error('档案不可用'))
    renderCard({ ...base, text: '字'.repeat(220) })
    await user.click(screen.getByRole('button', { name: '展开全文' }))
    expect(await screen.findByText('档案不可用')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: '重试' })).toBeInTheDocument()
    expect(screen.queryByText('字'.repeat(220))).not.toBeInTheDocument()
  })

  it('expands a truncated video description from the detail query', async () => {
    const user = userEvent.setup()
    const preview = '简'.repeat(220)
    const video: UnifiedContent = {
      ...base,
      id: 'bilibili:content:video',
      type: 'video',
      upstream_type: 'DYNAMIC_TYPE_AV',
      title: '测试视频',
      text: preview,
      url: 'https://www.bilibili.com/video/BV1xx411c7mD',
    }
    api.content.mockResolvedValue({ content: { ...video, text: `${preview}完整简介` }, attachments: [] })
    renderCard(video)
    expect(screen.getByRole('button', { name: '展开全文' })).toBeInTheDocument()
    expect(api.content).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: '展开全文' }))
    expect(await screen.findByText(`${preview}完整简介`)).toBeInTheDocument()
    expect(api.content).toHaveBeenCalledWith(video.id, expect.anything())
  })
})
