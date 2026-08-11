import { useQuery } from '@tanstack/react-query'
import {
  ExternalLink,
  FileText,
  Heart,
  MessageCircle,
  Paperclip,
  Play,
  Repeat2,
  Star,
  type LucideIcon,
} from 'lucide-react'
import { useState } from 'react'
import type { Attachment, CommentTreeNode, UnifiedContent } from '../../shared/api/types'
import { queries } from '../../shared/api/query'
import {
  dynamicTypeLabel,
  formatDate,
  formatInteractionCount,
  formatRelativeDate,
  normalizePreviewText,
} from '../../shared/lib/presentation'
import { Alert, Badge, Button, Card, LoadingState } from '../../shared/ui'
import { Dialog } from '../../shared/ui/Dialog'
import {
  attachmentURL,
  avatarText,
  formatBytes,
  formatDuration,
  historyTypeLabel,
  isContentCardType,
  isVideoLike,
  nonImageAttachments,
  originalContentURL,
  platformLabel,
  roleLabel,
  videoEmbedURL,
} from './helpers'
import { galleryImagesFromAttachments, MediaGrid } from './MediaGallery'

export function HistoryCard({ item, timeZone, sourceName }: {
  item: UnifiedContent
  timeZone: string
  sourceName?: string
}) {
  const [expanded, setExpanded] = useState(false)
  const [panel, setPanel] = useState<'none' | 'media' | 'comments'>('none')
  const [now] = useState(() => Date.now())
  const contentCard = isContentCardType(item)
  const title = (item.title || '').trim()
  const text = (item.text || '').trim()
  // Content-card types (video/article/…) use the landing preview for title/description.
  // Feed types put the primary readable copy in text (or title when text is empty).
  const body = contentCard ? '' : (text || title)
  const showTitle = !contentCard && Boolean(title && text && normalizePreviewText(title) !== normalizePreviewText(text))
  const targetURL = originalContentURL(item)
  const expandable = body.length > 180
  const author = item.author_name || item.author_id || sourceName || '未知作者'
  const typeLabel = historyTypeLabel(item, dynamicTypeLabel)
  const detailEnabled = panel !== 'none'
  const detail = useQuery({ ...queries.content(item.id), enabled: detailEnabled })
  const comments = useQuery({ ...queries.contentComments(item.id), enabled: panel === 'comments' })
  const commentLabel = `查看评论：${item.title || item.text?.slice(0, 40) || item.external_id}`

  return <Card className="history-card">
    <div className="history-card__header">
      <span className="avatar" aria-hidden="true">{avatarText(author)}</span>
      <div>
        <strong className="history-author">{author}</strong>
        <p title={formatDate(item.published_at, timeZone)}>
          {formatRelativeDate(item.published_at, now, timeZone)}
          {' · '}
          {platformLabel(item.platform)}
          {sourceName ? ` · ${sourceName}` : ''}
          {' · '}
          {typeLabel}
        </p>
      </div>
      <div className="badge-row">
        {item.baseline && <Badge>基线</Badge>}
        {item.deleted_at && <Badge tone="danger">已删除</Badge>}
        {item.tree_incomplete && <Badge tone="warning">树不完整</Badge>}
      </div>
    </div>

    <div className="history-body">
      {showTitle && <h2>{title}</h2>}
      {body && <>
        <p className={expanded ? '' : 'history-text-clamp'}>{body}</p>
        {(expanded || expandable) && (
          <Button onPress={() => setExpanded(value => !value)}>{expanded ? '收起' : '展开全文'}</Button>
        )}
      </>}

      {contentCard && <ContentLandingPreview item={item} description={text} />}

      {!showTitle && !body && !contentCard && (
        <p className="muted">（该归档没有可预览的正文）</p>
      )}

      {panel === 'media' && <MediaPanel item={item} detailPending={detail.isPending} detailError={detail.error} attachments={detail.data?.attachments || []} onClose={() => setPanel('none')} />}
      {panel === 'comments' && <CommentsPanel timeZone={timeZone} commentsPending={comments.isPending} commentsError={comments.error} commentsData={comments.data} targetURL={originalContentURL(detail.data?.content || item)} onClose={() => setPanel('none')} />}
    </div>

    <footer className="history-footer">
      <div className="history-stats">
        {item.stats && <>
          {item.stats.forwards !== undefined && <Stat icon={Repeat2} value={item.stats.forwards} empty="转发" label="转发" />}
          {item.stats.comments !== undefined && <Stat icon={MessageCircle} value={item.stats.comments} empty="评论" label="评论" />}
          {item.stats.likes !== undefined && <Stat icon={Heart} value={item.stats.likes} empty="点赞" label="点赞" />}
          {item.stats.rewards !== undefined && item.stats.rewards > 0 && <Stat icon={Star} value={item.stats.rewards} empty="赞赏" label="赞赏" />}
        </>}
      </div>
      <div className="badge-row">
        <button
          type="button"
          className={`button button--${panel === 'media' ? 'primary' : 'ghost'}`}
          aria-expanded={panel === 'media'}
          onClick={() => setPanel(value => value === 'media' ? 'none' : 'media')}
        >
          <Paperclip aria-hidden="true" />媒体与附件
        </button>
        <button
          type="button"
          className={`button button--${panel === 'comments' ? 'primary' : 'ghost'}`}
          aria-expanded={panel === 'comments'}
          aria-label={commentLabel}
          onClick={() => setPanel(value => value === 'comments' ? 'none' : 'comments')}
        >
          <MessageCircle aria-hidden="true" />评论
        </button>
        {targetURL && (
          <a className="button button--ghost" href={targetURL} target="_blank" rel="noopener noreferrer"><ExternalLink aria-hidden="true" />查看原内容</a>
        )}
      </div>
    </footer>
  </Card>
}

