import { useEffect, useState } from 'react'
import ChevronLeft from '@mui/icons-material/ChevronLeft'
import ChevronRight from '@mui/icons-material/ChevronRight'
import ReceiptLong from '@mui/icons-material/ReceiptLong'
import { Alert, Box, Button, Card, CardContent, Chip, CircularProgress, FormControl, InputLabel, MenuItem, Paper, Select, Stack, TextField, Typography } from '@mui/material'
import { useSearchParams } from 'react-router-dom'
import type { AdminAPI } from '../api'
import { DateTimeField } from '../app/DateTimeField'
import { auditActionLabel, auditResult, errorMessage, formatDate, localInputToRFC3339 } from '../presentation'
import type { AuditLog } from '../types'
import { EmptyState, PageHeader } from '../app/shared'

const pageSize = 20
const auditActions = ['auth.setup', 'auth.login', 'auth.logout', 'auth.password.change', 'up.create', 'up.update', 'up.delete', 'channel.create', 'channel.update', 'channel.delete', 'channel.test', 'delivery.retry', 'bilibili.login.start', 'bilibili.login.cancel', 'microsoft.login.start', 'microsoft.login.cancel', 'settings.update']

export function AuditLogsPage({ api, timeZone, refresh }: { api: AdminAPI; timeZone: string; refresh: number }) {
  const [params, setParams] = useSearchParams()
  const action = params.get('action') || ''; const outcome = params.get('outcome') || ''; const q = params.get('q') || ''; const from = params.get('from') || ''; const to = params.get('to') || ''
  const offset = Math.max(0, Number(params.get('offset') || '0') || 0)
  const [draftQ, setDraftQ] = useState(q); const [items, setItems] = useState<AuditLog[]>([]); const [total, setTotal] = useState(0); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  const updateParams = (patch: Record<string, string | undefined>) => { const next = new URLSearchParams(params); for (const [key, value] of Object.entries(patch)) value ? next.set(key, value) : next.delete(key); if (!('offset' in patch)) next.delete('offset'); setParams(next) }
  useEffect(() => { setDraftQ(q) }, [q])
  useEffect(() => { const handle = window.setTimeout(() => { if (draftQ !== q) updateParams({ q: draftQ || undefined }) }, 300); return () => window.clearTimeout(handle) }, [draftQ])
  useEffect(() => {
    let cancelled = false
    const run = async () => { setBusy(true); setError(''); try { const page = await api.queryAuditLogs({ ...(action ? { action } : {}), ...(outcome ? { outcome } : {}), ...(q ? { q } : {}), ...(from ? { from: localInputToRFC3339(from) } : {}), ...(to ? { to: localInputToRFC3339(to) } : {}), limit: pageSize, offset }); if (!cancelled) { setItems(page.items || []); setTotal(page.total || 0) } } catch (err) { if (!cancelled) { setItems([]); setTotal(0); setError(errorMessage(err)) } } finally { if (!cancelled) setBusy(false) } }
    void run(); return () => { cancelled = true }
  }, [action, outcome, q, from, to, offset, api, refresh])
  const pageEnd = Math.min(offset + pageSize, total)
  return <Stack spacing={3}><PageHeader title="操作日志" subtitle="查询管理员认证、配置变更和手动运维操作；日志不会保存密码、令牌或 Webhook 内容。" />
    <Paper sx={{ p: 2 }}><Stack direction={{ xs: 'column', md: 'row' }} spacing={1.5}><FormControl sx={{ minWidth: 190 }}><InputLabel id="audit-action-label">操作</InputLabel><Select labelId="audit-action-label" label="操作" value={action} onChange={event => updateParams({ action: event.target.value || undefined })}><MenuItem value="">全部操作</MenuItem>{auditActions.map(value => <MenuItem key={value} value={value}>{auditActionLabel(value)}</MenuItem>)}</Select></FormControl><FormControl sx={{ minWidth: 140 }}><InputLabel id="audit-outcome-label">结果</InputLabel><Select labelId="audit-outcome-label" label="结果" value={outcome} onChange={event => updateParams({ outcome: event.target.value || undefined })}><MenuItem value="">全部结果</MenuItem><MenuItem value="success">成功</MenuItem><MenuItem value="failure">失败</MenuItem><MenuItem value="denied">已拒绝</MenuItem></Select></FormControl><TextField label="资源、IP 或请求 ID" value={draftQ} onChange={event => setDraftQ(event.target.value)} fullWidth /><DateTimeField label="开始时间" value={from} onChange={event => updateParams({ from: event.target.value || undefined })} /><DateTimeField label="结束时间" value={to} onChange={event => updateParams({ to: event.target.value || undefined })} /></Stack></Paper>
    {error && <Alert severity="error">{error}</Alert>}{busy && items.length === 0 ? <Box display="grid" sx={{ placeItems: 'center', py: 8 }}><CircularProgress /></Box> : items.length === 0 ? <EmptyState icon={<ReceiptLong />} title="当前筛选下没有操作日志" /> : <Stack spacing={1.5}>{items.map(item => <AuditLogCard key={item.id} item={item} timeZone={timeZone} />)}</Stack>}
    {total > 0 && <Stack direction="row" justifyContent="space-between" alignItems="center"><Typography variant="body2" color="text.secondary">共 {total} 条，当前 {offset + 1}-{pageEnd}</Typography><Stack direction="row" spacing={1}><Button startIcon={<ChevronLeft />} disabled={offset <= 0 || busy} onClick={() => updateParams({ offset: String(Math.max(0, offset - pageSize)) })}>上一页</Button><Button endIcon={<ChevronRight />} disabled={offset + pageSize >= total || busy} onClick={() => updateParams({ offset: String(offset + pageSize) })}>下一页</Button></Stack></Stack>}
  </Stack>
}

