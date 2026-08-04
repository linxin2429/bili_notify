import { useCallback, useEffect, useMemo, useState } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import {
  Add, Autorenew, BrightnessAuto, CheckCircle, ChevronLeft, ChevronRight, DarkMode, Dashboard, Delete, Edit, Email,
  ErrorOutlined, History, Hub, LightMode, LiveTv, Logout, Menu as MenuIcon, NotificationsActive, OpenInNew,
  Password, People, PlayArrow, QrCode2, Refresh, Science, Settings, WarningAmber,
} from '@mui/icons-material'
import {
  Alert, AppBar, Avatar, BottomNavigation, BottomNavigationAction, Box, Button, Card, CardContent,
  Chip, CircularProgress, Container, CssBaseline, Dialog, DialogActions, DialogContent, DialogTitle,
  Divider, Drawer, FormControl, FormControlLabel, IconButton, InputLabel, List, ListItemButton,
  ListItemIcon, ListItemText, MenuItem, Paper, Select, Snackbar, Stack, Switch, Tab, Tabs,
  TextField, ThemeProvider, Toolbar, Tooltip, Typography, createTheme, useMediaQuery,
} from '@mui/material'
import type {
  BiliLogin, Channel, ChannelDraft, ChannelType, CommentDetail, CommentHistoryItem, ConnectionState,
  DashboardSnapshot, Delivery, DynamicDetail, DynamicHistoryItem, MicrosoftLogin,
  RuntimeSettings, ThemePreference, UP,
} from './types'
import { RealtimeClient } from './realtime'
import { AdminAPI, httpJSON } from './api'
import { applyUpdate, readinessMessage } from './dashboard'

const drawerWidth = 236
const navigation = [
  { path: '/overview', label: '概览', icon: <Dashboard /> },
  { path: '/ups', label: 'UP 主', icon: <People /> },
  { path: '/channels', label: '通知渠道', icon: <NotificationsActive /> },
  { path: '/deliveries', label: '投递队列', icon: <Hub /> },
  { path: '/history', label: '历史', icon: <History /> },
  { path: '/settings', label: '设置', icon: <Settings /> },
]
const pageSize = 20

interface SessionState { setup_required: boolean; authenticated: boolean; csrf_token?: string }

function useThemePreference() {
  const [preference, setPreference] = useState<ThemePreference>(() => (localStorage.getItem('theme') as ThemePreference) || 'system')
  const systemDark = useMediaQuery('(prefers-color-scheme: dark)')
  const mode = preference === 'system' ? (systemDark ? 'dark' : 'light') : preference
  const theme = useMemo(() => createTheme({
    palette: {
      mode,
      primary: { main: '#fb7299' },
      secondary: { main: '#23ade5' },
      background: mode === 'dark' ? { default: '#101116', paper: '#191b22' } : { default: '#f6f7fb', paper: '#ffffff' },
    },
    shape: { borderRadius: 14 },
    typography: { fontFamily: 'Inter, "Noto Sans SC", "Microsoft YaHei", system-ui, sans-serif' },
    components: {
      MuiButton: { styleOverrides: { root: { minHeight: 42, textTransform: 'none', fontWeight: 650 } } },
      MuiIconButton: { styleOverrides: { root: { minWidth: 44, minHeight: 44 } } },
      MuiCard: { styleOverrides: { root: { border: mode === 'dark' ? '1px solid #2b2e39' : '1px solid #e7e9f1', boxShadow: 'none' } } },
    },
  }), [mode])
  const update = (value: ThemePreference) => { localStorage.setItem('theme', value); setPreference(value) }
  return { theme, preference, update }
}

export default function App() {
  const { theme, preference, update } = useThemePreference()
  const [session, setSession] = useState<SessionState | null>(null)
  const [message, setMessage] = useState('')

  const refreshSession = useCallback(async () => {
    try { setSession(await httpJSON<SessionState>('/api/v1/session')) }
    catch (error) { setMessage(errorMessage(error)) }
  }, [])
  useEffect(() => { void refreshSession() }, [])

  return <ThemeProvider theme={theme}>
    <CssBaseline />
    {!session ? <LoadingScreen /> : session.authenticated && session.csrf_token
      ? <Console csrf={session.csrf_token} themePreference={preference} setThemePreference={update} onAuthLost={refreshSession} />
      : <AuthScreen setup={session.setup_required} onAuthenticated={state => setSession({ setup_required: false, authenticated: true, csrf_token: state.csrf_token })} />}
    <Snackbar open={Boolean(message)} autoHideDuration={5000} onClose={() => setMessage('')} message={message} />
  </ThemeProvider>
}

function LoadingScreen() {
  return <Box minHeight="100vh" display="grid" sx={{ placeItems: 'center' }}><Stack alignItems="center" spacing={2}><CircularProgress /><Typography color="text.secondary">正在连接 Bili Notify</Typography></Stack></Box>
}

function AuthScreen({ setup, onAuthenticated }: { setup: boolean; onAuthenticated: (state: { csrf_token: string }) => void }) {
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const submit = async () => {
    if (setup && password !== confirm) { setError('两次输入的密码不一致'); return }
    setBusy(true); setError('')
    try {
      const state = await httpJSON<{ csrf_token: string }>(setup ? '/api/v1/setup' : '/api/v1/session', {
        method: 'POST', body: JSON.stringify(setup ? { setup_code: code, password } : { password }),
      })
      onAuthenticated(state)
    } catch (err) { setError(errorMessage(err)) } finally { setBusy(false) }
  }
  return <Box minHeight="100vh" display="grid" sx={{ placeItems: 'center', p: 2, background: theme => theme.palette.mode === 'dark' ? 'radial-gradient(circle at top, #251925, #101116 46%)' : 'radial-gradient(circle at top, #fff0f5, #f6f7fb 48%)' }}>
    <Card sx={{ width: '100%', maxWidth: 440 }}><CardContent sx={{ p: { xs: 3, sm: 5 } }}>
      <Stack spacing={3}>
        <Stack direction="row" spacing={2} alignItems="center"><Avatar sx={{ bgcolor: 'primary.main', width: 52, height: 52, fontWeight: 800 }}>BN</Avatar><Box><Typography variant="h5" fontWeight={800}>Bili Notify</Typography><Typography color="text.secondary">{setup ? '完成安全初始化' : '登录实时管理台'}</Typography></Box></Stack>
        {setup && <Alert severity="info">初始化码只会输出到本次容器启动日志。设置成功后立即失效。</Alert>}
        {setup && <TextField label="初始化码" value={code} onChange={e => setCode(e.target.value.toUpperCase())} autoComplete="one-time-code" fullWidth />}
        <TextField label={setup ? '设置管理员密码' : '管理员密码'} type="password" value={password} onChange={e => setPassword(e.target.value)} autoComplete={setup ? 'new-password' : 'current-password'} helperText={setup ? '至少 12 个字节' : undefined} fullWidth onKeyDown={e => { if (e.key === 'Enter' && !setup) void submit() }} />
        {setup && <TextField label="确认密码" type="password" value={confirm} onChange={e => setConfirm(e.target.value)} autoComplete="new-password" fullWidth onKeyDown={e => { if (e.key === 'Enter') void submit() }} />}
        {error && <Alert severity="error">{error}</Alert>}
        <Button variant="contained" size="large" disabled={busy || !password || (setup && !code)} onClick={() => void submit()}>{busy ? '处理中…' : setup ? '初始化并登录' : '登录'}</Button>
      </Stack>
    </CardContent></Card>
  </Box>
}

