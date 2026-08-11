import { useEffect, useState } from 'react'
import { ChevronLeft, ChevronRight, ImageOff } from 'lucide-react'
import type { Attachment } from '../../shared/api/types'
import { historyMediaURL } from '../../shared/lib/presentation'
import { Button, IconButton } from '../../shared/ui'
import { Dialog } from '../../shared/ui/Dialog'
import { attachmentURL } from './helpers'

export interface GalleryImage {
  id: string
  url: string
  width?: number
  height?: number
  alt?: string
}

export function galleryImagesFromAttachments(contentID: string, attachments: Attachment[]): GalleryImage[] {
  return attachments
    .filter(item => item.type === 'image' && item.local_path)
    .map(item => ({
      id: item.id,
      url: attachmentURL(contentID, item),
      width: item.width,
      height: item.height,
      alt: item.file_name || '动态图片',
    }))
    .filter(item => item.url)
}

export function MediaGrid({ images }: { images: GalleryImage[] }) {
  const visible = images.slice(0, 9)
  const [selected, setSelected] = useState<number | null>(null)
  if (!visible.length) return null
  return <>
    <div className={`media-grid${visible.length === 1 ? ' media-grid--single' : ''}`}>
      {visible.map((item, index) => (
        <MediaTile
          key={item.id}
          image={item}
          index={index}
          single={visible.length === 1}
          extra={index === 8 ? images.length - 9 : 0}
          onOpen={() => setSelected(index)}
        />
      ))}
    </div>
    <MediaLightbox images={visible} selected={selected} onSelect={setSelected} onClose={() => setSelected(null)} />
  </>
}

function MediaTile({ image, index, single, extra, onOpen }: {
  image: GalleryImage; index: number; single: boolean; extra: number; onOpen: () => void
}) {
  const [failed, setFailed] = useState(false)
  return <div className="media-tile" style={single && image.width && image.height ? { aspectRatio: `${image.width} / ${image.height}` } : undefined}>
    {failed
      ? <span className="media-fallback"><ImageOff aria-hidden="true" /><small>媒体加载失败</small></span>
      : <button type="button" onClick={onOpen} aria-label={`放大第 ${index + 1} 张图片`}>
        <img
          src={historyMediaURL(image.url, single ? 720 : 480)}
          alt={image.alt || '动态图片'}
          loading="lazy"
          onError={() => setFailed(true)}
        />
      </button>}
    {extra > 0 && <span className="media-extra">+{extra}</span>}
  </div>
}

export function MediaLightbox({ images, selected, onSelect, onClose }: {
  images: GalleryImage[]
  selected: number | null
  onSelect: (index: number) => void
  onClose: () => void
}) {
  const current = selected === null ? undefined : images[selected]
  const [failedURL, setFailedURL] = useState('')
  const move = (offset: number) => {
    if (selected === null) return
    const next = selected + offset
    if (next >= 0 && next < images.length) onSelect(next)
  }

  useEffect(() => {
    if (selected === null) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'ArrowLeft') {
        event.preventDefault()
        const next = selected - 1
        if (next >= 0) onSelect(next)
      }
      if (event.key === 'ArrowRight') {
        event.preventDefault()
        const next = selected + 1
        if (next < images.length) onSelect(next)
      }
    }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [selected, images.length, onSelect])

  const imageFailed = Boolean(current && failedURL === current.url)

  return <Dialog
    open={Boolean(current)}
    onClose={onClose}
    ariaLabel="图片预览"
    actions={<>
      <IconButton label="上一张图片" isDisabled={selected === 0} onPress={() => move(-1)}><ChevronLeft aria-hidden="true" /></IconButton>
      <span>{selected === null ? 0 : selected + 1} / {images.length}</span>
      <IconButton label="下一张图片" isDisabled={selected === null || selected === images.length - 1} onPress={() => move(1)}><ChevronRight aria-hidden="true" /></IconButton>
      <Button onPress={onClose}>关闭</Button>
    </>}
  >
    <div className="lightbox-image">
      {current && (imageFailed
        ? <span className="media-fallback">图片加载失败</span>
        : <img key={current.url} src={current.url} alt={`预览第 ${(selected ?? 0) + 1} 张图片`} onError={() => setFailedURL(current.url)} />)}
    </div>
  </Dialog>
}
