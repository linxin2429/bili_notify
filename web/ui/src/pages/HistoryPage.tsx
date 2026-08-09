import { useQuery } from '@tanstack/react-query'
import { useCallback, useEffect, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import type { CommentHistoryItem, ContentQuery } from '../shared/api/types'
import { queries } from '../shared/api/query'
import { Alert, Badge, Button, Card, EmptyState, LoadingState, NativeDateTimeField, PageError, PageHeader, SelectField, TextField } from '../shared/ui'
import { Dialog } from '../shared/ui/Dialog'
import { DynamicHistoryCard } from '../modules/history'
import { dynamicTypeLabel, formatDate, localInputToRFC3339, safeBilibiliURL } from '../shared/lib/presentation'

export function HistoryPage() {
  const [params, setParams] = useSearchParams(); const tab = params.get('tab') === 'comments' ? 'comments' : 'dynamics'; const uid = params.get('uid') || ''; const q = params.get('q') || ''; const from = params.get('from') || ''; const to = params.get('to') || ''; const after = params.get('after') || ''
  const [draftQ, setDraftQ] = useState(q); const [detailID, setDetailID] = useState('')
  const ups = useQuery(queries.ups()); const runtime = useQuery(queries.runtime())
  const query: ContentQuery = { ...(uid && { uid }), ...(q && { q }), ...(from && { from: localInputToRFC3339(from) }), ...(to && { to: localInputToRFC3339(to) }), ...(after && { after }), limit: 20 }
  const dynamics = useQuery({ ...queries.dynamics(query), enabled: tab === 'dynamics' }); const comments = useQuery({ ...queries.comments(query), enabled: tab === 'comments' }); const detail = useQuery({ ...queries.comment(detailID), enabled: Boolean(detailID) })
  const page = tab === 'comments' ? comments : dynamics
  const update = useCallback((patch: Record<string, string | undefined>, resetCursor = true) => { const next = new URLSearchParams(params); for (const [key, value] of Object.entries(patch)) { if (value) next.set(key, value); else next.delete(key) } if (resetCursor && !('after' in patch)) next.delete('after'); setParams(next) }, [params, setParams])
  useEffect(() => { const timer = window.setTimeout(() => { if (draftQ !== q) update({ q: draftQ || undefined }) }, 300); return () => window.clearTimeout(timer) }, [draftQ, q, update])
  if (ups.isPending || runtime.isPending) return <LoadingState />
  if (ups.error || runtime.error) return <PageError error={ups.error || runtime.error} retry={() => { void ups.refetch(); void runtime.refetch() }} />
  return <div className="page-stack"><PageHeader title="历史内容" subtitle="浏览已采集的动态与 UP 回复；首次基线内容入库但不发通知。" />
    <div className="tabs" role="tablist" aria-label="历史类型"><Button role="tab" aria-selected={tab === 'dynamics'} className={tab === 'dynamics' ? 'tab--active' : ''} onPress={() => update({ tab: undefined })}>动态</Button><Button role="tab" aria-selected={tab === 'comments'} className={tab === 'comments' ? 'tab--active' : ''} onPress={() => update({ tab: 'comments' })}>UP 回复</Button></div>
    <Card><div className="filter-grid"><SelectField label="UP 主" value={uid} onChange={value => update({ uid: value || undefined })} options={[{ value: '', label: '全部' }, ...ups.data.map(up => ({ value: up.uid, label: up.name || up.uid }))]} /><TextField label="关键字" value={draftQ} onChange={setDraftQ} /><NativeDateTimeField label="开始时间" value={from} onChange={value => update({ from: value || undefined })} /><NativeDateTimeField label="结束时间" value={to} onChange={value => update({ to: value || undefined })} /></div></Card>
    {page.isPending ? <LoadingState /> : page.error ? <PageError error={page.error} retry={() => void page.refetch()} /> : page.data.items.length === 0 ? <EmptyState icon="◷" title="当前筛选下没有历史记录" /> : <div className="list-stack">{page.data.items.map(item => tab === 'comments' ? <CommentCard key={(item as CommentHistoryItem).rpid} item={item as CommentHistoryItem} timeZone={runtime.data.timezone} onOpen={() => setDetailID((item as CommentHistoryItem).rpid)} /> : <DynamicHistoryCard key={'id' in item ? item.id : ''} item={item as never} timeZone={runtime.data.timezone} />)}</div>}
    {page.data && <div className="pagination"><Button isDisabled={!after} onPress={() => history.back()}>← 上一页</Button><Button isDisabled={!page.data.page.has_more} onPress={() => update({ after: page.data.page.next_cursor }, false)}>下一页 →</Button></div>}
    <Dialog open={Boolean(detailID)} onClose={() => setDetailID('')} title={detail.data?.content_title || '评论对话'} actions={<Button onPress={() => setDetailID('')}>关闭</Button>}>{detail.isPending ? <LoadingState /> : detail.error ? <Alert tone="danger">{detail.error.message}</Alert> : detail.data && <div className="comment-thread"><p className="muted">{detail.data.up_name} · {formatDate(detail.data.published_at, runtime.data.timezone)}</p>{detail.data.incomplete && <Alert tone="warning">当前评论串不完整。</Alert>}{detail.data.thread.map(entry => <article key={entry.rpid} className={entry.is_trigger ? 'comment comment--trigger' : 'comment'}><div><strong>{entry.name}</strong>{entry.is_up && <Badge tone="info">UP 主</Badge>}<time>{formatDate(entry.time, runtime.data.timezone)}</time></div><p>{entry.message}</p></article>)}{safeBilibiliURL(detail.data.content_url) && <a className="button button--outline" href={safeBilibiliURL(detail.data.content_url)} target="_blank" rel="noreferrer">在 B 站查看 ↗</a>}</div>}</Dialog>
  </div>
}

function CommentCard({ item, timeZone, onOpen }: { item: CommentHistoryItem; timeZone: string; onOpen: () => void }) { return <Card className="clickable-card"><button type="button" onClick={onOpen} aria-label={`查看评论对话：${item.content_title || 'UP 回复'}`}><div className="card-title"><div><h2>{item.content_title || 'UP 回复'}</h2><p>{item.up_name || item.up_uid} · {dynamicTypeLabel(item.content_type || '')}</p></div><div className="badge-row">{item.baseline && <Badge>基线</Badge>}{item.incomplete && <Badge tone="warning">串不完整</Badge>}</div></div><p>{formatDate(item.published_at, timeZone)}</p></button></Card> }
