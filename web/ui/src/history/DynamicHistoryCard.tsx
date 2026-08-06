import { useEffect, useLayoutEffect, useRef, useState } from 'react'
import BrokenImage from '@mui/icons-material/BrokenImage'
import ChatBubbleOutline from '@mui/icons-material/ChatBubbleOutline'
import ChevronLeft from '@mui/icons-material/ChevronLeft'
import ChevronRight from '@mui/icons-material/ChevronRight'
import Close from '@mui/icons-material/Close'
import FavoriteBorder from '@mui/icons-material/FavoriteBorder'
import OpenInNew from '@mui/icons-material/OpenInNew'
import Repeat from '@mui/icons-material/Repeat'
import Visibility from '@mui/icons-material/Visibility'
import { Avatar, Box, Button, Card, CardContent, Chip, Dialog, Divider, IconButton, Paper, Stack, Typography } from '@mui/material'
import { composePreviewBody, dynamicTypeLabel, formatDate, formatInteractionCount, formatRelativeDate, historyMediaURL, normalizePreviewText } from '../presentation'
import type { DynamicHistoryItem, DynamicMedia, DynamicPreview } from '../types'

type HistoryContent = DynamicHistoryItem | DynamicPreview

export function DynamicHistoryCard({ item, timeZone }: { item: DynamicHistoryItem; timeZone: string }) {
  const [expanded, setExpanded] = useState(false)
  const [clamped, setClamped] = useState(false)
  const bodyRef = useRef<HTMLElement | null>(null)
  const contentCard = isContentCardType(item.type)
  const body = contentCard ? (item.summary || '').trim() : composePreviewBody(item.summary, item.description)
  const title = contentCard ? '' : (item.title || '').trim()
  const targetURL = (item.target_url || item.url || '').trim()
  useLayoutEffect(() => {
    if (expanded) { setClamped(false); return }
    const node = bodyRef.current
    setClamped(Boolean(node && node.scrollHeight > node.clientHeight + 1))
  }, [body, expanded])
  return <Card className="history-card"><CardContent className="history-card-content">
    <Stack direction="row" spacing={1.5} alignItems="flex-start">
      <Avatar className="history-author-avatar" aria-hidden="true">{historyAvatarText(item.up_name || item.uid)}</Avatar>
      <Box minWidth={0} flex={1}>
        <Stack direction="row" justifyContent="space-between" alignItems="flex-start" gap={1}>
          <Box minWidth={0}><Typography className="history-author-name" fontWeight={750}>{item.up_name || item.uid}</Typography><Typography variant="body2" color="text.secondary" title={formatDate(item.published_at, timeZone)}>{formatRelativeDate(item.published_at, Date.now(), timeZone)} · {dynamicTypeLabel(item.type)}</Typography></Box>
          <Stack direction="row" spacing={.75} flexWrap="wrap" justifyContent="flex-end">{item.badge && <Chip size="small" label={item.badge} />}{item.baseline && <Chip size="small" label="基线" variant="outlined" />}</Stack>
        </Stack>
        <Box className="history-copyable">
          {title && <Typography className="history-post-title" fontWeight={750} sx={{ mt: 1.75 }}>{title}</Typography>}
          {body && <><Typography ref={bodyRef} className={expanded ? undefined : 'history-text-clamp'} sx={{ mt: title ? .75 : 1.75, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{body}</Typography>{(expanded || clamped) && <Button size="small" sx={{ mt: .5, px: 0 }} onClick={() => setExpanded(value => !value)}>{expanded ? '收起' : '展开全文'}</Button>}</>}
          <DynamicContentPreview item={item} />
          {item.original && <OriginalDynamicPreview item={item.original} />}
          {!title && !body && !item.media?.length && !item.original && <Typography color="text.secondary" sx={{ mt: 1.5 }}>（该归档没有可预览的正文或媒体）</Typography>}
        </Box>
      </Box>
    </Stack>
    <Divider sx={{ mt: 2 }} />
    <Stack className="history-actions" direction="row" justifyContent="space-between" alignItems="center" gap={1}>
      <Stack direction="row" alignItems="center" spacing={{ xs: 1.25, sm: 2.5 }} minWidth={0}>{item.stats && <><HistoryStat icon={<Repeat />} value={item.stats.forwards} emptyLabel="转发" label="转发" /><HistoryStat icon={<ChatBubbleOutline />} value={item.stats.comments} emptyLabel="评论" label="评论" /><HistoryStat icon={<FavoriteBorder />} value={item.stats.likes} emptyLabel="点赞" label="点赞" /></>}</Stack>
      {targetURL && <Button className="history-original-link" size="small" startIcon={<OpenInNew />} href={targetURL} target="_blank" rel="noopener noreferrer">查看原内容</Button>}
    </Stack>
  </CardContent></Card>
}

function DynamicMediaGrid({ media }: { media: NonNullable<DynamicHistoryItem['media']> }) {
  const available = media.filter(item => item.url)
  const visible = available.slice(0, 9)
  const [selected, setSelected] = useState<number | null>(null)
  if (visible.length === 0) return null
  const single = visible.length === 1
  return <><Box className={`history-media-grid ${single ? 'history-media-single' : ''}`} sx={{ mt: 1.5 }}>{visible.map((item, index) => <MediaTile key={`${item.url}-${index}`} item={item} extra={index === 8 ? available.length - 9 : 0} single={single} index={index} onOpen={() => setSelected(index)} />)}</Box><MediaLightbox media={visible} selected={selected} onSelect={setSelected} onClose={() => setSelected(null)} /></>
}

function MediaTile({ item, extra, single, index, onOpen }: { item: DynamicMedia; extra: number; single: boolean; index: number; onOpen: () => void }) {
  const [failed, setFailed] = useState(false)
  return <Box className="history-media-tile" sx={single && item.width && item.height ? { aspectRatio: `${item.width} / ${item.height}` } : undefined}>
    {failed ? <Stack className="history-media-fallback" alignItems="center" justifyContent="center"><BrokenImage /><Typography variant="caption">媒体加载失败</Typography></Stack> : <button type="button" className="history-media-button" aria-label={`放大第 ${index + 1} 张${item.kind === 'cover' ? '内容封面' : '动态图片'}`} onClick={onOpen}><img src={historyMediaURL(item.url, single ? 720 : 240)} alt={item.kind === 'cover' ? '内容封面' : '动态图片'} loading="lazy" onError={() => setFailed(true)} /></button>}
    {extra > 0 && <Box className="history-media-extra">+{extra}</Box>}
  </Box>
}

function OriginalDynamicPreview({ item }: { item: NonNullable<DynamicHistoryItem['original']> }) {
  const contentCard = isContentCardType(item.type)
  const body = contentCard ? (item.summary || '').trim() : composePreviewBody(item.summary, item.description)
  const title = contentCard ? '' : (item.title || '').trim()
  return <Paper variant="outlined" sx={{ mt: 1.5, p: 1.5, bgcolor: 'action.hover' }}><Typography variant="caption" color="text.secondary">转发自 {item.up_name || item.uid || '原动态'}</Typography>{title && <Typography fontWeight={700} sx={{ mt: .5 }}>{title}</Typography>}{body && normalizePreviewText(body) !== normalizePreviewText(title) && <Typography className="history-original-clamp" sx={{ mt: .5, whiteSpace: 'pre-wrap' }}>{body}</Typography>}<DynamicContentPreview item={item} />{!title && !body && !item.media?.length && <Typography color="text.secondary" sx={{ mt: .5 }}>原动态内容未被归档</Typography>}</Paper>
}

function DynamicContentPreview({ item }: { item: HistoryContent }) {
  if (!item.media?.length) return null
  if (!isContentCardType(item.type)) return <DynamicMediaGrid media={item.media} />
  const cover = item.media.find(media => media.url)
  return cover ? <ContentLandingCard item={item} cover={cover} /> : null
}

function ContentLandingCard({ item, cover }: { item: HistoryContent; cover: DynamicMedia }) {
  const [selected, setSelected] = useState<number | null>(null)
  const [failed, setFailed] = useState(false)
  const media = (item.media || []).filter(entry => entry.url)
  return <><Paper variant="outlined" className="history-content-card"><Box className="history-content-cover">{failed ? <Stack className="history-media-fallback" alignItems="center" justifyContent="center"><BrokenImage /><Typography variant="caption">封面加载失败</Typography></Stack> : <button type="button" className="history-media-button" aria-label="放大内容封面" onClick={() => setSelected(Math.max(0, media.indexOf(cover)))}><img src={historyMediaURL(cover.url, 720)} alt="内容封面" loading="lazy" onError={() => setFailed(true)} />{item.video?.duration && <span className="history-video-duration">{item.video.duration}</span>}</button>}</Box><Stack className="history-content-meta" spacing={1}><Typography fontWeight={750}>{item.title || dynamicTypeLabel(item.type || '')}</Typography>{item.description && <Typography variant="body2" color="text.secondary" className="history-content-description">{item.description}</Typography>}{item.video && <Stack direction="row" spacing={2} color="text.secondary" mt="auto">{item.video.views && <Stack direction="row" spacing={.5} alignItems="center"><Visibility fontSize="small" /><Typography variant="caption">{item.video.views}</Typography></Stack>}{item.video.danmaku && <Typography variant="caption">弹幕 {item.video.danmaku}</Typography>}</Stack>}</Stack></Paper><MediaLightbox media={media} selected={selected} onSelect={setSelected} onClose={() => setSelected(null)} /></>
}

function MediaLightbox({ media, selected, onSelect, onClose }: { media: DynamicMedia[]; selected: number | null; onSelect: (index: number) => void; onClose: () => void }) {
  const open = selected !== null && Boolean(media[selected])
  const current = open ? media[selected!] : undefined
  const [failed, setFailed] = useState(false)
  useEffect(() => { setFailed(false) }, [current?.url])
  const move = (offset: number) => {
    if (selected === null) return
    const next = selected + offset
    if (next >= 0 && next < media.length) onSelect(next)
  }
  return <Dialog open={open} onClose={onClose} maxWidth={false} className="history-lightbox" onKeyDown={event => { if (event.key === 'ArrowLeft') move(-1); if (event.key === 'ArrowRight') move(1); if (event.key === 'Escape') { event.preventDefault(); onClose() } }} PaperProps={{ 'aria-label': '图片预览', sx: { bgcolor: 'transparent', boxShadow: 'none', overflow: 'visible', maxWidth: 'calc(100vw - 32px)', maxHeight: 'calc(100vh - 32px)' } }}>
    <Box className="history-lightbox-stage"><IconButton className="history-lightbox-close" aria-label="关闭图片预览" onClick={onClose}><Close /></IconButton>{current && (failed ? <Stack className="history-lightbox-fallback" alignItems="center" justifyContent="center"><BrokenImage /><Typography>图片加载失败</Typography></Stack> : <img src={current.url} alt={`预览第 ${selected! + 1} 张图片`} onError={() => setFailed(true)} />)}{media.length > 1 && <><IconButton className="history-lightbox-previous" aria-label="上一张图片" disabled={selected === 0} onClick={() => move(-1)}><ChevronLeft /></IconButton><IconButton className="history-lightbox-next" aria-label="下一张图片" disabled={selected === media.length - 1} onClick={() => move(1)}><ChevronRight /></IconButton><Typography className="history-lightbox-count" variant="body2">{selected! + 1} / {media.length}</Typography></>}</Box>
  </Dialog>
}

function HistoryStat({ icon, value, emptyLabel, label }: { icon: React.ReactNode; value: number; emptyLabel: string; label: string }) {
  return <Stack direction="row" spacing={.5} alignItems="center" color="text.secondary" aria-label={`${label} ${value}`}>{icon}<Typography variant="body2">{formatInteractionCount(value, emptyLabel)}</Typography></Stack>
}

function isContentCardType(type?: string) {
  return type === 'DYNAMIC_TYPE_AV' || type === 'DYNAMIC_TYPE_ARTICLE' || type === 'DYNAMIC_TYPE_PGC' || type === 'DYNAMIC_TYPE_COMMON_SQUARE'
}

function historyAvatarText(value: string) {
  return Array.from(value.trim())[0] || 'UP'
}
