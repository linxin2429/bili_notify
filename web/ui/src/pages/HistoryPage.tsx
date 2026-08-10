import { useQuery } from '@tanstack/react-query'
import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { CommentTreeNode, ContentQuery, UnifiedContent } from '../shared/api/types'
import { queries } from '../shared/api/query'
import { Alert, Badge, Button, Card, EmptyState, LoadingState, NativeDateTimeField, PageError, PageHeader, SelectField, TextField } from '../shared/ui'
import { Dialog } from '../shared/ui/Dialog'
import { formatDate, localInputToRFC3339 } from '../shared/lib/presentation'

export function HistoryPage() {
  const [params, setParams] = useSearchParams()
  const platform = params.get('platform') === 'bilibili' || params.get('platform') === 'zsxq' ? params.get('platform') as 'bilibili' | 'zsxq' : undefined
  const sourceID = params.get('source_id') || ''
  const q = params.get('q') || ''
  const from = params.get('from') || ''
  const to = params.get('to') || ''
  const after = params.get('after') || ''
  const [draftQ, setDraftQ] = useState(q)
  const [selectedID, setSelectedID] = useState('')
  const runtime = useQuery(queries.runtime())
  const sources = useQuery(queries.sources(platform || ''))
  const query: ContentQuery = {
    ...(platform && { platform }), ...(sourceID && { source_id: sourceID }), ...(q && { q }),
    ...(from && { from: localInputToRFC3339(from) }), ...(to && { to: localInputToRFC3339(to) }), ...(after && { after }), limit: 20,
  }
  const contents = useQuery(queries.contents(query))
  const detail = useQuery(queries.content(selectedID))
  const comments = useQuery(queries.contentComments(selectedID))
  const update = useCallback((patch: Record<string, string | undefined>, resetCursor = true) => {
    const next = new URLSearchParams(params)
    for (const [key, value] of Object.entries(patch)) {
      if (value) next.set(key, value)
      else next.delete(key)
    }
    if (resetCursor && !('after' in patch)) next.delete('after')
    setParams(next)
  }, [params, setParams])
  useEffect(() => {
    const timer = window.setTimeout(() => { if (draftQ !== q) update({ q: draftQ || undefined }) }, 300)
    return () => window.clearTimeout(timer)
  }, [draftQ, q, update])

  if (runtime.isPending || sources.isPending) return <LoadingState />
  if (runtime.error || sources.error) return <PageError error={runtime.error || sources.error} retry={() => { void runtime.refetch(); void sources.refetch() }} />
  return <div className="page-stack">
    <PageHeader title="历史内容" subtitle="统一浏览 B 站与知识星球归档；基线、编辑、删除和恢复只更新档案，不重复通知。" />
    <Card><div className="filter-grid">
      <SelectField label="平台" value={platform || ''} onChange={value => update({ platform: value || undefined, source_id: undefined })} options={[{ value: '', label: '全部平台' }, { value: 'bilibili', label: 'B 站' }, { value: 'zsxq', label: '知识星球' }]} />
      <SelectField label="采集源" value={sourceID} onChange={value => update({ source_id: value || undefined })} options={[{ value: '', label: '全部来源' }, ...sources.data.map(source => ({ value: source.id, label: source.name || source.external_id }))]} />
      <TextField label="关键字" value={draftQ} onChange={setDraftQ} />
      <NativeDateTimeField label="开始时间" value={from} onChange={value => update({ from: value || undefined })} />
      <NativeDateTimeField label="结束时间" value={to} onChange={value => update({ to: value || undefined })} />
    </div></Card>
    {contents.isPending ? <LoadingState /> : contents.error ? <PageError error={contents.error} retry={() => void contents.refetch()} /> : contents.data.items.length === 0 ? <EmptyState icon="◷" title="当前筛选下没有历史记录" /> : <div className="list-stack">{contents.data.items.map(item => <ContentCard key={item.id} item={item} timeZone={runtime.data.timezone} sourceName={sources.data.find(source => source.id === item.source_id)?.name} onOpen={() => setSelectedID(item.id)} />)}</div>}
    {contents.data && <div className="pagination"><Button isDisabled={!after} onPress={() => history.back()}>← 上一页</Button><Button isDisabled={!contents.data.page.has_more} onPress={() => update({ after: contents.data.page.next_cursor }, false)}>下一页 →</Button></div>}
    <Dialog open={Boolean(selectedID)} onClose={() => setSelectedID('')} title={detail.data?.content.title || '内容详情'} actions={<Button onPress={() => setSelectedID('')}>关闭</Button>}>
      {detail.isPending ? <LoadingState /> : detail.error ? <Alert tone="danger">{detail.error.message}</Alert> : detail.data && <div className="list-stack">
        <div className="badge-row"><Badge tone="info">{platformLabel(detail.data.content.platform)}</Badge><Badge>{detail.data.content.type}</Badge>{detail.data.content.baseline && <Badge>基线</Badge>}{detail.data.content.deleted_at && <Badge tone="danger">已删除</Badge>}</div>
        <p className="muted">{detail.data.content.author_name || detail.data.content.author_id} · {formatDate(detail.data.content.published_at, runtime.data.timezone)}</p>
        {detail.data.content.text && <p className="preserve-lines">{detail.data.content.text}</p>}
        {safeExternalURL(detail.data.content.url) && <a className="button button--outline" href={safeExternalURL(detail.data.content.url)} target="_blank" rel="noreferrer">查看原内容 ↗</a>}
        {detail.data.attachments.length > 0 && <Card><strong>附件</strong><ul>{detail.data.attachments.map(attachment => <li key={attachment.id}>{attachment.local_path ? <a href={`/api/v3/contents/${encodeURIComponent(selectedID)}/attachments/${encodeURIComponent(attachment.id)}`}>{attachment.file_name || attachment.external_id}</a> : <span>{attachment.file_name || attachment.external_id}</span>}{attachment.size ? ` · ${formatBytes(attachment.size)}` : ''}{attachment.localize_error ? ` · ${attachment.localize_error}` : ''}</li>)}</ul></Card>}
        <Card><strong>评论树</strong>{comments.isPending ? <LoadingState /> : comments.error ? <Alert tone="danger">{comments.error.message}</Alert> : comments.data && <>{comments.data.incomplete && <Alert tone="warning">上游分页或父子关系不完整，当前树可能缺少节点。</Alert>}{comments.data.children.length === 0 ? <p className="muted">暂无评论</p> : <div className="comment-thread">{comments.data.children.map(node => <CommentBranch key={node.id} node={node} timeZone={runtime.data.timezone} />)}</div>}</>}</Card>
      </div>}
    </Dialog>
  </div>
}

