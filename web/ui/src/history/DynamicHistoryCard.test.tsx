import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { DynamicHistoryCard } from './DynamicHistoryCard'
import { makeDynamic } from '../test/fixtures'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('DynamicHistoryCard', () => {
  it.each([
    { name: 'text', item: makeDynamic({ summary: '完整正文' }), texts: ['完整正文'], images: 0 },
    { name: 'single image', item: makeDynamic({ type: 'DYNAMIC_TYPE_DRAW', media: [{ kind: 'image', url: 'https://example.com/1.jpg', width: 800, height: 600 }] }), texts: [], images: 1 },
    { name: 'multiple images', item: makeDynamic({ type: 'DYNAMIC_TYPE_DRAW', media: [{ kind: 'image', url: 'https://example.com/1.jpg' }, { kind: 'image', url: 'https://example.com/2.jpg' }] }), texts: [], images: 2 },
    { name: 'video', item: makeDynamic({ type: 'DYNAMIC_TYPE_AV', title: '视频标题', summary: '摘要', description: '简介', media: [{ kind: 'cover', url: 'https://example.com/cover.jpg' }], video: { duration: '01:09', views: '8468', danmaku: '12' } }), texts: ['摘要', '视频标题', '简介', '01:09', '8468', '弹幕 12'], images: 1 },
    { name: 'empty', item: makeDynamic({ summary: '', media: [] }), texts: ['（该归档没有可预览的正文或媒体）'], images: 0 },
  ])('renders $name content', ({ item, texts, images }) => {
    const { container } = render(<DynamicHistoryCard item={item} timeZone="Asia/Shanghai" />)
    for (const value of texts) expect(screen.getAllByText(value).length).toBeGreaterThan(0)
    expect(container.querySelectorAll('img')).toHaveLength(images)
  })

  it('renders metadata, original preview, and interaction data', () => {
    render(<DynamicHistoryCard timeZone="Asia/Shanghai" item={makeDynamic({ badge: '投稿', baseline: true, target_url: 'https://www.bilibili.com/video/BV1', stats: { forwards: 0, comments: 572, likes: 12_500 }, original: { up_name: '原作者', summary: '原文' } })} />)
    expect(screen.getByText('投稿')).toBeVisible(); expect(screen.getByText('基线')).toBeVisible(); expect(screen.getByText('转发自 原作者')).toBeVisible(); expect(screen.getByText('原文')).toBeVisible()
    expect(screen.getByRole('link', { name: /查看原内容/ })).toHaveAttribute('href', 'https://www.bilibili.com/video/BV1')
    expect(screen.getByLabelText('评论 572')).toBeVisible(); expect(screen.getByText('1.3万')).toBeVisible()
  })

  it('opens media, navigates by keyboard, handles failure, and closes', async () => {
    const user = userEvent.setup()
    render(<DynamicHistoryCard timeZone="" item={makeDynamic({ type: 'DYNAMIC_TYPE_DRAW', media: [{ kind: 'image', url: 'https://example.com/1.jpg' }, { kind: 'image', url: 'https://example.com/2.jpg' }] })} />)
    await user.click(screen.getByRole('button', { name: '放大第 1 张动态图片' }))
    const dialog = screen.getByRole('dialog', { name: '图片预览' })
    await user.keyboard('{ArrowRight}')
    const second = screen.getByAltText('预览第 2 张图片')
    expect(second).toHaveAttribute('src', 'https://example.com/2.jpg')
    fireEvent.error(second)
    expect(screen.getByText('图片加载失败')).toBeVisible()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(dialog).not.toBeInTheDocument())
  })

  it('opens an embedded player for video covers with a BV target', async () => {
    const user = userEvent.setup()
    render(<DynamicHistoryCard timeZone="" item={makeDynamic({
      type: 'DYNAMIC_TYPE_AV',
      title: '可播放视频',
      target_url: 'https://www.bilibili.com/video/BV1xx411c7mD',
      media: [{ kind: 'cover', url: 'https://example.com/cover.jpg' }],
      video: { duration: '01:09' },
    })} />)
    await user.click(screen.getByRole('button', { name: '预览视频' }))
    const dialog = screen.getByRole('dialog', { name: '视频预览' })
    const frame = dialog.querySelector('iframe')
    expect(frame).toHaveAttribute('src', expect.stringContaining('bvid=BV1xx411c7mD'))
    expect(screen.getByRole('link', { name: '在 B 站打开' })).toHaveAttribute('href', 'https://www.bilibili.com/video/BV1xx411c7mD')
    await user.click(screen.getByRole('button', { name: '关闭视频预览' }))
    await waitFor(() => expect(dialog).not.toBeInTheDocument())
  })

  it('opens the original URL when a content card cannot embed', async () => {
    const user = userEvent.setup()
    const open = vi.fn()
    vi.stubGlobal('open', open)
    render(<DynamicHistoryCard timeZone="" item={makeDynamic({
      type: 'DYNAMIC_TYPE_ARTICLE',
      title: '专栏标题',
      target_url: 'https://www.bilibili.com/read/cv1',
      media: [{ kind: 'cover', url: 'https://example.com/cover.jpg' }],
    })} />)
    await user.click(screen.getByRole('button', { name: '打开原内容' }))
    expect(open).toHaveBeenCalledWith('https://www.bilibili.com/read/cv1', '_blank', 'noopener,noreferrer')
    expect(screen.queryByRole('dialog', { name: '图片预览' })).not.toBeInTheDocument()
  })

  it.each([
    { name: 'tile', item: makeDynamic({ type: 'DYNAMIC_TYPE_DRAW', media: [{ kind: 'image', url: 'https://example.com/1.jpg' }] }), alt: '动态图片', want: '媒体加载失败' },
    { name: 'cover', item: makeDynamic({ type: 'DYNAMIC_TYPE_AV', media: [{ kind: 'cover', url: 'https://example.com/cover.jpg' }] }), alt: '内容封面', want: '封面加载失败' },
  ])('handles $name load failure', ({ item, alt, want }) => {
    render(<DynamicHistoryCard item={item} timeZone="" />)
    fireEvent.error(screen.getByAltText(alt))
    expect(screen.getByText(want)).toBeVisible()
  })

  it('limits grids to nine items and exposes the remainder', () => {
    render(<DynamicHistoryCard item={makeDynamic({ type: 'DYNAMIC_TYPE_DRAW', media: Array.from({ length: 11 }, (_, index) => ({ kind: 'image', url: `https://example.com/${index}.jpg` })) })} timeZone="" />)
    expect(screen.getAllByRole('button', { name: /放大第/ })).toHaveLength(9)
    expect(screen.getByText('+2')).toBeVisible()
  })
})
