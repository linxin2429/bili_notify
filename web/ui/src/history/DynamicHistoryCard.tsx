import { useState } from 'react'
import type { DynamicHistoryItem, DynamicMedia, DynamicPreview } from '../types'
import { Badge, Button, Card, IconButton } from '../shared/ui'
import { Dialog } from '../shared/ui/Dialog'
import { bilibiliPlayerEmbedURL, composePreviewBody, dynamicTypeLabel, formatDate, formatInteractionCount, formatRelativeDate, historyMediaURL, normalizePreviewText, safeBilibiliURL } from '../presentation'

type HistoryContent = DynamicHistoryItem | DynamicPreview

export function DynamicHistoryCard({ item, timeZone }: { item: DynamicHistoryItem; timeZone: string }) {
  const [expanded, setExpanded] = useState(false); const [now] = useState(() => Date.now())
  const contentCard = isContentCardType(item.type); const body = contentCard ? (item.summary || '').trim() : composePreviewBody(item.summary, item.description); const title = contentCard ? '' : (item.title || '').trim(); const targetURL = safeBilibiliURL(item.target_url || item.url)
  const expandable = body.length > 180
  return <Card className="history-card"><div className="history-card__header"><span className="avatar">{Array.from((item.up_name || item.uid).trim())[0] || 'UP'}</span><div><strong className="history-author">{item.up_name || item.uid}</strong><p title={formatDate(item.published_at, timeZone)}>{formatRelativeDate(item.published_at, now, timeZone)} · {dynamicTypeLabel(item.type)}</p></div><div className="badge-row">{item.badge && <Badge>{item.badge}</Badge>}{item.baseline && <Badge>基线</Badge>}</div></div>
    <div className="history-body">{title && <h2>{title}</h2>}{body && <><p className={expanded ? '' : 'history-text-clamp'}>{body}</p>{(expanded || expandable) && <Button onPress={() => setExpanded(value => !value)}>{expanded ? '收起' : '展开全文'}</Button>}</>}<DynamicContentPreview item={item} />{item.original && <OriginalPreview item={item.original} />}{!title && !body && !item.media?.length && !item.original && <p className="muted">（该归档没有可预览的正文或媒体）</p>}</div>
    <footer className="history-footer"><div className="history-stats">{item.stats && <><Stat icon="↗" value={item.stats.forwards} empty="转发" label="转发" /><Stat icon="◌" value={item.stats.comments} empty="评论" label="评论" /><Stat icon="♡" value={item.stats.likes} empty="点赞" label="点赞" /></>}</div>{targetURL && <a className="button button--ghost" href={targetURL} target="_blank" rel="noopener noreferrer">↗ 查看原内容</a>}</footer>
  </Card>
}

function DynamicContentPreview({ item }: { item: HistoryContent }) {
  if (!item.media?.length) return null
  if (!isContentCardType(item.type)) return <MediaGrid media={item.media} />
  const cover = item.media.find(media => media.url)
  return cover ? <ContentCard item={item} cover={cover} /> : null
}

function MediaGrid({ media }: { media: DynamicMedia[] }) {
  const available = media.filter(item => item.url); const visible = available.slice(0, 9); const [selected, setSelected] = useState<number | null>(null)
  if (!visible.length) return null
  return <><div className={`media-grid${visible.length === 1 ? ' media-grid--single' : ''}`}>{visible.map((item, index) => <MediaTile key={`${item.url}-${index}`} media={item} index={index} extra={index === 8 ? available.length - 9 : 0} onOpen={() => setSelected(index)} />)}</div><MediaLightbox media={visible} selected={selected} onSelect={setSelected} onClose={() => setSelected(null)} /></>
}

function MediaTile({ media, index, extra, onOpen }: { media: DynamicMedia; index: number; extra: number; onOpen: () => void }) {
  const [failed, setFailed] = useState(false)
  return <div className="media-tile" style={media.width && media.height ? { aspectRatio: `${media.width} / ${media.height}` } : undefined}>{failed ? <span className="media-fallback">▧<small>媒体加载失败</small></span> : <button type="button" onClick={onOpen} aria-label={`放大第 ${index + 1} 张图片`}><img src={historyMediaURL(media.url, 480)} alt={media.kind === 'cover' ? '内容封面' : '动态图片'} loading="lazy" onError={() => setFailed(true)} /></button>}{extra > 0 && <span className="media-extra">+{extra}</span>}</div>
}