function ContentCard({ item, timeZone, sourceName, onOpen }: { item: UnifiedContent; timeZone: string; sourceName?: string; onOpen: () => void }) {
  return <Card className="clickable-card"><button type="button" onClick={onOpen} aria-label={`查看内容：${item.title || item.text || item.external_id}`}><div className="card-title"><div><h2>{item.title || item.text?.slice(0, 80) || item.external_id}</h2><p>{platformLabel(item.platform)} · {sourceName || item.source_id} · {item.author_name || item.author_id}</p></div><div className="badge-row">{item.baseline && <Badge>基线</Badge>}{item.deleted_at && <Badge tone="danger">已删除</Badge>}{item.tree_incomplete && <Badge tone="warning">树不完整</Badge>}</div></div><p>{formatDate(item.published_at, timeZone)}</p></button></Card>
}

function CommentBranch({ node, timeZone }: { node: CommentTreeNode; timeZone: string }) {
  const role = roleLabel(node.author_role)
  return <article className={node.is_trigger ? 'comment comment--trigger' : 'comment'}><div><strong>{node.name || node.author_id}</strong>{role && <Badge tone="info">{role}</Badge>}{node.is_trigger && <Badge tone="success">新增触发</Badge>}{node.deleted_at && <Badge tone="danger">已删除</Badge>}<time>{formatDate(node.time, timeZone)}</time></div><p>{node.message}</p>{node.children?.map(child => <div key={child.id} className="comment-thread"><CommentBranch node={child} timeZone={timeZone} /></div>)}</article>
}

function platformLabel(platform: string) { return platform === 'zsxq' ? '知识星球' : 'B 站' }
function roleLabel(role?: string) { return ({ owner: '星球主', admin: '管理员', guest: '嘉宾', partner: '合伙人', up: 'UP 主' } as Record<string, string>)[role || ''] || '' }
function formatBytes(value: number) { if (value < 1024) return `${value} B`; if (value < 1024 ** 2) return `${(value / 1024).toFixed(1)} KiB`; return `${(value / 1024 ** 2).toFixed(1)} MiB` }
function safeExternalURL(raw?: string) { try { const url = new URL(raw || ''); return url.protocol === 'https:' ? url.toString() : '' } catch { return '' } }
