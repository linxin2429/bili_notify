import { useEffect, useState } from 'react'
import ChevronLeft from '@mui/icons-material/ChevronLeft'
import ChevronRight from '@mui/icons-material/ChevronRight'
import History from '@mui/icons-material/History'
import OpenInNew from '@mui/icons-material/OpenInNew'
import { Alert, Box, Button, Card, CardActionArea, CardContent, Chip, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, FormControl, InputLabel, MenuItem, Paper, Select, Stack, Tab, Tabs, TextField, Typography, useMediaQuery } from '@mui/material'
import { useSearchParams } from 'react-router-dom'
import type { AdminAPI } from '../api'
import { DateTimeField } from '../app/DateTimeField'
import { DynamicHistoryCard } from '../history/DynamicHistoryCard'
import { dynamicTypeLabel, errorMessage, formatDate, localInputToRFC3339 } from '../presentation'
import type { CommentDetail, CommentHistoryItem, DynamicHistoryItem, UP } from '../types'
import { EmptyState, PageHeader } from '../app/shared'

const pageSize = 20

export function HistoryPage({ ups, timeZone, api, refresh }: { ups: UP[]; timeZone: string; api: AdminAPI; refresh: number }) {
  const [params, setParams] = useSearchParams()
  const tab = params.get('tab') === 'comments' ? 'comments' : 'dynamics'
  const uid = params.get('uid') || ''; const q = params.get('q') || ''; const from = params.get('from') || ''; const to = params.get('to') || ''
  const offset = Math.max(0, Number(params.get('offset') || '0') || 0)
  const [draftQ, setDraftQ] = useState(q)
  const [items, setItems] = useState<Array<DynamicHistoryItem | CommentHistoryItem>>([])
  const [total, setTotal] = useState(0); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  const [commentDetail, setCommentDetail] = useState<CommentDetail | null>(null)
  const mobile = useMediaQuery(theme => theme.breakpoints.down('sm'))
  const updateParams = (patch: Record<string, string | undefined>) => {
    const next = new URLSearchParams(params)
    for (const [key, value] of Object.entries(patch)) value ? next.set(key, value) : next.delete(key)
    if (!('offset' in patch)) next.delete('offset')
    setParams(next)
  }
  useEffect(() => { setDraftQ(q) }, [q])
  useEffect(() => { const handle = window.setTimeout(() => { if (draftQ !== q) updateParams({ q: draftQ || undefined }) }, 300); return () => window.clearTimeout(handle) }, [draftQ])
  useEffect(() => {
    let cancelled = false
    const run = async () => {
      setBusy(true); setError('')
      try {
        const payload = { ...(uid ? { uid } : {}), ...(q ? { q } : {}), ...(from ? { from: localInputToRFC3339(from) } : {}), ...(to ? { to: localInputToRFC3339(to) } : {}), limit: pageSize, offset }
        const page = tab === 'comments' ? await api.queryComments(payload) : await api.queryDynamics(payload)
        if (!cancelled) { setItems(page.items || []); setTotal(page.total || 0) }
      } catch (err) { if (!cancelled) { setItems([]); setTotal(0); setError(errorMessage(err)) } } finally { if (!cancelled) setBusy(false) }
    }
    void run()
    return () => { cancelled = true }
  }, [tab, uid, q, from, to, offset, api, refresh])
  const openCommentDetail = async (id: string) => {
    try { setCommentDetail(await api.getComment(id)) } catch (err) { setError(errorMessage(err)) }
  }
  const pageEnd = Math.min(offset + pageSize, total)
  return <Stack spacing={3}>
    <PageHeader title="历史内容" subtitle="浏览已采集的动态与 UP 回复；首次基线内容也会入库，但不发通知。" />
    <Paper><Tabs value={tab} onChange={(_, value) => updateParams({ tab: value === 'comments' ? 'comments' : undefined })} variant="scrollable"><Tab value="dynamics" label="动态" /><Tab value="comments" label="UP 回复" /></Tabs></Paper>
    <Paper sx={{ p: 2 }}><Stack direction={{ xs: 'column', md: 'row' }} spacing={1.5}><FormControl sx={{ minWidth: 180 }}><InputLabel id="history-up-label">UP 主</InputLabel><Select labelId="history-up-label" label="UP 主" value={uid} onChange={event => updateParams({ uid: event.target.value || undefined })}><MenuItem value="">全部</MenuItem>{ups.map(up => <MenuItem key={up.uid} value={up.uid}>{up.name || up.uid}</MenuItem>)}</Select></FormControl><TextField label="关键字" value={draftQ} onChange={event => setDraftQ(event.target.value)} fullWidth /><DateTimeField label="开始时间" value={from} onChange={event => updateParams({ from: event.target.value || undefined })} /><DateTimeField label="结束时间" value={to} onChange={event => updateParams({ to: event.target.value || undefined })} /></Stack></Paper>
    {error && <Alert severity="error">{error}</Alert>}
    {busy && items.length === 0 ? <Box display="grid" sx={{ placeItems: 'center', py: 8 }}><CircularProgress /></Box> : items.length === 0 ? <EmptyState icon={<History />} title="当前筛选下没有历史记录" /> : <Stack spacing={1.5}>{items.map(item => tab === 'comments' ? <CommentHistoryCard key={(item as CommentHistoryItem).rpid} item={item as CommentHistoryItem} timeZone={timeZone} onOpen={() => void openCommentDetail((item as CommentHistoryItem).rpid)} /> : <DynamicHistoryCard key={(item as DynamicHistoryItem).id} item={item as DynamicHistoryItem} timeZone={timeZone} />)}</Stack>}
    {total > 0 && <Stack direction="row" justifyContent="space-between" alignItems="center"><Typography variant="body2" color="text.secondary">共 {total} 条，当前 {offset + 1}-{pageEnd}</Typography><Stack direction="row" spacing={1}><Button startIcon={<ChevronLeft />} disabled={offset <= 0 || busy} onClick={() => updateParams({ offset: String(Math.max(0, offset - pageSize)) })}>上一页</Button><Button endIcon={<ChevronRight />} disabled={offset + pageSize >= total || busy} onClick={() => updateParams({ offset: String(offset + pageSize) })}>下一页</Button></Stack></Stack>}
    <CommentHistoryDialog open={Boolean(commentDetail)} detail={commentDetail} timeZone={timeZone} fullScreen={mobile} onClose={() => setCommentDetail(null)} />
  </Stack>
}