function ContentLandingPreview({ item, description }: { item: UnifiedContent; description: string }) {
  const [open, setOpen] = useState(false)
  const target = originalContentURL(item)
  const embed = videoEmbedURL(item)
  const title = item.title || historyTypeLabel(item, dynamicTypeLabel)
  const blurb = description && normalizePreviewText(description) !== normalizePreviewText(title) ? description : ''

  const activate = () => {
    if (embed) setOpen(true)
    else if (target) window.open(target, '_blank', 'noopener,noreferrer')
  }

  return <>
    <div className="content-preview">
      <div className="content-preview__cover">
        <button
          type="button"
          onClick={activate}
          aria-label={embed ? '预览视频' : target ? '打开原内容' : '内容预览'}
          disabled={!embed && !target}
        >
          <span className="media-fallback" aria-hidden="true">
            {isVideoLike(item) ? <Play /> : <FileText />}
            <small>{isVideoLike(item) ? '视频' : '内容卡片'}</small>
          </span>
          {(embed || target) && isVideoLike(item) && <span className="play"><Play aria-hidden="true" /></span>}
        </button>
      </div>
      <div className="content-preview__meta">
        <strong>{title}</strong>
        {blurb && <p>{blurb}</p>}
        {item.stats?.views !== undefined && item.stats.views > 0 && (
          <small>播放 {formatInteractionCount(item.stats.views, '播放')}</small>
        )}
      </div>
    </div>
    {embed && <VideoDialog open={open} title={title} embed={embed} target={target} onClose={() => setOpen(false)} />}
  </>
}

function VideoDialog({ open, title, embed, target, onClose }: {
  open: boolean; title: string; embed: string; target: string; onClose: () => void
}) {
  return <Dialog
    open={open}
    onClose={onClose}
    ariaLabel="视频预览"
    actions={<>
      {target && <a className="button button--primary" href={target} target="_blank" rel="noreferrer">在 B 站打开<ExternalLink aria-hidden="true" /></a>}
      <Button onPress={onClose}>关闭</Button>
    </>}
  >
    <div className="video-frame">
      {open && <iframe title={title} src={embed} allow="fullscreen; picture-in-picture" allowFullScreen loading="lazy" referrerPolicy="no-referrer" />}
    </div>
  </Dialog>
}

