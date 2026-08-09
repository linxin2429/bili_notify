import { useState } from 'react'
import CheckCircle from '@mui/icons-material/CheckCircle'
import ErrorOutlined from '@mui/icons-material/ErrorOutlined'
import Hub from '@mui/icons-material/Hub'
import LiveTv from '@mui/icons-material/LiveTv'
import NotificationsActive from '@mui/icons-material/NotificationsActive'
import People from '@mui/icons-material/People'
import QrCode2 from '@mui/icons-material/QrCode2'
import WarningAmber from '@mui/icons-material/WarningAmber'
import { Alert, Avatar, Box, Button, Card, CardContent, Chip, Divider, Stack, Typography } from '@mui/material'
import type { AdminAPI } from '../api'
import { applyBiliLoginMutation, readinessMessage } from '../dashboard'
import { formatDate, loginLabel } from '../presentation'
import type { BiliLogin, DashboardSnapshot } from '../types'
import type { RunMutation } from '../app/shared'

export function OverviewPage({ snapshot, api, runMutation }: { snapshot: DashboardSnapshot; api: AdminAPI; runMutation: RunMutation }) {
  const status = snapshot.status
  const [busy, setBusy] = useState(false)
  const startLogin = async () => {
    setBusy(true)
    try { await runMutation(() => api.startBiliLogin(), applyBiliLoginMutation) } catch { /* shared handler reports it */ } finally { setBusy(false) }
  }
  const cancelLogin = async (id: string) => {
    try { await runMutation(() => api.cancelBiliLogin(id), current => applyBiliLoginMutation(current, null)) } catch { /* shared handler reports it */ }
  }
  return <Stack spacing={3}>
    <Box><Typography component="h1" variant="h4" fontWeight={850}>运行概览</Typography><Typography color="text.secondary">第一眼确认服务是否正在发现并可靠投递动态。</Typography></Box>
    <Alert severity={status.ready ? 'success' : 'warning'} icon={status.ready ? <CheckCircle /> : <WarningAmber />} sx={{ py: 1.5 }}>
      <Typography component="h2" variant="h6" fontWeight={800}>{status.ready ? '服务已就绪' : '服务尚未就绪'}</Typography><Typography>{readinessMessage(snapshot)}</Typography>
    </Alert>
    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr 1fr', lg: 'repeat(4, 1fr)' }, gap: 2 }}>
      <MetricCard label="B站登录" value={status.auth_valid ? '有效' : '未登录'} icon={<LiveTv />} tone={status.auth_valid ? 'success.main' : 'warning.main'} />
      <MetricCard label="UP 主" value={`${status.up_count}`} detail={`${snapshot.ups.filter(up => up.enabled).length} 个已启用`} icon={<People />} />
      <MetricCard label="通知渠道" value={`${status.channel_count}`} detail={`${snapshot.channels.filter(channel => channel.enabled).length} 个已启用`} icon={<NotificationsActive />} />
      <MetricCard label="待投递" value={`${status.outbox_depth}`} detail={status.oldest_delivery ? `最早 ${formatDate(status.oldest_delivery, snapshot.timezone)}` : '队列为空'} icon={<Hub />} tone={status.outbox_depth ? 'warning.main' : 'success.main'} />
    </Box>
    {status.risk_paused_until && <Alert severity="error" icon={<ErrorOutlined />}>B站风控暂停至 {formatDate(status.risk_paused_until, snapshot.timezone)}，程序不会尝试绕过风控。</Alert>}
    <Typography variant="body2" color="text.secondary">当前采集参数：每 {snapshot.settings.poll_interval_sec} 秒轮询 · {snapshot.settings.request_rate} 请求/秒 · 并发 {snapshot.settings.request_concurrency} · 评论监控{snapshot.settings.comment_enabled ? '开' : '关'}（N={snapshot.settings.comment_track_n}，批次 {snapshot.settings.comment_batch_interval_sec}s）</Typography>
    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', lg: 'minmax(0, 1.35fr) minmax(320px, .65fr)' }, gap: 2 }}>
      <Card><CardContent><Stack spacing={2}><Stack direction="row" justifyContent="space-between" alignItems="center"><Box><Typography component="h2" variant="h6" fontWeight={800}>B站账号</Typography><Typography color="text.secondary">{status.bili_account ? `${status.bili_account.name || '已登录账号'} · UID ${status.bili_account.uid}` : '使用哔哩哔哩 App 扫码建立网页会话'}</Typography></Box><QrCode2 color="primary" /></Stack><BiliLoginPanel login={snapshot.bili_login || null} busy={busy} timeZone={snapshot.timezone} start={() => void startLogin()} cancel={id => void cancelLogin(id)} /></Stack></CardContent></Card>
      <Card><CardContent><Typography component="h2" variant="h6" fontWeight={800} gutterBottom>启动检查</Typography><Stack spacing={1.5}><Checklist done={status.auth_valid} label="B站账号已登录" /><Checklist done={snapshot.channels.some(channel => channel.enabled)} label="至少一个通知渠道已启用" /><Checklist done={snapshot.ups.some(up => up.enabled)} label="至少一个 UP 主已启用" /></Stack><Divider sx={{ my: 2 }} /><Typography variant="body2" color="text.secondary">最后成功采集：{status.last_success_at ? formatDate(status.last_success_at, snapshot.timezone) : '尚无记录'}</Typography></CardContent></Card>
    </Box>
  </Stack>
}

function MetricCard({ label, value, detail, icon, tone = 'primary.main' }: { label: string; value: string; detail?: string; icon: React.ReactNode; tone?: string }) {
  return <Card><CardContent><Stack direction="row" justifyContent="space-between" gap={1}><Box minWidth={0}><Typography color="text.secondary" variant="body2">{label}</Typography><Typography fontWeight={850} sx={{ mt: .5, fontSize: { xs: '1.9rem', sm: '2.15rem' }, lineHeight: 1.15, wordBreak: 'keep-all' }}>{value}</Typography>{detail && <Typography variant="body2" color="text.secondary">{detail}</Typography>}</Box><Avatar sx={{ bgcolor: tone, color: 'white', flexShrink: 0 }}>{icon}</Avatar></Stack></CardContent></Card>
}

function Checklist({ done, label }: { done: boolean; label: string }) {
  return <Stack direction="row" spacing={1.25} alignItems="center">{done ? <CheckCircle color="success" /> : <WarningAmber color="warning" />}<Typography>{label}</Typography></Stack>
}

function BiliLoginPanel({ login, busy, timeZone, start, cancel }: { login: BiliLogin | null; busy: boolean; timeZone: string; start: () => void; cancel: (id: string) => void }) {
  if (!login || ['success', 'expired'].includes(login.status)) return <Button variant="contained" startIcon={<QrCode2 />} onClick={start} disabled={busy}>{busy ? '正在生成…' : '生成登录二维码'}</Button>
  return <Stack alignItems="center"><Chip label={loginLabel(login.status)} color={login.status === 'scanned' ? 'info' : 'warning'} />{login.qr_data_url && <img src={login.qr_data_url} className="qr-image" alt="哔哩哔哩登录二维码" />}<Typography color="text.secondary">二维码有效至 {formatDate(login.expires_at, timeZone)}</Typography><Button color="inherit" onClick={() => cancel(login.id)}>取消本次登录</Button></Stack>
}