function Console({ csrf, themePreference, setThemePreference, onAuthLost }: { csrf: string; themePreference: ThemePreference; setThemePreference: (value: ThemePreference) => void; onAuthLost: () => void }) {
  const [snapshot, setSnapshot] = useState<DashboardSnapshot | null>(null)
  const [connection, setConnection] = useState<ConnectionState>('connecting')
  const [message, setMessage] = useState('')
  const [mobileOpen, setMobileOpen] = useState(false)
  const api = useMemo(() => new AdminAPI(csrf), [csrf])
  const mobile = useMediaQuery(theme => theme.breakpoints.down('md'))
  const location = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    let stopped = false
    const client = new RealtimeClient({
      onSnapshot: value => { displayTimeZone = usableTimeZone(value.timezone); setSnapshot(value) },
      onEvent: (event, data) => setSnapshot(current => applyUpdate(current, event, data)),
      onState: setConnection,
      onAuthLost,
      onError: setMessage,
    })
    void api.dashboard()
      .then(value => {
        if (!stopped) {
          displayTimeZone = usableTimeZone(value.timezone)
          setSnapshot(value)
        }
      })
      .catch(error => { if (!stopped) setMessage(errorMessage(error)) })
      .finally(() => { if (!stopped) client.start() })
    return () => { stopped = true; client.stop() }
  }, [api, onAuthLost])
  const logout = async () => {
    try { await httpJSON('/api/v1/session', { method: 'DELETE' }, csrf) } finally { await onAuthLost() }
  }
  const activePath = navigation.find(item => location.pathname.startsWith(item.path))?.path || '/overview'
  const navigateTo = (path: string) => { navigate(path); setMobileOpen(false) }
  const connectionMeta = connectionPresentation(connection)

  return <Box minHeight="100vh" bgcolor="background.default">
    <AppBar position="fixed" color="inherit" elevation={0} sx={{ borderBottom: 1, borderColor: 'divider', zIndex: theme => theme.zIndex.drawer + 1 }}>
      <Toolbar>
        {mobile && <IconButton edge="start" onClick={() => setMobileOpen(true)} aria-label="打开导航"><MenuIcon /></IconButton>}
        <Avatar sx={{ bgcolor: 'primary.main', width: 34, height: 34, fontSize: 14, fontWeight: 800, ml: mobile ? 1 : 0 }}>BN</Avatar>
        <Typography fontWeight={800} sx={{ ml: 1.25, flexGrow: 1 }}>Bili Notify</Typography>
        <Tooltip title={`实时状态：${connectionMeta.label}`}><Chip size="small" icon={connectionMeta.icon} color={connectionMeta.color} label={connectionMeta.label} variant={connection === 'live' ? 'filled' : 'outlined'} /></Tooltip>
        <Tooltip title={`主题：${themeLabel(themePreference)}`}><IconButton onClick={() => setThemePreference(nextTheme(themePreference))} aria-label="切换主题">{themePreference === 'system' ? <BrightnessAuto /> : themePreference === 'dark' ? <DarkMode /> : <LightMode />}</IconButton></Tooltip>
        <Tooltip title="退出登录"><IconButton onClick={() => void logout()} aria-label="退出登录"><Logout /></IconButton></Tooltip>
      </Toolbar>
    </AppBar>
    <Drawer variant={mobile ? 'temporary' : 'permanent'} open={mobile ? mobileOpen : true} onClose={() => setMobileOpen(false)} sx={{ width: drawerWidth, flexShrink: 0, '& .MuiDrawer-paper': { width: drawerWidth, mt: '64px', height: 'calc(100% - 64px)', borderRightColor: 'divider' } }}>
      <List sx={{ p: 1.5 }}>{navigation.map(item => <ListItemButton key={item.path} selected={activePath === item.path} onClick={() => navigateTo(item.path)} sx={{ borderRadius: 2, mb: .5 }}><ListItemIcon>{item.icon}</ListItemIcon><ListItemText primary={item.label} /></ListItemButton>)}</List>
    </Drawer>
    <Box component="main" sx={{ ml: mobile ? 0 : `${drawerWidth}px`, pt: '64px', pb: mobile ? '74px' : 3 }}>
      {connection !== 'live' && snapshot && <Alert severity="warning" icon={<Autorenew />} sx={{ borderRadius: 0 }}>实时连接已中断，正在保留 {formatDate(snapshot.updated_at)} 的最后状态并尝试重连。</Alert>}
      <Container maxWidth="xl" sx={{ py: { xs: 2, sm: 3 } }}>
        {!snapshot ? <Box minHeight="50vh" display="grid" sx={{ placeItems: 'center' }}><Stack alignItems="center" spacing={2}><CircularProgress /><Typography color="text.secondary">正在加载实时状态</Typography></Stack></Box> :
          <Routes>
            <Route path="/overview" element={<Overview snapshot={snapshot} api={api} />} />
            <Route path="/ups" element={<UPsPage ups={snapshot.ups} api={api} />} />
            <Route path="/channels" element={<ChannelsPage channels={snapshot.channels} logins={snapshot.microsoft_logins} api={api} />} />
            <Route path="/deliveries" element={<DeliveriesPage deliveries={snapshot.deliveries} channels={snapshot.channels} total={snapshot.status.outbox_depth} />} />
            <Route path="/history" element={<HistoryPage ups={snapshot.ups} api={api} />} />
            <Route path="/settings" element={<SettingsPage csrf={csrf} preference={themePreference} setPreference={setThemePreference} settings={snapshot.settings} api={api} onChanged={onAuthLost} />} />
            <Route path="*" element={<Navigate to="/overview" replace />} />
          </Routes>}
      </Container>
    </Box>
    {mobile && <Paper elevation={6} sx={{ position: 'fixed', left: 0, right: 0, bottom: 0, zIndex: theme => theme.zIndex.appBar }}><BottomNavigation value={activePath} onChange={(_, value) => navigateTo(value)} showLabels>{navigation.slice(0, 5).map(item => <BottomNavigationAction key={item.path} value={item.path} label={item.label} icon={item.icon} />)}</BottomNavigation></Paper>}
    <Snackbar open={Boolean(message)} autoHideDuration={6000} onClose={() => setMessage('')} message={message} />
  </Box>
}