function AuditLogCard({ item, timeZone }: { item: AuditLog; timeZone: string }) {
  const [expanded, setExpanded] = useState(false)
  const result = auditResult(item)
  const target = item.resource_id ? `${item.resource_type} · ${item.resource_id}` : item.resource_type
  return <Card><CardContent><Stack spacing={1.25}><Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" gap={1.5}><Box><Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap"><Chip size="small" color={result.color} label={result.label} /><Typography fontWeight={750}>{auditActionLabel(item.action)}</Typography>{target && <Typography variant="body2" color="text.secondary">{target}</Typography>}</Stack><Typography variant="body2" color="text.secondary" sx={{ mt: .75 }}>{formatDate(item.occurred_at, timeZone)} · {item.remote_ip || '未知来源'} · HTTP {item.status_code} · {item.duration_ms} ms</Typography></Box><Button size="small" onClick={() => setExpanded(value => !value)} aria-expanded={expanded}>{expanded ? '收起详情' : '查看详情'}</Button></Stack>{expanded && <Box component="dl" sx={{ m: 0, display: 'grid', gridTemplateColumns: { xs: '1fr', sm: '130px 1fr' }, gap: .75, overflowWrap: 'anywhere' }}><Typography component="dt" color="text.secondary">请求 ID</Typography><Typography component="dd" sx={{ m: 0, fontFamily: 'monospace' }}>{item.request_id}</Typography><Typography component="dt" color="text.secondary">会话</Typography><Typography component="dd" sx={{ m: 0, fontFamily: 'monospace' }}>{item.session_id || '未认证'}</Typography><Typography component="dt" color="text.secondary">操作者</Typography><Typography component="dd" sx={{ m: 0 }}>{item.actor === 'administrator' ? '管理员' : '匿名来源'}</Typography><Typography component="dt" color="text.secondary">User-Agent</Typography><Typography component="dd" sx={{ m: 0 }}>{item.user_agent || '未提供'}</Typography><Typography component="dt" color="text.secondary">路由</Typography><Typography component="dd" sx={{ m: 0, fontFamily: 'monospace' }}>{item.http_method} {item.route}</Typography>{item.error_code && <><Typography component="dt" color="text.secondary">错误码</Typography><Typography component="dd" sx={{ m: 0 }}>{item.error_code}</Typography></>}<Typography component="dt" color="text.secondary">安全变更摘要</Typography><Box component="dd" sx={{ m: 0 }}><Box component="pre" sx={{ m: 0, p: 1.5, bgcolor: 'action.hover', borderRadius: 1, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{JSON.stringify(item.details || {}, null, 2)}</Box></Box></Box>}</Stack></CardContent></Card>
}