function CommentHistoryCard({ item, timeZone, onOpen }: { item: CommentHistoryItem; timeZone: string; onOpen: () => void }) {
  return <Card><CardActionArea onClick={onOpen} aria-label={`查看评论对话：${item.content_title || 'UP 回复'}`}><CardContent><Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" gap={2}><Box minWidth={0}><Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap"><Typography fontWeight={750}>{item.content_title || 'UP 回复'}</Typography>{item.baseline && <Chip size="small" label="基线" variant="outlined" />}{item.incomplete && <Chip size="small" color="warning" label="串不完整" />}</Stack><Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>{item.up_name || item.up_uid} · {dynamicTypeLabel(item.content_type || '')}</Typography></Box><Box flexShrink={0}><Typography variant="body2" color="text.secondary">回复时间</Typography><Typography>{formatDate(item.published_at, timeZone)}</Typography></Box></Stack></CardContent></CardActionArea></Card>
}

function CommentHistoryDialog({ open, detail, timeZone, fullScreen, onClose }: { open: boolean; detail: CommentDetail | null; timeZone: string; fullScreen: boolean; onClose: () => void }) {
  if (!detail) return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm" />
  return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm"><DialogTitle>{detail.content_title || 'UP 回复'}</DialogTitle><DialogContent><Stack spacing={1.5} sx={{ pt: 1 }}><Typography variant="body2" color="text.secondary">{detail.up_name} · {formatDate(detail.published_at, timeZone)}</Typography>{detail.incomplete && <Alert severity="warning">对话串可能不完整（翻页窗口外）。</Alert>}{detail.thread?.map(node => <Paper key={node.rpid} variant="outlined" sx={{ p: 1.5, bgcolor: node.is_trigger ? 'action.selected' : 'background.paper' }}><Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap"><Typography fontWeight={700}>{node.name}</Typography>{node.is_up && <Chip size="small" label="UP" color="primary" />}{node.is_trigger && <Chip size="small" label="触发" />}<Typography variant="caption" color="text.secondary">{formatDate(node.time, timeZone)}</Typography></Stack><Typography sx={{ mt: .75 }} whiteSpace="pre-wrap">{node.message}</Typography></Paper>)}</Stack></DialogContent><DialogActions>{detail.content_url && <Button startIcon={<OpenInNew />} href={detail.content_url} target="_blank" rel="noopener noreferrer">打开原内容</Button>}<Button onClick={onClose}>关闭</Button></DialogActions></Dialog>
}
