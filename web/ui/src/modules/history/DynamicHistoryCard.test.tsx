import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import type { DynamicHistoryItem } from '../../shared/api/types'
import { DynamicHistoryCard } from './DynamicHistoryCard'

const base: DynamicHistoryItem = {
  id: 'dynamic', uid: '42', up_name: '测试 UP', type: 'DYNAMIC_TYPE_WORD',
  published_at: '2026-08-09T10:00:00Z', discovered_at: '2026-08-09T10:00:01Z',
  baseline: false, summary: '测试正文', url: 'https://t.bilibili.com/123',
}

describe('DynamicHistoryCard', () => {
  afterEach(() => vi.restoreAllMocks())

  it('expands text, reports stats and navigates an image lightbox', async () => {
    const user = userEvent.setup()
    render(<DynamicHistoryCard item={{
      ...base, baseline: true, badge: '投稿', title: '标题', summary: '很长的正文'.repeat(50),
      media: [
        { kind: 'image', url: 'https://i.example/1.jpg', width: 400, height: 300 },
        { kind: 'image', url: 'https://i.example/2.jpg' },
        ...Array.from({ length: 8 }, (_, index) => ({ kind: 'image' as const, url: `https://i.example/${index + 3}.jpg` })),
      ],
      stats: { forwards: 0, comments: 12_000, likes: 3 },
      original: { uid: '7', up_name: '原作者', type: 'DYNAMIC_TYPE_WORD', title: '原标题', summary: '原动态正文' },
    }} timeZone="Asia/Shanghai" />)
    expect(screen.getByText('+1')).toBeInTheDocument(); expect(screen.getByLabelText('转发 0')).toHaveTextContent('转发'); expect(screen.getByLabelText('评论 12000')).toHaveTextContent('1.2万')
    await user.click(screen.getByRole('button', { name: '展开全文' })); expect(screen.getByRole('button', { name: '收起' })).toBeInTheDocument(); await user.click(screen.getByRole('button', { name: '收起' }))
    await user.click(screen.getByRole('button', { name: '放大第 1 张图片' })); expect(screen.getByRole('dialog', { name: '图片预览' })).toBeInTheDocument(); expect(screen.getByText('1 / 9')).toBeInTheDocument()
    await user.click(screen.getByLabelText('下一张图片')); expect(screen.getByText('2 / 9')).toBeInTheDocument(); await user.click(screen.getByLabelText('上一张图片'))
    fireEvent.error(screen.getByAltText('预览第 1 张图片')); expect(screen.getByText('图片加载失败')).toBeInTheDocument(); await user.click(screen.getByRole('button', { name: '关闭' }))
    fireEvent.error(screen.getAllByAltText('动态图片')[0]); expect(screen.getByText('媒体加载失败')).toBeInTheDocument()
  })

  it('opens an embedded Bilibili video and exposes its external target', async () => {
    const user = userEvent.setup()
    render(<DynamicHistoryCard item={{ ...base, type: 'DYNAMIC_TYPE_AV', title: '视频标题', summary: '', target_url: 'https://www.bilibili.com/video/BV1xx411c7mD', media: [{ kind: 'cover', url: 'https://i.example/cover.jpg' }], video: { duration: '01:23', views: '100', danmaku: '20' } }} timeZone="UTC" />)
    expect(screen.getByText('播放 100 · 弹幕 20')).toBeInTheDocument(); await user.click(screen.getByRole('button', { name: '预览视频' }))
    expect(screen.getByRole('dialog', { name: '视频预览' })).toBeInTheDocument(); expect(screen.getByTitle('视频标题')).toHaveAttribute('src', expect.stringContaining('player.bilibili.com'))
    expect(screen.getByRole('link', { name: /在 B 站打开/ })).toHaveAttribute('href', expect.stringContaining('BV1xx411c7mD')); await user.click(screen.getByRole('button', { name: '关闭' }))
  })

  it('opens article targets directly and falls back when cover media fails', async () => {
    const open = vi.spyOn(window, 'open').mockImplementation(() => null); const user = userEvent.setup()
    const { rerender } = render(<DynamicHistoryCard item={{ ...base, type: 'DYNAMIC_TYPE_ARTICLE', title: '专栏', summary: '', target_url: 'https://www.bilibili.com/read/cv1', media: [{ kind: 'cover', url: 'https://i.example/article.jpg' }] }} timeZone="UTC" />)
    await user.click(screen.getByRole('button', { name: '打开原内容' })); expect(open).toHaveBeenCalledWith('https://www.bilibili.com/read/cv1', '_blank', 'noopener,noreferrer')
    fireEvent.error(screen.getByAltText('内容封面')); expect(screen.getByText('封面加载失败')).toBeInTheDocument()
    rerender(<DynamicHistoryCard item={{ ...base, id: 'empty', summary: '', url: '', up_name: '', uid: '' }} timeZone="UTC" />)
    expect(screen.getByText('（该归档没有可预览的正文或媒体）')).toBeInTheDocument()
  })

  it('uses a lightbox for a content card without a safe target', async () => {
    const user = userEvent.setup()
    render(<DynamicHistoryCard item={{ ...base, type: 'DYNAMIC_TYPE_COMMON_SQUARE', title: '卡片', summary: '', url: 'javascript:bad', media: [{ kind: 'cover', url: 'https://i.example/card.jpg' }], original: { summary: '', media: [] } }} timeZone="UTC" />)
    await user.click(screen.getByRole('button', { name: '放大内容封面' })); expect(screen.getByRole('dialog', { name: '图片预览' })).toBeInTheDocument(); expect(screen.getByText('原动态内容未被归档')).toBeInTheDocument()
  })
})
