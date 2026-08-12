import { fireEvent, render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, it } from 'vitest'
import type { Attachment } from '../../shared/api/types'
import { galleryImagesFromAttachments, MediaGrid, MediaLightbox } from './MediaGallery'

const image = (id: string, path = `${id}.jpg`): Attachment => ({
  id, content_id: 'content', external_id: id, type: 'image', localized: Boolean(path), file_name: `${id}.jpg`,
})

describe('MediaGallery', () => {
  it('builds gallery images only from localized attachments', () => {
    expect(galleryImagesFromAttachments('content', [
      image('a'),
      { id: 'b', content_id: 'content', external_id: 'b', type: 'image', localized: false },
      { id: 'c', content_id: 'content', external_id: 'c', type: 'file', localized: true },
    ])).toEqual([{ id: 'a', url: '/api/v4/contents/content/attachments/a', width: undefined, height: undefined, alt: 'a.jpg' }])
  })

  it('renders a media grid, opens lightbox, and navigates without wrapping', async () => {
    const user = userEvent.setup()
    const images = [
      { id: '1', url: '/api/v4/contents/c/attachments/1', alt: '一' },
      { id: '2', url: '/api/v4/contents/c/attachments/2', alt: '二' },
      { id: '3', url: '/api/v4/contents/c/attachments/3', alt: '三' },
    ]
    render(<MediaGrid images={images} />)
    expect(screen.getByLabelText('放大第 1 张图片')).toBeInTheDocument()
    await user.click(screen.getByLabelText('放大第 2 张图片'))
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    await user.click(screen.getByLabelText('下一张图片'))
    expect(screen.getByText('3 / 3')).toBeInTheDocument()
    expect(screen.getByLabelText('下一张图片')).toBeDisabled()
    fireEvent.keyDown(window, { key: 'ArrowRight' })
    expect(screen.getByText('3 / 3')).toBeInTheDocument()
    fireEvent.keyDown(window, { key: 'ArrowLeft' })
    expect(screen.getByText('2 / 3')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '关闭' }))
    expect(screen.queryByText('/ 3')).not.toBeInTheDocument()
  })

  it('shows overflow marker for more than nine images and handles load failure', async () => {
    const user = userEvent.setup()
    const images = Array.from({ length: 11 }, (_, index) => ({ id: String(index), url: `/img/${index}.jpg`, alt: `图${index}` }))
    render(<MediaGrid images={images} />)
    expect(screen.getByText('+2')).toBeInTheDocument()
    fireEvent.error(screen.getAllByRole('img')[0])
    expect(screen.getByText('媒体加载失败')).toBeInTheDocument()
    await user.click(screen.getByLabelText('放大第 2 张图片'))
    fireEvent.error(screen.getByAltText('预览第 2 张图片'))
    expect(screen.getByText('图片加载失败')).toBeInTheDocument()
  })

  it('renders nothing for an empty gallery and ignores navigation when closed', () => {
    const { container } = render(<MediaGrid images={[]} />)
    expect(container).toBeEmptyDOMElement()
    const onSelect = () => undefined
    render(<MediaLightbox images={[{ id: '1', url: '/a.jpg' }]} selected={null} onSelect={onSelect} onClose={() => undefined} />)
    expect(screen.queryByText('/ 1')).not.toBeInTheDocument()
  })
})