function Overview({ snapshot, api }: { snapshot: DashboardSnapshot; api: AdminAPI }) {
  const status = snapshot.status
  const [busy, setBusy] = useState(false)
  const startLogin = async () => { setBusy(true); try { await api.startBiliLogin() } finally { setBusy(false) } }
  return <Stack spacing={3}>
    <Box><Typography variant="h4" fontWeight={850}>运行概览</Typography><Typography color="text.secondary">第一眼确认服务是否正在发现并可靠投递动态。</Typography></Box>
    <Alert severity={status.ready ? 'success' : 'warning'} icon={status.ready ? <CheckCircle /> : <WarningAmber />} sx={{ py: 1.5 }}>
      <Typography variant="h6" fontWeight={800}>{status.ready ? '服务已就绪' : '服务尚未就绪'}</Typography>
      <Typography>{readinessMessage(snapshot)}</Typography>
    </Alert>
    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr 1fr', lg: 'repeat(4, 1fr)' }, gap: 2 }}>
      <MetricCard label="B站登录" value={status.auth_valid ? '有效' : '未登录'} icon={<LiveTv />} tone={status.auth_valid ? 'success.main' : 'warning.main'} />
      <MetricCard label="UP 主" value={`${status.up_count}`} detail={`${snapshot.ups.filter(up => up.enabled).length} 个已启用`} icon={<People />} />
      <MetricCard label="通知渠道" value={`${status.channel_count}`} detail={`${snapshot.channels.filter(channel => channel.enabled).length} 个已启用`} icon={<NotificationsActive />} />
      <MetricCard label="待投递" value={`${status.outbox_depth}`} detail={status.oldest_delivery ? `最早 ${formatDate(status.oldest_delivery)}` : '队列为空'} icon={<Hub />} tone={status.outbox_depth ? 'warning.main' : 'success.main'} />
    </Box>
    {status.risk_paused_until && <Alert severity="error" icon={<ErrorOutlined />}>B站风控暂停至 {formatDate(status.risk_paused_until)}，程序不会尝试绕过风控。</Alert>}
    <Typography variant="body2" color="text.secondary">当前采集参数：每 {snapshot.settings.poll_interval_sec} 秒轮询 · {snapshot.settings.request_rate} 请求/秒 · 并发 {snapshot.settings.request_concurrency} · 评论监控{snapshot.settings.comment_enabled ? '开' : '关'}（N={snapshot.settings.comment_track_n}，批次 {snapshot.settings.comment_batch_interval_sec}s）</Typography>
    <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', lg: 'minmax(0, 1.35fr) minmax(320px, .65fr)' }, gap: 2 }}>
      <Card><CardContent><Stack spacing={2}><Stack direction="row" justifyContent="space-between" alignItems="center"><Box><Typography variant="h6" fontWeight={800}>B站账号</Typography><Typography color="text.secondary">使用哔哩哔哩 App 扫码建立网页会话</Typography></Box><QrCode2 color="primary" /></Stack><BiliLoginPanel login={snapshot.bili_login || null} busy={busy} start={() => void startLogin()} cancel={id => void api.cancelBiliLogin(id)} /></Stack></CardContent></Card>
      <Card><CardContent><Typography variant="h6" fontWeight={800} gutterBottom>启动检查</Typography><Stack spacing={1.5}><Checklist done={status.auth_valid} label="B站账号已登录" /><Checklist done={snapshot.channels.some(channel => channel.enabled)} label="至少一个通知渠道已启用" /><Checklist done={snapshot.ups.some(up => up.enabled)} label="至少一个 UP 主已启用" /></Stack><Divider sx={{ my: 2 }} /><Typography variant="body2" color="text.secondary">最后成功采集：{status.last_success_at ? formatDate(status.last_success_at) : '尚无记录'}</Typography></CardContent></Card>
    </Box>
  </Stack>
}

function MetricCard({ label, value, detail, icon, tone = 'primary.main' }: { label: string; value: string; detail?: string; icon: React.ReactNode; tone?: string }) {
  return <Card><CardContent><Stack direction="row" justifyContent="space-between" gap={1}><Box minWidth={0}><Typography color="text.secondary" variant="body2">{label}</Typography><Typography fontWeight={850} sx={{ mt: .5, fontSize: { xs: '1.9rem', sm: '2.15rem' }, lineHeight: 1.15, wordBreak: 'keep-all' }}>{value}</Typography>{detail && <Typography variant="body2" color="text.secondary">{detail}</Typography>}</Box><Avatar sx={{ bgcolor: tone, color: 'white', flexShrink: 0 }}>{icon}</Avatar></Stack></CardContent></Card>
}

function Checklist({ done, label }: { done: boolean; label: string }) {
  return <Stack direction="row" spacing={1.25} alignItems="center">{done ? <CheckCircle color="success" /> : <WarningAmber color="warning" />}<Typography>{label}</Typography></Stack>
}

function BiliLoginPanel({ login, busy, start, cancel }: { login: BiliLogin | null; busy: boolean; start: () => void; cancel: (id: string) => void }) {
  if (!login || ['success', 'expired'].includes(login.status)) return <Button variant="contained" startIcon={<QrCode2 />} onClick={start} disabled={busy}>{busy ? '正在生成…' : '生成登录二维码'}</Button>
  return <Stack alignItems="center"><Chip label={loginLabel(login.status)} color={login.status === 'scanned' ? 'info' : 'warning'} />{login.qr_data_url && <img src={login.qr_data_url} className="qr-image" alt="哔哩哔哩登录二维码" />}<Typography color="text.secondary">二维码有效至 {formatDate(login.expires_at)}</Typography><Button color="inherit" onClick={() => cancel(login.id)}>取消本次登录</Button></Stack>
}