function ContentCard({ item, cover }: { item: HistoryContent; cover: DynamicMedia }) {
  const [open, setOpen] = useState(false); const [failed, setFailed] = useState(false); const target = safeBilibiliURL(item.target_url || item.url); const embed = item.type === 'DYNAMIC_TYPE_AV' ? bilibiliPlayerEmbedURL(item.target_url || item.url) : ''
  const activate = () => { if (embed || !target) setOpen(true); else window.open(target, '_blank', 'noopener,noreferrer') }
  return <><div className="content-preview"><div className="content-preview__cover">{failed ? <span className="media-fallback">▧<small>封面加载失败</small></span> : <button type="button" onClick={activate} aria-label={embed ? '预览视频' : target ? '打开原内容' : '放大内容封面'}><img src={historyMediaURL(cover.url, 720)} alt="内容封面" loading="lazy" onError={() => setFailed(true)} />{(embed || target) && <span className="play">▶</span>}{item.video?.duration && <span className="duration">{item.video.duration}</span>}</button>}</div><div className="content-preview__meta"><strong>{item.title || dynamicTypeLabel(item.type || '')}</strong>{item.description && <p>{item.description}</p>}{item.video && <small>{item.video.views && `播放 ${item.video.views}`}{item.video.danmaku && ` · 弹幕 ${item.video.danmaku}`}</small>}</div></div>{embed ? <VideoDialog open={open} title={item.title || '视频预览'} embed={embed} target={target} onClose={() => setOpen(false)} /> : !target ? <MediaLightbox media={[cover]} selected={open ? 0 : null} onSelect={() => undefined} onClose={() => setOpen(false)} /> : null}</>
}

function OriginalPreview({ item }: { item: DynamicPreview }) {
  const contentCard = isContentCardType(item.type); const body = contentCard ? (item.summary || '').trim() : composePreviewBody(item.summary, item.description); const title = contentCard ? '' : (item.title || '').trim()
  return <aside className="original-preview"><small>转发自 {item.up_name || item.uid || '原动态'}</small>{title && <strong>{title}</strong>}{body && normalizePreviewText(body) !== normalizePreviewText(title) && <p>{body}</p>}<DynamicContentPreview item={item} />{!title && !body && !item.media?.length && <p>原动态内容未被归档</p>}</aside>
}

function VideoDialog({ open, title, embed, target, onClose }: { open: boolean; title: string; embed: string; target: string; onClose: () => void }) { return <Dialog open={open} onClose={onClose} ariaLabel="视频预览" actions={<>{target && <a className="button button--primary" href={target} target="_blank" rel="noreferrer">在 B 站打开 ↗</a>}<Button onPress={onClose}>关闭</Button></>}><div className="video-frame">{open && <iframe title={title} src={embed} allow="fullscreen; picture-in-picture" allowFullScreen loading="lazy" referrerPolicy="no-referrer" />}</div></Dialog> }

function MediaLightbox({ media, selected, onSelect, onClose }: { media: DynamicMedia[]; selected: number | null; onSelect: (index: number) => void; onClose: () => void }) {
  const current = selected === null ? undefined : media[selected]; const [failedURL, setFailedURL] = useState(''); const move = (offset: number) => { if (selected === null) return; const next = selected + offset; if (next >= 0 && next < media.length) onSelect(next) }
  return <Dialog open={Boolean(current)} onClose={onClose} ariaLabel="图片预览" actions={<><IconButton label="上一张图片" isDisabled={selected === 0} onPress={() => move(-1)}>←</IconButton><span>{selected === null ? 0 : selected + 1} / {media.length}</span><IconButton label="下一张图片" isDisabled={selected === media.length - 1} onPress={() => move(1)}>→</IconButton><Button onPress={onClose}>关闭</Button></>}><div className="lightbox-image">{current && (failedURL === current.url ? <span className="media-fallback">图片加载失败</span> : <img src={current.url} alt={`预览第 ${(selected ?? 0) + 1} 张图片`} onError={() => setFailedURL(current.url)} />)}</div></Dialog>
}

function Stat({ icon, value, empty, label }: { icon: string; value: number; empty: string; label: string }) { return <span aria-label={`${label} ${value}`}>{icon} {formatInteractionCount(value, empty)}</span> }
function isContentCardType(type?: string) { return ['DYNAMIC_TYPE_AV', 'DYNAMIC_TYPE_ARTICLE', 'DYNAMIC_TYPE_PGC', 'DYNAMIC_TYPE_COMMON_SQUARE'].includes(type || '') }
