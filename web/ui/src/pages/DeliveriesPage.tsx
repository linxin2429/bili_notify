import { useState } from 'react'
import CheckCircle from '@mui/icons-material/CheckCircle'
import Refresh from '@mui/icons-material/Refresh'
import { Box, Button, Card, CardContent, Chip, CircularProgress, Paper, Stack, Tab, Tabs, Typography } from '@mui/material'
import { useSearchParams } from 'react-router-dom'
import type { AdminAPI } from '../api'
import { deliverySummary, deliveryTitle, formatDate } from '../presentation'
import type { Channel, Delivery } from '../types'
import { EmptyState, PageHeader, type RunMutation } from '../app/shared'

export function DeliveriesPage({ deliveries, channels, total, timeZone, api, runMutation, refreshDashboard }: { deliveries: Delivery[]; channels: Channel[]; total: number; timeZone: string; api: AdminAPI; runMutation: RunMutation; refreshDashboard: () => Promise<void> }) {
  const [params, setParams] = useSearchParams()
  const [retrying, setRetrying] = useState<Set<string>>(() => new Set())
  const requested = params.get('state')
  const filter = requested === 'pending' || requested === 'blocked' ? requested : 'all'
  const visible = deliveries.filter(delivery => filter === 'all' || delivery.state === filter)
  const retry = async (id: string) => {
    setRetrying(current => new Set(current).add(id))
    try { await runMutation(() => api.retryDelivery(id)); await refreshDashboard() } catch { /* shared handler reports it */ } finally {
      setRetrying(current => { const next = new Set(current); next.delete(id); return next })
    }
  }
  return <Stack spacing={3}><PageHeader title="投递队列" subtitle={`共 ${total} 个任务，页面展示前 ${deliveries.length} 个。`} /><Paper><Tabs value={filter} onChange={(_, value) => setParams(value === 'all' ? {} : { state: value })} variant="scrollable"><Tab value="all" label="全部" /><Tab value="pending" label="等待重试" /><Tab value="blocked" label="已阻塞" /></Tabs></Paper>{visible.length === 0 ? <EmptyState icon={<CheckCircle />} title="当前筛选下没有待投递任务" /> : <Stack spacing={1.5}>{visible.map(delivery => <Card key={delivery.id}><CardContent><Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" gap={2}><Box minWidth={0}><Stack direction="row" spacing={1} alignItems="center"><Chip size="small" color={delivery.state === 'blocked' ? 'error' : 'warning'} label={delivery.state === 'blocked' ? '已阻塞' : '等待重试'} /><Typography fontWeight={750}>{deliveryTitle(delivery)}</Typography></Stack><Typography className="summary-clamp" sx={{ mt: 1 }}>{deliverySummary(delivery)}</Typography><Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>渠道：{channels.find(channel => channel.id === delivery.channel_id)?.name || delivery.channel_id} · 已尝试 {delivery.attempts} 次</Typography>{delivery.last_error && <Typography variant="body2" color="error" sx={{ mt: .5 }}>上次错误：{delivery.last_error}</Typography>}</Box><Stack flexShrink={0} alignItems={{ xs: 'stretch', sm: 'flex-start' }} spacing={1}><Box><Typography variant="body2" color="text.secondary">下次处理</Typography><Typography>{formatDate(delivery.next_at, timeZone)}</Typography></Box>{delivery.state === 'blocked' && <Button variant="outlined" startIcon={retrying.has(delivery.id) ? <CircularProgress size={18} /> : <Refresh />} disabled={retrying.has(delivery.id)} onClick={() => void retry(delivery.id)}>{retrying.has(delivery.id) ? '正在提交' : '立即重试'}</Button>}</Stack></Stack></CardContent></Card>)}</Stack>}</Stack>
}