function MediaPanel({ item, detailPending, detailError, attachments, onClose }: {
  item: UnifiedContent
  detailPending: boolean
  detailError: Error | null
  attachments: Attachment[]
  onClose: () => void
}) {
  if (detailPending) return <div className="original-preview"><LoadingState label="正在加载附件" /></div>
  if (detailError) return <div className="original-preview"><Alert tone="danger">{detailError.message}</Alert><Button onPress={onClose}>收起</Button></div>
  const images = galleryImagesFromAttachments(item.id, attachments)
  const others = nonImageAttachments(attachments)
  return <div className="original-preview" aria-label="媒体与附件">
    <div className="badge-row"><strong>媒体与附件</strong><Button onPress={onClose}>收起</Button></div>
    {images.length > 0 && <MediaGrid images={images} />}
    {others.length > 0 && (
      <ul>
        {others.map(attachment => <AttachmentRow key={attachment.id} contentID={item.id} attachment={attachment} />)}
      </ul>
    )}
    {images.length === 0 && others.length === 0 && <p className="muted">暂无附件。远端图片 URL 不在列表接口中暴露，仅已本地化的图片可预览。</p>}
  </div>
}

function AttachmentRow({ contentID, attachment }: { contentID: string; attachment: Attachment }) {
  const href = attachmentURL(contentID, attachment)
  const label = attachment.file_name || attachment.external_id || attachment.id
  const meta = [
    attachment.type,
    attachment.size ? formatBytes(attachment.size) : '',
    formatDuration(attachment.duration_sec),
    attachment.localize_error || '',
  ].filter(Boolean).join(' · ')
  return <li>
    {href ? <a href={href}>{label}</a> : <span>{label}</span>}
    {meta ? ` · ${meta}` : ''}
  </li>
}

function CommentsPanel({ timeZone, commentsPending, commentsError, commentsData, targetURL, onClose }: {
  timeZone: string
  commentsPending: boolean
  commentsError: Error | null
  commentsData?: { children: CommentTreeNode[]; incomplete: boolean }
  targetURL: string
  onClose: () => void
}) {
  return <div className="original-preview" aria-label="评论树">
    <div className="badge-row"><strong>评论</strong><Button onPress={onClose}>收起</Button></div>
    {commentsPending ? <LoadingState label="正在加载评论" /> : commentsError ? <Alert tone="danger">{commentsError.message}</Alert> : commentsData && <>
      {commentsData.incomplete && <Alert tone="warning">上游分页或父子关系不完整，当前树可能缺少节点。</Alert>}
      {commentsData.children.length === 0
        ? <p className="muted">暂无评论</p>
        : <div className="comment-thread">{commentsData.children.map(node => <CommentBranch key={node.id} node={node} timeZone={timeZone} />)}</div>}
    </>}
    {targetURL && <a className="button button--outline" href={targetURL} target="_blank" rel="noreferrer">查看原内容<ExternalLink aria-hidden="true" /></a>}
  </div>
}

export function CommentBranch({ node, timeZone }: { node: CommentTreeNode; timeZone: string }) {
  const role = roleLabel(node.author_role)
  return <article className={node.is_trigger ? 'comment comment--trigger' : 'comment'}>
    <div>
      <strong>{node.name || node.author_id}</strong>
      {role && <Badge tone="info">{role}</Badge>}
      {node.is_trigger && <Badge tone="success">新增触发</Badge>}
      {node.deleted_at && <Badge tone="danger">已删除</Badge>}
      <time>{formatDate(node.time, timeZone)}</time>
    </div>
    <p>{node.message}</p>
    {node.children?.map(child => (
      <div key={child.id} className="comment-thread">
        <CommentBranch node={child} timeZone={timeZone} />
      </div>
    ))}
  </article>
}

function Stat({ icon: StatIcon, value, empty, label }: { icon: LucideIcon; value: number; empty: string; label: string }) {
  return <span aria-label={`${label} ${value}`}><StatIcon aria-hidden="true" />{formatInteractionCount(value, empty)}</span>
}