function UPsPage({ ups, api }: { ups: UP[]; api: AdminAPI }) {
  const [editing, setEditing] = useState<UP | null | undefined>(undefined)
  const mobile = useMediaQuery(theme => theme.breakpoints.down('sm'))
  const save = async (value: { uid: string; name: string; enabled: boolean }) => { await (editing ? api.updateUP(value) : api.createUP(value)); setEditing(undefined) }
  const remove = async (uid: string) => { if (confirm('删除该 UP 主及其去重状态？')) await api.deleteUP(uid) }
  return <Stack spacing={3}><PageHeader title="UP 主" subtitle="管理需要轮询的公开账号；首次采集只建立基线。" action={<Button variant="contained" startIcon={<Add />} onClick={() => setEditing(null)}>添加 UP 主</Button>} />
    {ups.length === 0 ? <EmptyState icon={<People />} title="尚未添加 UP 主" action={<Button variant="contained" onClick={() => setEditing(null)}>添加第一个 UP 主</Button>} /> :
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', lg: 'repeat(2, 1fr)' }, gap: 2 }}>{ups.map(up => <Card key={up.uid}><CardContent><Stack spacing={2}><Stack direction="row" justifyContent="space-between" alignItems="start"><Box><Typography variant="h6" fontWeight={800}>{up.name || `UID ${up.uid}`}</Typography><Typography color="text.secondary">UID {up.uid}</Typography></Box><Chip label={up.enabled ? '已启用' : '已停用'} color={up.enabled ? 'success' : 'default'} /></Stack><Stack direction="row" spacing={1} flexWrap="wrap"><Chip size="small" label={up.baseline_ready ? '基线已建立' : '等待基线'} /><Chip size="small" label={`连续失败 ${up.consecutive_fail} 次`} color={up.consecutive_fail ? 'warning' : 'default'} /></Stack>{up.last_error && <Alert severity="error">{up.last_error}</Alert>}<Typography variant="body2" color="text.secondary">最后成功：{up.last_success_at ? formatDate(up.last_success_at) : '尚无记录'}</Typography><Stack direction="row" spacing={1}><Button startIcon={<Edit />} onClick={() => setEditing(up)}>编辑</Button><Button color="error" startIcon={<Delete />} onClick={() => void remove(up.uid)}>删除</Button></Stack></Stack></CardContent></Card>)}</Box>}
    <UPDialog open={editing !== undefined} value={editing || undefined} fullScreen={mobile} onClose={() => setEditing(undefined)} onSave={save} />
  </Stack>
}

function UPDialog({ open, value, fullScreen, onClose, onSave }: { open: boolean; value?: UP; fullScreen: boolean; onClose: () => void; onSave: (value: { uid: string; name: string; enabled: boolean }) => Promise<void> }) {
  const [uid, setUID] = useState(''); const [name, setName] = useState(''); const [enabled, setEnabled] = useState(true); const [busy, setBusy] = useState(false)
  useEffect(() => { setUID(value?.uid || ''); setName(value?.name || ''); setEnabled(value?.enabled ?? true) }, [value, open])
  const submit = async () => { setBusy(true); try { await onSave({ uid, name, enabled }) } finally { setBusy(false) } }
  return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm"><DialogTitle>{value ? '编辑 UP 主' : '添加 UP 主'}</DialogTitle><DialogContent><Stack spacing={2} sx={{ pt: 1 }}><TextField label="UID" value={uid} onChange={e => setUID(e.target.value)} disabled={Boolean(value)} inputMode="numeric" required /><TextField label="备注名" value={name} onChange={e => setName(e.target.value)} /><FormControlLabel control={<Switch checked={enabled} onChange={e => setEnabled(e.target.checked)} />} label="启用轮询" /></Stack></DialogContent><DialogActions><Button onClick={onClose}>取消</Button><Button variant="contained" disabled={busy || !uid} onClick={() => void submit()}>保存</Button></DialogActions></Dialog>
}

function ChannelsPage({ channels, logins, api }: { channels: Channel[]; logins: MicrosoftLogin[]; api: AdminAPI }) {
  const [editing, setEditing] = useState<Channel | null | undefined>(undefined)
  const mobile = useMediaQuery(theme => theme.breakpoints.down('sm'))
  const save = async (draft: ChannelDraft) => { await (draft.id ? api.updateChannel(draft as ChannelDraft & { id: string }) : api.createChannel(draft)); setEditing(undefined) }
  const remove = async (id: string) => { if (confirm('存在待投递任务时不能删除渠道。继续？')) await api.deleteChannel(id) }
  const authorize = async (channelID: string) => {
    const login = await api.startMicrosoftLogin(channelID)
    const url = login.verification_uri_complete || login.verification_uri
    if (url) window.open(url, '_blank', 'noopener,noreferrer')
  }
  return <Stack spacing={3}><PageHeader title="通知渠道" subtitle="秘密字段仅写入，不会返回浏览器。" action={<Button variant="contained" startIcon={<Add />} onClick={() => setEditing(null)}>添加渠道</Button>} />
    {channels.length === 0 ? <EmptyState icon={<NotificationsActive />} title="尚未配置通知渠道" action={<Button variant="contained" onClick={() => setEditing(null)}>添加第一个渠道</Button>} /> :
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', xl: 'repeat(2, 1fr)' }, gap: 2 }}>{channels.map(channel => { const login = logins.find(item => item.channel_id === channel.id); return <Card key={channel.id}><CardContent><Stack spacing={2}><Stack direction="row" justifyContent="space-between"><Stack direction="row" spacing={1.5} alignItems="center"><Avatar sx={{ bgcolor: 'secondary.main' }}><Email /></Avatar><Box><Typography variant="h6" fontWeight={800}>{channel.name}</Typography><Typography color="text.secondary">{channelTypeLabel(channel.type)}</Typography></Box></Stack><Chip label={channel.enabled ? '已启用' : '已停用'} color={channel.enabled ? 'success' : 'default'} /></Stack><Divider /><ChannelSummary channel={channel} />{channel.type === 'microsoft' && <MicrosoftAuthorization channel={channel} login={login} authorize={() => void authorize(channel.id)} cancel={() => void api.cancelMicrosoftLogin(channel.id)} />}<Stack direction="row" spacing={1} flexWrap="wrap"><Button startIcon={<Edit />} onClick={() => setEditing(channel)}>编辑</Button><Button startIcon={<Science />} onClick={() => void api.testChannel(channel.id)}>发送测试</Button><Button color="error" startIcon={<Delete />} onClick={() => void remove(channel.id)}>删除</Button></Stack></Stack></CardContent></Card> })}</Box>}
    <ChannelDialog open={editing !== undefined} channel={editing || undefined} fullScreen={mobile} onClose={() => setEditing(undefined)} onSave={save} />
  </Stack>
}

function ChannelSummary({ channel }: { channel: Channel }) {
  const entries = Object.entries(channel.settings).filter(([key]) => !['authorized', 'token_type', 'token_expiry'].includes(key))
  return <Stack spacing={.75}>{entries.map(([key, value]) => <Stack key={key} direction="row" justifyContent="space-between" gap={2}><Typography color="text.secondary" variant="body2">{settingLabel(key)}</Typography><Typography variant="body2" textAlign="right" sx={{ overflowWrap: 'anywhere' }}>{value}</Typography></Stack>)}{channel.configured_secrets.map(secret => <Stack key={secret} direction="row" justifyContent="space-between"><Typography color="text.secondary" variant="body2">{settingLabel(secret)}</Typography><Chip label="已安全保存" size="small" /></Stack>)}</Stack>
}

function MicrosoftAuthorization({ channel, login, authorize, cancel }: { channel: Channel; login?: MicrosoftLogin; authorize: () => void; cancel: () => void }) {
  const authorized = channel.settings.authorized === 'true'
  return <Alert severity={authorized ? 'success' : login?.status === 'pending' ? 'info' : 'warning'} action={login?.status === 'pending' ? <Button onClick={cancel}>取消</Button> : <Button onClick={authorize}>{authorized ? '重新授权' : '开始授权'}</Button>}>
    {login?.status === 'pending' ? <>打开 Microsoft 登录页并输入代码 <strong>{login.user_code}</strong>，正在等待授权。</> : login?.error || (authorized ? 'Microsoft 账户已授权。' : '必须完成 Microsoft 授权后才能启用。')}
  </Alert>
}

function ChannelDialog({ open, channel, fullScreen, onClose, onSave }: { open: boolean; channel?: Channel; fullScreen: boolean; onClose: () => void; onSave: (draft: ChannelDraft) => Promise<void> }) {
  const [name, setName] = useState(''); const [type, setType] = useState<ChannelType>('email'); const [enabled, setEnabled] = useState(true)
  const [fields, setFields] = useState<Record<string, string>>({}); const [secrets, setSecrets] = useState<Record<string, string>>({}); const [busy, setBusy] = useState(false)
  useEffect(() => { setName(channel?.name || ''); setType(channel?.type || 'email'); setEnabled(channel?.enabled ?? true); setFields(channel?.settings || {}); setSecrets({}) }, [channel, open])
  useEffect(() => { if (!channel && type === 'microsoft') setEnabled(false) }, [type, channel])
  const setField = (key: string, value: string) => setFields(current => ({ ...current, [key]: value }))
  const setSecret = (key: string, value: string) => setSecrets(current => ({ ...current, [key]: value }))
  const submit = async () => {
    const settings = channelFields(type).filter(field => !field.secret).reduce<Record<string, string>>((result, field) => ({ ...result, [field.key]: fields[field.key] || field.defaultValue || '' }), {})
    const changedSecrets = Object.fromEntries(Object.entries(secrets).filter(([, value]) => value !== ''))
    setBusy(true); try { await onSave({ id: channel?.id, name, type, enabled, settings, ...(Object.keys(changedSecrets).length ? { secrets: changedSecrets } : {}) }) } finally { setBusy(false) }
  }
  return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm"><DialogTitle>{channel ? '编辑通知渠道' : '添加通知渠道'}</DialogTitle><DialogContent><Stack spacing={2} sx={{ pt: 1 }}><TextField label="渠道名称" value={name} onChange={e => setName(e.target.value)} required /><FormControl><InputLabel id="channel-type-label">渠道类型</InputLabel><Select labelId="channel-type-label" label="渠道类型" value={type} onChange={e => { setType(e.target.value as ChannelType); setFields({}); setSecrets({}) }}>{(['email', 'microsoft', 'dingtalk', 'feishu', 'wecom'] as ChannelType[]).map(value => <MenuItem key={value} value={value}>{channelTypeLabel(value)}</MenuItem>)}</Select></FormControl>{channelFields(type).map(field => <TextField key={field.key} label={field.label} type={field.secret ? 'password' : 'text'} value={field.secret ? secrets[field.key] || '' : fields[field.key] || field.defaultValue || ''} onChange={e => field.secret ? setSecret(field.key, e.target.value) : setField(field.key, e.target.value)} required={field.required && !(channel?.configured_secrets.includes(field.key))} helperText={field.secret && channel?.configured_secrets.includes(field.key) ? '已安全保存；留空表示保留原值' : field.help} />)}<FormControlLabel control={<Switch checked={enabled} onChange={e => setEnabled(e.target.checked)} />} label="启用渠道" />{type === 'microsoft' && <Alert severity="info">保存后需要完成 Microsoft 设备码授权，再启用渠道。</Alert>}</Stack></DialogContent><DialogActions><Button onClick={onClose}>取消</Button><Button variant="contained" disabled={busy || !name} onClick={() => void submit()}>保存</Button></DialogActions></Dialog>
}

function DeliveriesPage({ deliveries, channels, total }: { deliveries: Delivery[]; channels: Channel[]; total: number }) {
  const [params, setParams] = useSearchParams()
  const requested = params.get('state')
  const filter = requested === 'pending' || requested === 'blocked' ? requested : 'all'
  const setFilter = (value: string) => setParams(value === 'all' ? {} : { state: value })
  const visible = deliveries.filter(delivery => filter === 'all' || delivery.state === filter)
  return <Stack spacing={3}><PageHeader title="投递队列" subtitle={`共 ${total} 个任务，页面展示前 ${deliveries.length} 个。`} /><Paper><Tabs value={filter} onChange={(_, value) => setFilter(value)} variant="scrollable"><Tab value="all" label="全部" /><Tab value="pending" label="等待重试" /><Tab value="blocked" label="已阻塞" /></Tabs></Paper>{visible.length === 0 ? <EmptyState icon={<CheckCircle />} title="当前筛选下没有待投递任务" /> : <Stack spacing={1.5}>{visible.map(delivery => <Card key={delivery.id}><CardContent><Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" gap={2}><Box minWidth={0}><Stack direction="row" spacing={1} alignItems="center"><Chip size="small" color={delivery.state === 'blocked' ? 'error' : 'warning'} label={delivery.state === 'blocked' ? '已阻塞' : '等待重试'} /><Typography fontWeight={750}>{deliveryTitle(delivery)}</Typography></Stack><Typography className="summary-clamp" sx={{ mt: 1 }}>{deliverySummary(delivery)}</Typography><Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>渠道：{channels.find(channel => channel.id === delivery.channel_id)?.name || delivery.channel_id} · 已尝试 {delivery.attempts} 次</Typography>{delivery.last_error && <Typography variant="body2" color="error" sx={{ mt: .5 }}>{delivery.last_error}</Typography>}</Box><Box flexShrink={0}><Typography variant="body2" color="text.secondary">下次处理</Typography><Typography>{formatDate(delivery.next_at)}</Typography></Box></Stack></CardContent></Card>)}</Stack>}</Stack>
}

function HistoryPage({ ups, api }: { ups: UP[]; api: AdminAPI }) {
  const [params, setParams] = useSearchParams()
  const tab = params.get('tab') === 'comments' ? 'comments' : 'dynamics'
  const uid = params.get('uid') || ''
  const q = params.get('q') || ''
  const from = params.get('from') || ''
  const to = params.get('to') || ''
  const offset = Math.max(0, Number(params.get('offset') || '0') || 0)
  const [draftQ, setDraftQ] = useState(q)
  const [items, setItems] = useState<Array<DynamicHistoryItem | CommentHistoryItem>>([])
  const [total, setTotal] = useState(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [detail, setDetail] = useState<{ kind: 'dynamics' | 'comments'; data: DynamicDetail | CommentDetail } | null>(null)
  const mobile = useMediaQuery(theme => theme.breakpoints.down('sm'))

  const updateParams = (patch: Record<string, string | undefined>) => {
    const next = new URLSearchParams(params)
    for (const [key, value] of Object.entries(patch)) {
      if (!value) next.delete(key)
      else next.set(key, value)
    }
    if (!('offset' in patch)) next.delete('offset')
    setParams(next)
  }

  useEffect(() => { setDraftQ(q) }, [q])
  useEffect(() => {
    const handle = window.setTimeout(() => {
      if (draftQ !== q) updateParams({ q: draftQ || undefined })
    }, 300)
    return () => window.clearTimeout(handle)
  }, [draftQ])

  useEffect(() => {
    let cancelled = false
    const run = async () => {
      setBusy(true); setError('')
      try {
        const payload = {
          ...(uid ? { uid } : {}),
          ...(q ? { q } : {}),
          ...(from ? { from: localInputToRFC3339(from) } : {}),
          ...(to ? { to: localInputToRFC3339(to) } : {}),
          limit: pageSize,
          offset,
        }
        const page = tab === 'comments'
          ? await api.queryComments<CommentHistoryItem>(payload)
          : await api.queryDynamics<DynamicHistoryItem>(payload)
        if (!cancelled) {
          setItems(page.items || [])
          setTotal(page.total || 0)
        }
      } catch (err) {
        if (!cancelled) {
          setItems([])
          setTotal(0)
          setError(errorMessage(err))
        }
      } finally {
        if (!cancelled) setBusy(false)
      }
    }
    void run()
    return () => { cancelled = true }
  }, [tab, uid, q, from, to, offset, api])

  const openDetail = async (kind: 'dynamics' | 'comments', id: string) => {
    try {
      if (kind === 'dynamics') {
        const data = await api.getDynamic(id)
        setDetail({ kind, data })
      } else {
        const data = await api.getComment(id)
        setDetail({ kind, data })
      }
    } catch (err) {
      setError(errorMessage(err))
    }
  }

  const pageEnd = Math.min(offset + pageSize, total)
  return <Stack spacing={3}>
    <PageHeader title="历史内容" subtitle="浏览已采集的动态与 UP 回复；首次基线内容也会入库，但不发通知。" />
    <Paper><Tabs value={tab} onChange={(_, value) => updateParams({ tab: value === 'comments' ? 'comments' : undefined })} variant="scrollable">
      <Tab value="dynamics" label="动态" /><Tab value="comments" label="UP 回复" />
    </Tabs></Paper>
    <Paper sx={{ p: 2 }}><Stack direction={{ xs: 'column', md: 'row' }} spacing={1.5}>
      <FormControl sx={{ minWidth: 180 }}><InputLabel id="history-up-label">UP 主</InputLabel>
        <Select labelId="history-up-label" label="UP 主" value={uid} onChange={e => updateParams({ uid: e.target.value || undefined })}>
          <MenuItem value="">全部</MenuItem>
          {ups.map(up => <MenuItem key={up.uid} value={up.uid}>{up.name || up.uid}</MenuItem>)}
        </Select>
      </FormControl>
      <TextField label="关键字" value={draftQ} onChange={e => setDraftQ(e.target.value)} fullWidth />
      <TextField label="开始时间" type="datetime-local" value={from} onChange={e => updateParams({ from: e.target.value || undefined })} InputLabelProps={{ shrink: true }} sx={{ minWidth: 210 }} />
      <TextField label="结束时间" type="datetime-local" value={to} onChange={e => updateParams({ to: e.target.value || undefined })} InputLabelProps={{ shrink: true }} sx={{ minWidth: 210 }} />
    </Stack></Paper>
    {error && <Alert severity="error">{error}</Alert>}
    {busy && items.length === 0 ? <Box display="grid" sx={{ placeItems: 'center', py: 8 }}><CircularProgress /></Box>
      : items.length === 0 ? <EmptyState icon={<History />} title="当前筛选下没有历史记录" />
        : <Stack spacing={1.5}>{items.map(item => tab === 'comments'
          ? <CommentHistoryCard key={(item as CommentHistoryItem).rpid} item={item as CommentHistoryItem} onOpen={() => void openDetail('comments', (item as CommentHistoryItem).rpid)} />
          : <DynamicHistoryCard key={(item as DynamicHistoryItem).id} item={item as DynamicHistoryItem} onOpen={() => void openDetail('dynamics', (item as DynamicHistoryItem).id)} />)}</Stack>}
    {total > 0 && <Stack direction="row" justifyContent="space-between" alignItems="center">
      <Typography variant="body2" color="text.secondary">共 {total} 条，当前 {offset + 1}-{pageEnd}</Typography>
      <Stack direction="row" spacing={1}>
        <Button startIcon={<ChevronLeft />} disabled={offset <= 0 || busy} onClick={() => updateParams({ offset: String(Math.max(0, offset - pageSize)) })}>上一页</Button>
        <Button endIcon={<ChevronRight />} disabled={offset + pageSize >= total || busy} onClick={() => updateParams({ offset: String(offset + pageSize) })}>下一页</Button>
      </Stack>
    </Stack>}
    <HistoryDetailDialog open={Boolean(detail)} detail={detail} fullScreen={mobile} onClose={() => setDetail(null)} />
  </Stack>
}

function DynamicHistoryCard({ item, onOpen }: { item: DynamicHistoryItem; onOpen: () => void }) {
  return <Card sx={{ cursor: 'pointer' }} onClick={onOpen}><CardContent>
    <Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" gap={2}>
      <Box minWidth={0}>
        <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap">
          <Typography fontWeight={750}>{item.title || item.summary || item.id}</Typography>
          {item.badge && <Chip size="small" label={item.badge} />}
          {item.baseline && <Chip size="small" label="基线" variant="outlined" />}
        </Stack>
        <Typography className="summary-clamp" sx={{ mt: 1 }}>{item.summary || '（无正文摘要）'}</Typography>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>{item.up_name || item.uid} · {dynamicTypeLabel(item.type)}</Typography>
      </Box>
      <Box flexShrink={0}><Typography variant="body2" color="text.secondary">发布时间</Typography><Typography>{formatDate(item.published_at)}</Typography></Box>
    </Stack>
  </CardContent></Card>
}

function CommentHistoryCard({ item, onOpen }: { item: CommentHistoryItem; onOpen: () => void }) {
  return <Card sx={{ cursor: 'pointer' }} onClick={onOpen}><CardContent>
    <Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" gap={2}>
      <Box minWidth={0}>
        <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap">
          <Typography fontWeight={750}>{item.content_title || 'UP 回复'}</Typography>
          {item.baseline && <Chip size="small" label="基线" variant="outlined" />}
          {item.incomplete && <Chip size="small" color="warning" label="串不完整" />}
        </Stack>
        <Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>{item.up_name || item.up_uid} · {dynamicTypeLabel(item.content_type || '')}</Typography>
      </Box>
      <Box flexShrink={0}><Typography variant="body2" color="text.secondary">回复时间</Typography><Typography>{formatDate(item.published_at)}</Typography></Box>
    </Stack>
  </CardContent></Card>
}

function HistoryDetailDialog({ open, detail, fullScreen, onClose }: {
  open: boolean
  detail: { kind: 'dynamics' | 'comments'; data: DynamicDetail | CommentDetail } | null
  fullScreen: boolean
  onClose: () => void
}) {
  if (!detail) return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm" />
  if (detail.kind === 'dynamics') {
    const d = detail.data as DynamicDetail
    return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm">
      <DialogTitle>{d.title || d.summary || d.id}</DialogTitle>
      <DialogContent><Stack spacing={1.5} sx={{ pt: 1 }}>
        <Typography variant="body2" color="text.secondary">{d.up_name} · {dynamicTypeLabel(d.type)} · {formatDate(d.published_at)}</Typography>
        {d.badge && <Chip size="small" label={d.badge} sx={{ alignSelf: 'flex-start' }} />}
        {d.summary && <Typography whiteSpace="pre-wrap">{d.summary}</Typography>}
        {d.description && <Typography color="text.secondary" whiteSpace="pre-wrap">{d.description}</Typography>}
        {d.media?.map(media => <Box key={media.url} component="img" src={media.url} alt="" sx={{ maxWidth: '100%', borderRadius: 2 }} />)}
        {d.original && <Alert severity="info">转发原文：{d.original.up_name} · {d.original.title || d.original.summary}</Alert>}
      </Stack></DialogContent>
      <DialogActions>
        {(d.target_url || d.url) && <Button startIcon={<OpenInNew />} href={(d.target_url || d.url)!} target="_blank" rel="noopener noreferrer">打开原内容</Button>}
        <Button onClick={onClose}>关闭</Button>
      </DialogActions>
    </Dialog>
  }
  const c = detail.data as CommentDetail
  return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm">
    <DialogTitle>{c.content_title || 'UP 回复'}</DialogTitle>
    <DialogContent><Stack spacing={1.5} sx={{ pt: 1 }}>
      <Typography variant="body2" color="text.secondary">{c.up_name} · {formatDate(c.published_at)}</Typography>
      {c.incomplete && <Alert severity="warning">对话串可能不完整（翻页窗口外）。</Alert>}
      {c.thread?.map(node => <Paper key={node.rpid} variant="outlined" sx={{ p: 1.5, bgcolor: node.is_trigger ? 'action.selected' : 'background.paper' }}>
        <Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap">
          <Typography fontWeight={700}>{node.name}</Typography>
          {node.is_up && <Chip size="small" label="UP" color="primary" />}
          {node.is_trigger && <Chip size="small" label="触发" />}
          <Typography variant="caption" color="text.secondary">{formatDate(node.time)}</Typography>
        </Stack>
        <Typography sx={{ mt: .75 }} whiteSpace="pre-wrap">{node.message}</Typography>
      </Paper>)}
    </Stack></DialogContent>
    <DialogActions>
      {c.content_url && <Button startIcon={<OpenInNew />} href={c.content_url} target="_blank" rel="noopener noreferrer">打开原内容</Button>}
      <Button onClick={onClose}>关闭</Button>
    </DialogActions>
  </Dialog>
}

function SettingsPage({ csrf, preference, setPreference, settings, api, onChanged }: {
  csrf: string
  preference: ThemePreference
  setPreference: (value: ThemePreference) => void
  settings: RuntimeSettings
  api: AdminAPI
  onChanged: () => void
}) {
  const [current, setCurrent] = useState('')
  const [replacement, setReplacement] = useState('')
  const [confirm, setConfirm] = useState('')
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [pollSec, setPollSec] = useState(String(settings.poll_interval_sec))
  const [requestRate, setRequestRate] = useState(String(settings.request_rate))
  const [concurrency, setConcurrency] = useState(String(settings.request_concurrency))
  const [commentEnabled, setCommentEnabled] = useState(Boolean(settings.comment_enabled))
  const [commentTrackN, setCommentTrackN] = useState(String(settings.comment_track_n ?? 10))
  const [commentRootPages, setCommentRootPages] = useState(String(settings.comment_root_pages ?? 2))
  const [commentReplyPages, setCommentReplyPages] = useState(String(settings.comment_reply_pages ?? 5))
  const [commentBatchSec, setCommentBatchSec] = useState(String(settings.comment_batch_interval_sec ?? 120))
  const [settingsMessage, setSettingsMessage] = useState('')
  const [settingsBusy, setSettingsBusy] = useState(false)

  useEffect(() => {
    setPollSec(String(settings.poll_interval_sec))
    setRequestRate(String(settings.request_rate))
    setConcurrency(String(settings.request_concurrency))
    setCommentEnabled(Boolean(settings.comment_enabled))
    setCommentTrackN(String(settings.comment_track_n ?? 10))
    setCommentRootPages(String(settings.comment_root_pages ?? 2))
    setCommentReplyPages(String(settings.comment_reply_pages ?? 5))
    setCommentBatchSec(String(settings.comment_batch_interval_sec ?? 120))
  }, [settings.poll_interval_sec, settings.request_rate, settings.request_concurrency, settings.comment_enabled, settings.comment_track_n, settings.comment_root_pages, settings.comment_reply_pages, settings.comment_batch_interval_sec])

  const change = async () => {
    if (replacement !== confirm) { setMessage('两次输入的新密码不一致'); return }
    setBusy(true)
    try {
      await httpJSON('/api/v1/session/password', { method: 'PUT', body: JSON.stringify({ current_password: current, new_password: replacement }) }, csrf)
      await onChanged()
    } catch (error) {
      setMessage(errorMessage(error))
    } finally {
      setBusy(false)
    }
  }

  const saveSettings = async () => {
    const poll_interval_sec = Number(pollSec)
    const request_rate = Number(requestRate)
    const request_concurrency = Number(concurrency)
    const comment_track_n = Number(commentTrackN)
    const comment_root_pages = Number(commentRootPages)
    const comment_reply_pages = Number(commentReplyPages)
    const comment_batch_interval_sec = Number(commentBatchSec)
    if (!Number.isInteger(poll_interval_sec) || poll_interval_sec < 10) {
      setSettingsMessage('轮询间隔至少为 10 秒的整数')
      return
    }
    if (!(request_rate > 0 && request_rate <= 10)) {
      setSettingsMessage('请求速率必须在 (0, 10] 内')
      return
    }
    if (!Number.isInteger(request_concurrency) || request_concurrency < 1 || request_concurrency > 16) {
      setSettingsMessage('并发数必须是 1 到 16 的整数')
      return
    }
    if (!Number.isInteger(comment_track_n) || comment_track_n < 1 || comment_track_n > 50) {
      setSettingsMessage('评论跟踪条数必须是 1 到 50 的整数')
      return
    }
    if (!Number.isInteger(comment_root_pages) || comment_root_pages < 1 || comment_root_pages > 10) {
      setSettingsMessage('根评论页数必须是 1 到 10 的整数')
      return
    }
    if (!Number.isInteger(comment_reply_pages) || comment_reply_pages < 1 || comment_reply_pages > 20) {
      setSettingsMessage('子评论页数必须是 1 到 20 的整数')
      return
    }
    if (!Number.isInteger(comment_batch_interval_sec) || comment_batch_interval_sec < 30) {
      setSettingsMessage('评论批次间隔至少为 30 秒')
      return
    }
    setSettingsBusy(true)
    setSettingsMessage('')
    try {
      await api.updateSettings({
        poll_interval_sec,
        request_rate,
        request_concurrency,
        comment_enabled: commentEnabled,
        comment_track_n,
        comment_root_pages,
        comment_reply_pages,
        comment_batch_interval_sec,
      })
    } catch (error) {
      setSettingsMessage(errorMessage(error))
    } finally {
      setSettingsBusy(false)
    }
  }

  return <Stack spacing={3}>
    <PageHeader title="设置" subtitle="管理采集参数、本浏览器外观与管理员凭据。" />
    <Card><CardContent>
      <Stack spacing={2} maxWidth={520}>
        <Box>
          <Typography variant="h6" fontWeight={800}>采集参数</Typography>
          <Typography color="text.secondary">修改后立即生效并写入数据库；重启后仍以这里的值为准。命令行参数仅在首次启动空库时作为默认值。</Typography>
        </Box>
        <TextField label="轮询间隔（秒）" type="number" value={pollSec} onChange={e => setPollSec(e.target.value)} helperText="至少 10 秒" inputProps={{ min: 10, step: 1 }} />
        <TextField label="请求速率（次/秒）" type="number" value={requestRate} onChange={e => setRequestRate(e.target.value)} helperText="(0, 10]" inputProps={{ min: 0.1, max: 10, step: 0.1 }} />
        <TextField label="并发数" type="number" value={concurrency} onChange={e => setConcurrency(e.target.value)} helperText="1 到 16" inputProps={{ min: 1, max: 16, step: 1 }} />
        <FormControlLabel control={<Switch checked={commentEnabled} onChange={e => setCommentEnabled(e.target.checked)} />} label="启用 UP 评论回复监控" />
        <TextField label="每 UP 跟踪内容数 N" type="number" value={commentTrackN} onChange={e => setCommentTrackN(e.target.value)} helperText="1 到 50；仅最近 N 条视频/动态/专栏" inputProps={{ min: 1, max: 50, step: 1 }} disabled={!commentEnabled} />
        <TextField label="根评论最大页数" type="number" value={commentRootPages} onChange={e => setCommentRootPages(e.target.value)} helperText="1 到 10，每页最多 20 条" inputProps={{ min: 1, max: 10, step: 1 }} disabled={!commentEnabled} />
        <TextField label="子评论最大页数" type="number" value={commentReplyPages} onChange={e => setCommentReplyPages(e.target.value)} helperText="1 到 20，用于展开根串" inputProps={{ min: 1, max: 20, step: 1 }} disabled={!commentEnabled} />
        <TextField label="评论批次间隔（秒）" type="number" value={commentBatchSec} onChange={e => setCommentBatchSec(e.target.value)} helperText="至少 30 秒；与动态轮询共用请求速率" inputProps={{ min: 30, step: 1 }} disabled={!commentEnabled} />
        {settingsMessage && <Alert severity="error">{settingsMessage}</Alert>}
        <Button variant="contained" disabled={settingsBusy} onClick={() => void saveSettings()}>保存采集参数</Button>
      </Stack>
    </CardContent></Card>
    <Card><CardContent>
      <Typography variant="h6" fontWeight={800}>外观</Typography>
      <Typography color="text.secondary" gutterBottom>跟随系统会响应操作系统的明暗模式。</Typography>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ mt: 2 }}>
        {(['system', 'light', 'dark'] as ThemePreference[]).map(value => (
          <Button key={value} variant={preference === value ? 'contained' : 'outlined'} startIcon={value === 'system' ? <BrightnessAuto /> : value === 'dark' ? <DarkMode /> : <LightMode />} onClick={() => setPreference(value)}>{themeLabel(value)}</Button>
        ))}
      </Stack>
    </CardContent></Card>
    <Card><CardContent>
      <Stack spacing={2} maxWidth={520}>
        <Box>
          <Typography variant="h6" fontWeight={800}>修改管理员密码</Typography>
          <Typography color="text.secondary">修改后所有设备会话都会立即失效。</Typography>
        </Box>
        <TextField label="当前密码" type="password" value={current} onChange={e => setCurrent(e.target.value)} autoComplete="current-password" />
        <TextField label="新密码" type="password" value={replacement} onChange={e => setReplacement(e.target.value)} autoComplete="new-password" helperText="至少 12 个字节" />
        <TextField label="确认新密码" type="password" value={confirm} onChange={e => setConfirm(e.target.value)} autoComplete="new-password" />
        {message && <Alert severity="error">{message}</Alert>}
        <Button variant="contained" startIcon={<Password />} disabled={busy || !current || !replacement} onClick={() => void change()}>修改密码</Button>
      </Stack>
    </CardContent></Card>
  </Stack>
}

function PageHeader({ title, subtitle, action }: { title: string; subtitle: string; action?: React.ReactNode }) { return <Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" alignItems={{ xs: 'stretch', sm: 'center' }} gap={2}><Box><Typography variant="h4" fontWeight={850}>{title}</Typography><Typography color="text.secondary">{subtitle}</Typography></Box>{action}</Stack> }
function EmptyState({ icon, title, action }: { icon: React.ReactNode; title: string; action?: React.ReactNode }) { return <Paper sx={{ py: 8, px: 2, textAlign: 'center' }}><Avatar sx={{ mx: 'auto', mb: 2, bgcolor: 'action.selected', color: 'text.secondary' }}>{icon}</Avatar><Typography variant="h6" gutterBottom>{title}</Typography>{action}</Paper> }

function channelFields(type: ChannelType) {
  const fields: Record<ChannelType, Array<{ key: string; label: string; required?: boolean; secret?: boolean; help?: string; defaultValue?: string }>> = {
    email: [
      { key: 'host', label: 'SMTP 主机', required: true }, { key: 'port', label: '端口', required: true, defaultValue: '465' },
      { key: 'tls', label: 'TLS 模式（tls 或 starttls）', required: true, defaultValue: 'tls' }, { key: 'username', label: '用户名' },
      { key: 'password', label: '密码', secret: true }, { key: 'from', label: '发件人', required: true }, { key: 'to', label: '收件人', required: true, help: '多个地址使用英文逗号分隔' },
    ],
    microsoft: [{ key: 'client_id', label: '应用程序（客户端）ID', required: true }, { key: 'tenant', label: '租户', defaultValue: 'common' }, { key: 'to', label: '收件人', required: true, help: '多个地址使用英文逗号分隔' }],
    dingtalk: [{ key: 'webhook', label: 'Webhook URL', required: true, secret: true }, { key: 'secret', label: '签名密钥', required: true, secret: true }],
    feishu: [{ key: 'webhook', label: 'Webhook URL', required: true, secret: true }, { key: 'secret', label: '签名密钥', required: true, secret: true }],
    wecom: [{ key: 'webhook', label: 'Webhook URL', required: true, secret: true }],
  }
  return fields[type]
}

function connectionPresentation(state: ConnectionState): { label: string; color: 'success' | 'warning' | 'default'; icon: React.ReactElement } {
  if (state === 'live') return { label: '实时', color: 'success', icon: <CheckCircle /> }
  if (state === 'connecting' || state === 'reconnecting') return { label: '重连中', color: 'warning', icon: <Refresh /> }
  return { label: '数据过期', color: 'warning', icon: <WarningAmber /> }
}
function nextTheme(value: ThemePreference): ThemePreference { return value === 'system' ? 'light' : value === 'light' ? 'dark' : 'system' }
function themeLabel(value: ThemePreference) { return value === 'system' ? '跟随系统' : value === 'light' ? '浅色' : '深色' }
function channelTypeLabel(value: ChannelType) { return ({ email: 'SMTP 邮件', microsoft: 'Microsoft Graph', dingtalk: '钉钉机器人', feishu: '飞书机器人', wecom: '企业微信机器人' })[value] }
function settingLabel(value: string) { return ({ host: '主机', port: '端口', tls: 'TLS', from: '发件人', to: '收件人', username: '用户名', password: '密码', webhook: 'Webhook', secret: '签名密钥', client_id: '客户端 ID', tenant: '租户', access_token: '访问令牌', refresh_token: '刷新令牌' } as Record<string, string>)[value] || value }
function loginLabel(value: string) { return ({ waiting: '等待扫码', scanned: '已扫码，请确认', success: '登录成功', expired: '二维码已过期' } as Record<string, string>)[value] || value }

function deliveryTitle(delivery: Delivery) {
  if (delivery.kind === 'comment' && delivery.comment) {
    return `${delivery.comment.up_name || delivery.comment.up_uid} · 评论回复`
  }
  return delivery.dynamic?.up_name || delivery.dynamic?.uid || delivery.id
}

function deliverySummary(delivery: Delivery) {
  if (delivery.kind === 'comment' && delivery.comment) {
    return delivery.comment.content_title || delivery.comment.content_url || `评论 ${delivery.comment.rpid}`
  }
  return delivery.dynamic?.summary || ''
}

let displayTimeZone = ''
function usableTimeZone(value?: string) {
  if (!value || value === 'Local' || value.startsWith('UTC')) return ''
  try {
    new Intl.DateTimeFormat('zh-CN', { timeZone: value }).format(new Date())
    return value
  } catch {
    return ''
  }
}
function formatDate(value: string) {
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return '—'
  return new Intl.DateTimeFormat('zh-CN', {
    dateStyle: 'short',
    timeStyle: 'medium',
    ...(displayTimeZone ? { timeZone: displayTimeZone } : {}),
  }).format(date)
}

// datetime-local values are wall-clock local; convert to RFC3339 for the API.
function localInputToRFC3339(value: string) {
  if (!value) return ''
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return value
  return date.toISOString()
}

function dynamicTypeLabel(value: string) {
  return ({
    DYNAMIC_TYPE_WORD: '文字',
    DYNAMIC_TYPE_DRAW: '图文',
    DYNAMIC_TYPE_AV: '视频',
    DYNAMIC_TYPE_ARTICLE: '专栏',
    DYNAMIC_TYPE_FORWARD: '转发',
    DYNAMIC_TYPE_PGC: '番剧',
    DYNAMIC_TYPE_COMMON_SQUARE: '通用卡片',
  } as Record<string, string>)[value] || value || '内容'
}

function errorMessage(error: unknown) { return error instanceof Error ? error.message : '发生未知错误' }
