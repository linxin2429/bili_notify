import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Navigate, Route, Routes, useLocation, useNavigate, useSearchParams } from 'react-router-dom'
import Add from '@mui/icons-material/Add'
import Autorenew from '@mui/icons-material/Autorenew'
import BrightnessAuto from '@mui/icons-material/BrightnessAuto'
import BrokenImage from '@mui/icons-material/BrokenImage'
import ChatBubbleOutline from '@mui/icons-material/ChatBubbleOutline'
import CheckCircle from '@mui/icons-material/CheckCircle'
import ChevronLeft from '@mui/icons-material/ChevronLeft'
import ChevronRight from '@mui/icons-material/ChevronRight'
import Close from '@mui/icons-material/Close'
import DarkMode from '@mui/icons-material/DarkMode'
import Dashboard from '@mui/icons-material/Dashboard'
import Delete from '@mui/icons-material/Delete'
import Edit from '@mui/icons-material/Edit'
import Email from '@mui/icons-material/Email'
import ErrorOutlined from '@mui/icons-material/ErrorOutlined'
import FavoriteBorder from '@mui/icons-material/FavoriteBorder'
import History from '@mui/icons-material/History'
import Hub from '@mui/icons-material/Hub'
import LightMode from '@mui/icons-material/LightMode'
import LiveTv from '@mui/icons-material/LiveTv'
import Logout from '@mui/icons-material/Logout'
import MenuIcon from '@mui/icons-material/Menu'
import NotificationsActive from '@mui/icons-material/NotificationsActive'
import OpenInNew from '@mui/icons-material/OpenInNew'
import Password from '@mui/icons-material/Password'
import People from '@mui/icons-material/People'
import PlayArrow from '@mui/icons-material/PlayArrow'
import QrCode2 from '@mui/icons-material/QrCode2'
import Refresh from '@mui/icons-material/Refresh'
import ReceiptLong from '@mui/icons-material/ReceiptLong'
import Repeat from '@mui/icons-material/Repeat'
import Science from '@mui/icons-material/Science'
import Settings from '@mui/icons-material/Settings'
import WarningAmber from '@mui/icons-material/WarningAmber'
import Visibility from '@mui/icons-material/Visibility'
import {
  Alert, AppBar, Avatar, BottomNavigation, BottomNavigationAction, Box, Button, Card, CardContent,
  Chip, CircularProgress, Container, CssBaseline, Dialog, DialogActions, DialogContent, DialogTitle,
  Divider, Drawer, FormControl, FormControlLabel, IconButton, InputLabel, List, ListItemButton,
  ListItemIcon, ListItemText, MenuItem, Paper, Select, Snackbar, Stack, Switch, Tab, Tabs,
  TextField, ThemeProvider, Toolbar, Tooltip, Typography, createTheme, useMediaQuery,
} from '@mui/material'
import type {
  AuditLog, BiliLogin, Channel, ChannelDraft, ChannelType, CommentDetail, CommentHistoryItem, ConnectionState,
  DashboardSnapshot, Delivery, DynamicHistoryItem, DynamicMedia, DynamicPreview, MicrosoftLogin,
  RuntimeSettings, ThemePreference, UP,
} from './types'
import { RealtimeClient } from './realtime'
import { AdminAPI, httpJSON } from './api'
import {
  applyBiliLoginMutation, applyChannelDeletion, applyChannelMutation, applyMicrosoftLoginDeletion,
  applyMicrosoftLoginMutation, applySettingsMutation, applyUpdate, applyUPDeletion, applyUPMutation, readinessMessage,
} from './dashboard'

const drawerWidth = 236
const navigation = [
  { path: '/overview', label: '概览', icon: <Dashboard /> },
  { path: '/ups', label: 'UP 主', icon: <People /> },
  { path: '/channels', label: '通知渠道', icon: <NotificationsActive /> },
  { path: '/deliveries', label: '投递队列', icon: <Hub /> },
  { path: '/history', label: '历史', icon: <History /> },
  { path: '/audit-logs', label: '操作日志', icon: <ReceiptLong /> },
  { path: '/settings', label: '设置', icon: <Settings /> },
]
const pageSize = 20

interface SessionState { setup_required: boolean; authenticated: boolean; csrf_token?: string }
type SnapshotMutation<T> = (snapshot: DashboardSnapshot, value: T) => DashboardSnapshot
type RunMutation = <T>(request: () => Promise<T>, update?: SnapshotMutation<T>) => Promise<T>

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
  const [pageRefresh, setPageRefresh] = useState(0)
  const snapshotVersion = useRef(0)
  const refreshRequest = useRef(0)
  const api = useMemo(() => new AdminAPI(csrf), [csrf])
  const mobile = useMediaQuery(theme => theme.breakpoints.down('md'))
  const location = useLocation()
  const navigate = useNavigate()

  useEffect(() => {
    const client = new RealtimeClient({
      onSnapshot: value => {
        snapshotVersion.current += 1
        displayTimeZone = usableTimeZone(value.timezone)
        setSnapshot(value)
      },
      onEvent: (event, data) => {
        snapshotVersion.current += 1
        setSnapshot(current => applyUpdate(current, event, data))
      },
      onState: setConnection,
      onAuthLost,
      onError: setMessage,
    })
    client.start()
    return () => client.stop()
  }, [onAuthLost])
  const runMutation = useCallback<RunMutation>(async (request, update) => {
    try {
      const value = await request()
      if (update) setSnapshot(current => current ? update(current, value) : current)
      return value
    } catch (error) {
      setMessage(errorMessage(error))
      throw error
    }
  }, [])
  const refreshDashboard = useCallback(async () => {
    const request = ++refreshRequest.current
    const version = snapshotVersion.current
    try {
      const value = await api.dashboard()
      if (!canApplyDashboardRefresh(request, refreshRequest.current, version, snapshotVersion.current)) return
      snapshotVersion.current += 1
      displayTimeZone = usableTimeZone(value.timezone)
      setSnapshot(value)
    } catch (error) {
      if (request === refreshRequest.current) setMessage(errorMessage(error))
    }
  }, [api])
  const logout = async () => {
    try { await httpJSON('/api/v1/session', { method: 'DELETE' }, csrf) } finally { await onAuthLost() }
  }
  const activePath = navigation.find(item => location.pathname.startsWith(item.path))?.path || '/overview'
  const navigateTo = (path: string) => {
    activateNavigation(activePath, path, navigate, () => {
      setPageRefresh(current => current + 1)
      void refreshDashboard()
    })
    setMobileOpen(false)
  }
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
            <Route path="/overview" element={<Overview snapshot={snapshot} api={api} runMutation={runMutation} />} />
            <Route path="/ups" element={<UPsPage ups={snapshot.ups} api={api} runMutation={runMutation} />} />
            <Route path="/channels" element={<ChannelsPage channels={snapshot.channels} logins={snapshot.microsoft_logins} api={api} runMutation={runMutation} />} />
            <Route path="/deliveries" element={<DeliveriesPage deliveries={snapshot.deliveries} channels={snapshot.channels} total={snapshot.status.outbox_depth} api={api} runMutation={runMutation} refreshDashboard={refreshDashboard} />} />
            <Route path="/history" element={<HistoryPage ups={snapshot.ups} api={api} refresh={pageRefresh} />} />
            <Route path="/audit-logs" element={<AuditLogsPage api={api} refresh={pageRefresh} />} />
            <Route path="/settings" element={<SettingsPage csrf={csrf} preference={themePreference} setPreference={setThemePreference} settings={snapshot.settings} api={api} runMutation={runMutation} onChanged={onAuthLost} />} />
            <Route path="*" element={<Navigate to="/overview" replace />} />
          </Routes>}
      </Container>
    </Box>
    {mobile && <Paper elevation={6} sx={{ position: 'fixed', left: 0, right: 0, bottom: 0, zIndex: theme => theme.zIndex.appBar }}><BottomNavigation value={activePath} onChange={(_, value) => navigateTo(value)} showLabels>{navigation.slice(0, 5).map(item => <BottomNavigationAction key={item.path} value={item.path} label={item.label} icon={item.icon} />)}</BottomNavigation></Paper>}
    <Snackbar open={Boolean(message)} autoHideDuration={6000} onClose={() => setMessage('')} message={message} />
  </Box>
}

export function activateNavigation(activePath: string, targetPath: string, navigate: (path: string) => void, refresh: () => void) {
  if (activePath !== targetPath) navigate(targetPath)
  refresh()
}

export function canApplyDashboardRefresh(request: number, latestRequest: number, version: number, latestVersion: number) {
  return request === latestRequest && version === latestVersion
}

function Overview({ snapshot, api, runMutation }: { snapshot: DashboardSnapshot; api: AdminAPI; runMutation: RunMutation }) {
  const status = snapshot.status
  const [busy, setBusy] = useState(false)
  const startLogin = async () => {
    setBusy(true)
    try { await runMutation(() => api.startBiliLogin(), applyBiliLoginMutation) }
    catch { /* The shared mutation handler reports the failure. */ }
    finally { setBusy(false) }
  }
  const cancelLogin = async (id: string) => {
    try { await runMutation(() => api.cancelBiliLogin(id), current => applyBiliLoginMutation(current, null)) }
    catch { /* The shared mutation handler reports the failure. */ }
  }
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
      <Card><CardContent><Stack spacing={2}><Stack direction="row" justifyContent="space-between" alignItems="center"><Box><Typography variant="h6" fontWeight={800}>B站账号</Typography><Typography color="text.secondary">{status.bili_account ? `${status.bili_account.name || '已登录账号'} · UID ${status.bili_account.uid}` : '使用哔哩哔哩 App 扫码建立网页会话'}</Typography></Box><QrCode2 color="primary" /></Stack><BiliLoginPanel login={snapshot.bili_login || null} busy={busy} start={() => void startLogin()} cancel={id => void cancelLogin(id)} /></Stack></CardContent></Card>
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

function UPsPage({ ups, api, runMutation }: { ups: UP[]; api: AdminAPI; runMutation: RunMutation }) {
  const [editing, setEditing] = useState<UP | null | undefined>(undefined)
  const mobile = useMediaQuery(theme => theme.breakpoints.down('sm'))
  const save = async (value: { uid: string; name: string; enabled: boolean }) => {
    await runMutation(() => editing ? api.updateUP(value) : api.createUP(value), applyUPMutation)
    setEditing(undefined)
  }
  const remove = async (uid: string) => {
    if (!confirm('删除该 UP 主及其去重状态？')) return
    try { await runMutation(() => api.deleteUP(uid), current => applyUPDeletion(current, uid)) }
    catch { /* The shared mutation handler reports the failure. */ }
  }
  return <Stack spacing={3}><PageHeader title="UP 主" subtitle="管理需要轮询的公开账号；首次采集只建立基线。" action={<Button variant="contained" startIcon={<Add />} onClick={() => setEditing(null)}>添加 UP 主</Button>} />
    {ups.length === 0 ? <EmptyState icon={<People />} title="尚未添加 UP 主" action={<Button variant="contained" onClick={() => setEditing(null)}>添加第一个 UP 主</Button>} /> :
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', lg: 'repeat(2, 1fr)' }, gap: 2 }}>{ups.map(up => <Card key={up.uid}><CardContent><Stack spacing={2}><Stack direction="row" justifyContent="space-between" alignItems="start"><Box><Typography variant="h6" fontWeight={800}>{up.name || `UID ${up.uid}`}</Typography><Typography color="text.secondary">UID {up.uid}</Typography></Box><Chip label={up.enabled ? '已启用' : '已停用'} color={up.enabled ? 'success' : 'default'} /></Stack><Stack direction="row" spacing={1} flexWrap="wrap"><Chip size="small" label={up.baseline_ready ? '基线已建立' : '等待基线'} /><Chip size="small" label={followStateLabel(up.follow_state)} color={up.follow_state === 'followed' ? 'success' : up.follow_state === 'unknown' ? 'warning' : 'default'} /><Chip size="small" label={up.collection_route === 'feed_all' ? '综合流采集' : '空间采集'} color={up.collection_route === 'feed_all' ? 'info' : 'default'} /><Chip size="small" label={`连续失败 ${up.consecutive_fail} 次`} color={up.consecutive_fail ? 'warning' : 'default'} /></Stack>{up.last_error && <Alert severity="error">{up.last_error}</Alert>}<Typography variant="body2" color="text.secondary">关注关系检查：{up.follow_checked_at ? formatDate(up.follow_checked_at) : '尚未检查'}</Typography><Typography variant="body2" color="text.secondary">最后成功：{up.last_success_at ? formatDate(up.last_success_at) : '尚无记录'}</Typography><Stack direction="row" spacing={1}><Button startIcon={<Edit />} onClick={() => setEditing(up)}>编辑</Button><Button color="error" startIcon={<Delete />} onClick={() => void remove(up.uid)}>删除</Button></Stack></Stack></CardContent></Card>)}</Box>}
    <UPDialog open={editing !== undefined} value={editing || undefined} fullScreen={mobile} onClose={() => setEditing(undefined)} onSave={save} />
  </Stack>
}

export function followStateLabel(state: UP['follow_state']) {
  if (state === 'followed') return '当前账号已关注'
  if (state === 'unfollowed') return '当前账号未关注'
  return '关注关系未知'
}

function UPDialog({ open, value, fullScreen, onClose, onSave }: { open: boolean; value?: UP; fullScreen: boolean; onClose: () => void; onSave: (value: { uid: string; name: string; enabled: boolean }) => Promise<void> }) {
  const [uid, setUID] = useState(''); const [name, setName] = useState(''); const [enabled, setEnabled] = useState(true); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  useEffect(() => { setUID(value?.uid || ''); setName(value?.name || ''); setEnabled(value?.enabled ?? true); setError('') }, [value, open])
  const submit = async () => {
    setBusy(true); setError('')
    try { await onSave({ uid, name, enabled }) }
    catch (err) { setError(errorMessage(err)) }
    finally { setBusy(false) }
  }
  return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm"><DialogTitle>{value ? '编辑 UP 主' : '添加 UP 主'}</DialogTitle><DialogContent><Stack spacing={2} sx={{ pt: 1 }}>{error && <Alert severity="error">{error}</Alert>}<TextField label="UID" value={uid} onChange={e => setUID(e.target.value)} disabled={Boolean(value)} inputMode="numeric" required /><TextField label="备注名" value={name} onChange={e => setName(e.target.value)} /><FormControlLabel control={<Switch checked={enabled} onChange={e => setEnabled(e.target.checked)} />} label="启用轮询" /></Stack></DialogContent><DialogActions><Button onClick={onClose}>取消</Button><Button variant="contained" disabled={busy || !uid} onClick={() => void submit()}>保存</Button></DialogActions></Dialog>
}

function ChannelsPage({ channels, logins, api, runMutation }: { channels: Channel[]; logins: MicrosoftLogin[]; api: AdminAPI; runMutation: RunMutation }) {
  const [editing, setEditing] = useState<Channel | null | undefined>(undefined)
  const mobile = useMediaQuery(theme => theme.breakpoints.down('sm'))
  const save = async (draft: ChannelDraft) => {
    await runMutation(() => draft.id ? api.updateChannel(draft as ChannelDraft & { id: string }) : api.createChannel(draft), applyChannelMutation)
    setEditing(undefined)
  }
  const remove = async (id: string) => {
    if (!confirm('存在待投递任务时不能删除渠道。继续？')) return
    try { await runMutation(() => api.deleteChannel(id), current => applyChannelDeletion(current, id)) }
    catch { /* The shared mutation handler reports the failure. */ }
  }
  const authorize = async (channelID: string) => {
    try {
      const login = await runMutation(() => api.startMicrosoftLogin(channelID), applyMicrosoftLoginMutation)
      const url = login.verification_uri_complete || login.verification_uri
      if (url) window.open(url, '_blank', 'noopener,noreferrer')
    } catch { /* The shared mutation handler reports the failure. */ }
  }
  const cancelAuthorization = async (channelID: string) => {
    try { await runMutation(() => api.cancelMicrosoftLogin(channelID), current => applyMicrosoftLoginDeletion(current, channelID)) }
    catch { /* The shared mutation handler reports the failure. */ }
  }
  const test = async (channelID: string) => {
    try { await runMutation(() => api.testChannel(channelID)) }
    catch { /* The shared mutation handler reports the failure. */ }
  }
  return <Stack spacing={3}><PageHeader title="通知渠道" subtitle="秘密字段仅写入，不会返回浏览器。" action={<Button variant="contained" startIcon={<Add />} onClick={() => setEditing(null)}>添加渠道</Button>} />
    {channels.length === 0 ? <EmptyState icon={<NotificationsActive />} title="尚未配置通知渠道" action={<Button variant="contained" onClick={() => setEditing(null)}>添加第一个渠道</Button>} /> :
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', xl: 'repeat(2, 1fr)' }, gap: 2 }}>{channels.map(channel => { const login = logins.find(item => item.channel_id === channel.id); return <Card key={channel.id}><CardContent><Stack spacing={2}><Stack direction="row" justifyContent="space-between"><Stack direction="row" spacing={1.5} alignItems="center"><Avatar sx={{ bgcolor: 'secondary.main' }}><Email /></Avatar><Box><Typography variant="h6" fontWeight={800}>{channel.name}</Typography><Typography color="text.secondary">{channelTypeLabel(channel.type)}</Typography></Box></Stack><Chip label={channel.enabled ? '已启用' : '已停用'} color={channel.enabled ? 'success' : 'default'} /></Stack><Divider /><ChannelSummary channel={channel} />{channel.type === 'microsoft' && <MicrosoftAuthorization channel={channel} login={login} authorize={() => void authorize(channel.id)} cancel={() => void cancelAuthorization(channel.id)} />}<Stack direction="row" spacing={1} flexWrap="wrap"><Button startIcon={<Edit />} onClick={() => setEditing(channel)}>编辑</Button><Button startIcon={<Science />} onClick={() => void test(channel.id)}>发送测试</Button><Button color="error" startIcon={<Delete />} onClick={() => void remove(channel.id)}>删除</Button></Stack></Stack></CardContent></Card> })}</Box>}
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
  const [fields, setFields] = useState<Record<string, string>>({}); const [secrets, setSecrets] = useState<Record<string, string>>({}); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  useEffect(() => { setName(channel?.name || ''); setType(channel?.type || 'email'); setEnabled(channel?.enabled ?? true); setFields(channel?.settings || {}); setSecrets({}); setError('') }, [channel, open])
  useEffect(() => { if (!channel && type === 'microsoft') setEnabled(false) }, [type, channel])
  const setField = (key: string, value: string) => setFields(current => ({ ...current, [key]: value }))
  const setSecret = (key: string, value: string) => setSecrets(current => ({ ...current, [key]: value }))
  const submit = async () => {
    const settings = channelFields(type).filter(field => !field.secret).reduce<Record<string, string>>((result, field) => ({ ...result, [field.key]: fields[field.key] || field.defaultValue || '' }), {})
    const changedSecrets = Object.fromEntries(Object.entries(secrets).filter(([, value]) => value !== ''))
    setBusy(true); setError('')
    try { await onSave({ id: channel?.id, name, type, enabled, settings, ...(Object.keys(changedSecrets).length ? { secrets: changedSecrets } : {}) }) }
    catch (err) { setError(errorMessage(err)) }
    finally { setBusy(false) }
  }
  return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm"><DialogTitle>{channel ? '编辑通知渠道' : '添加通知渠道'}</DialogTitle><DialogContent><Stack spacing={2} sx={{ pt: 1 }}>{error && <Alert severity="error">{error}</Alert>}<TextField label="渠道名称" value={name} onChange={e => setName(e.target.value)} required /><FormControl><InputLabel id="channel-type-label">渠道类型</InputLabel><Select labelId="channel-type-label" label="渠道类型" value={type} onChange={e => { setType(e.target.value as ChannelType); setFields({}); setSecrets({}) }}>{(['email', 'microsoft', 'dingtalk', 'feishu', 'wecom'] as ChannelType[]).map(value => <MenuItem key={value} value={value}>{channelTypeLabel(value)}</MenuItem>)}</Select></FormControl>{channelFields(type).map(field => <TextField key={field.key} label={field.label} type={field.secret ? 'password' : 'text'} value={field.secret ? secrets[field.key] || '' : fields[field.key] || field.defaultValue || ''} onChange={e => field.secret ? setSecret(field.key, e.target.value) : setField(field.key, e.target.value)} required={field.required && !(channel?.configured_secrets.includes(field.key))} helperText={field.secret && channel?.configured_secrets.includes(field.key) ? '已安全保存；留空表示保留原值' : field.help} />)}<FormControlLabel control={<Switch checked={enabled} onChange={e => setEnabled(e.target.checked)} />} label="启用渠道" />{type === 'microsoft' && <Alert severity="info">保存后需要完成 Microsoft 设备码授权，再启用渠道。</Alert>}</Stack></DialogContent><DialogActions><Button onClick={onClose}>取消</Button><Button variant="contained" disabled={busy || !name} onClick={() => void submit()}>保存</Button></DialogActions></Dialog>
}

export function DeliveriesPage({ deliveries, channels, total, api, runMutation, refreshDashboard }: { deliveries: Delivery[]; channels: Channel[]; total: number; api: AdminAPI; runMutation: RunMutation; refreshDashboard: () => Promise<void> }) {
  const [params, setParams] = useSearchParams()
  const [retrying, setRetrying] = useState<Set<string>>(() => new Set())
  const requested = params.get('state')
  const filter = requested === 'pending' || requested === 'blocked' ? requested : 'all'
  const setFilter = (value: string) => setParams(value === 'all' ? {} : { state: value })
  const visible = deliveries.filter(delivery => filter === 'all' || delivery.state === filter)
  const retry = async (id: string) => {
    setRetrying(current => new Set(current).add(id))
    try {
      await runMutation(() => api.retryDelivery(id))
      await refreshDashboard()
    } catch { /* The shared mutation handler reports the failure. */ }
    finally {
      setRetrying(current => {
        const next = new Set(current)
        next.delete(id)
        return next
      })
    }
  }
  return <Stack spacing={3}><PageHeader title="投递队列" subtitle={`共 ${total} 个任务，页面展示前 ${deliveries.length} 个。`} /><Paper><Tabs value={filter} onChange={(_, value) => setFilter(value)} variant="scrollable"><Tab value="all" label="全部" /><Tab value="pending" label="等待重试" /><Tab value="blocked" label="已阻塞" /></Tabs></Paper>{visible.length === 0 ? <EmptyState icon={<CheckCircle />} title="当前筛选下没有待投递任务" /> : <Stack spacing={1.5}>{visible.map(delivery => <Card key={delivery.id}><CardContent><Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" gap={2}><Box minWidth={0}><Stack direction="row" spacing={1} alignItems="center"><Chip size="small" color={delivery.state === 'blocked' ? 'error' : 'warning'} label={delivery.state === 'blocked' ? '已阻塞' : '等待重试'} /><Typography fontWeight={750}>{deliveryTitle(delivery)}</Typography></Stack><Typography className="summary-clamp" sx={{ mt: 1 }}>{deliverySummary(delivery)}</Typography><Typography variant="body2" color="text.secondary" sx={{ mt: 1 }}>渠道：{channels.find(channel => channel.id === delivery.channel_id)?.name || delivery.channel_id} · 已尝试 {delivery.attempts} 次</Typography>{delivery.last_error && <Typography variant="body2" color="error" sx={{ mt: .5 }}>上次错误：{delivery.last_error}</Typography>}</Box><Stack flexShrink={0} alignItems={{ xs: 'stretch', sm: 'flex-start' }} spacing={1}><Box><Typography variant="body2" color="text.secondary">下次处理</Typography><Typography>{formatDate(delivery.next_at)}</Typography></Box>{delivery.state === 'blocked' && <Button variant="outlined" startIcon={retrying.has(delivery.id) ? <CircularProgress size={18} /> : <Refresh />} disabled={retrying.has(delivery.id)} onClick={() => void retry(delivery.id)}>{retrying.has(delivery.id) ? '正在提交' : '立即重试'}</Button>}</Stack></Stack></CardContent></Card>)}</Stack>}</Stack>
}

function HistoryPage({ ups, api, refresh }: { ups: UP[]; api: AdminAPI; refresh: number }) {
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
  const [commentDetail, setCommentDetail] = useState<CommentDetail | null>(null)
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
          : await api.queryDynamics(payload)
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
  }, [tab, uid, q, from, to, offset, api, refresh])

  const openCommentDetail = async (id: string) => {
    try {
      setCommentDetail(await api.getComment(id))
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
          ? <CommentHistoryCard key={(item as CommentHistoryItem).rpid} item={item as CommentHistoryItem} onOpen={() => void openCommentDetail((item as CommentHistoryItem).rpid)} />
          : <DynamicHistoryCard key={(item as DynamicHistoryItem).id} item={item as DynamicHistoryItem} />)}</Stack>}
    {total > 0 && <Stack direction="row" justifyContent="space-between" alignItems="center">
      <Typography variant="body2" color="text.secondary">共 {total} 条，当前 {offset + 1}-{pageEnd}</Typography>
      <Stack direction="row" spacing={1}>
        <Button startIcon={<ChevronLeft />} disabled={offset <= 0 || busy} onClick={() => updateParams({ offset: String(Math.max(0, offset - pageSize)) })}>上一页</Button>
        <Button endIcon={<ChevronRight />} disabled={offset + pageSize >= total || busy} onClick={() => updateParams({ offset: String(offset + pageSize) })}>下一页</Button>
      </Stack>
    </Stack>}
    <CommentHistoryDialog open={Boolean(commentDetail)} detail={commentDetail} fullScreen={mobile} onClose={() => setCommentDetail(null)} />
  </Stack>
}

const auditActions = [
  'auth.setup', 'auth.login', 'auth.logout', 'auth.password.change',
  'up.create', 'up.update', 'up.delete',
  'channel.create', 'channel.update', 'channel.delete', 'channel.test',
  'delivery.retry', 'bilibili.login.start', 'bilibili.login.cancel',
  'microsoft.login.start', 'microsoft.login.cancel', 'settings.update',
]

export function AuditLogsPage({ api, refresh }: { api: AdminAPI; refresh: number }) {
  const [params, setParams] = useSearchParams()
  const action = params.get('action') || ''
  const outcome = params.get('outcome') || ''
  const q = params.get('q') || ''
  const from = params.get('from') || ''
  const to = params.get('to') || ''
  const offset = Math.max(0, Number(params.get('offset') || '0') || 0)
  const [draftQ, setDraftQ] = useState(q)
  const [items, setItems] = useState<AuditLog[]>([])
  const [total, setTotal] = useState(0)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

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
        const page = await api.queryAuditLogs({
          ...(action ? { action } : {}),
          ...(outcome ? { outcome } : {}),
          ...(q ? { q } : {}),
          ...(from ? { from: localInputToRFC3339(from) } : {}),
          ...(to ? { to: localInputToRFC3339(to) } : {}),
          limit: pageSize,
          offset,
        })
        if (!cancelled) {
          setItems(page.items || [])
          setTotal(page.total || 0)
        }
      } catch (err) {
        if (!cancelled) {
          setItems([]); setTotal(0); setError(errorMessage(err))
        }
      } finally {
        if (!cancelled) setBusy(false)
      }
    }
    void run()
    return () => { cancelled = true }
  }, [action, outcome, q, from, to, offset, api, refresh])

  const pageEnd = Math.min(offset + pageSize, total)
  return <Stack spacing={3}>
    <PageHeader title="操作日志" subtitle="查询管理员认证、配置变更和手动运维操作；日志不会保存密码、令牌或 Webhook 内容。" />
    <Paper sx={{ p: 2 }}><Stack direction={{ xs: 'column', md: 'row' }} spacing={1.5}>
      <FormControl sx={{ minWidth: 190 }}><InputLabel id="audit-action-label">操作</InputLabel>
        <Select labelId="audit-action-label" label="操作" value={action} onChange={e => updateParams({ action: e.target.value || undefined })}>
          <MenuItem value="">全部操作</MenuItem>
          {auditActions.map(value => <MenuItem key={value} value={value}>{auditActionLabel(value)}</MenuItem>)}
        </Select>
      </FormControl>
      <FormControl sx={{ minWidth: 140 }}><InputLabel id="audit-outcome-label">结果</InputLabel>
        <Select labelId="audit-outcome-label" label="结果" value={outcome} onChange={e => updateParams({ outcome: e.target.value || undefined })}>
          <MenuItem value="">全部结果</MenuItem><MenuItem value="success">成功</MenuItem><MenuItem value="failure">失败</MenuItem><MenuItem value="denied">已拒绝</MenuItem>
        </Select>
      </FormControl>
      <TextField label="资源、IP 或请求 ID" value={draftQ} onChange={e => setDraftQ(e.target.value)} fullWidth />
      <TextField label="开始时间" type="datetime-local" value={from} onChange={e => updateParams({ from: e.target.value || undefined })} InputLabelProps={{ shrink: true }} sx={{ minWidth: 210 }} />
      <TextField label="结束时间" type="datetime-local" value={to} onChange={e => updateParams({ to: e.target.value || undefined })} InputLabelProps={{ shrink: true }} sx={{ minWidth: 210 }} />
    </Stack></Paper>
    {error && <Alert severity="error">{error}</Alert>}
    {busy && items.length === 0 ? <Box display="grid" sx={{ placeItems: 'center', py: 8 }}><CircularProgress /></Box>
      : items.length === 0 ? <EmptyState icon={<ReceiptLong />} title="当前筛选下没有操作日志" />
        : <Stack spacing={1.5}>{items.map(item => <AuditLogCard key={item.id} item={item} />)}</Stack>}
    {total > 0 && <Stack direction="row" justifyContent="space-between" alignItems="center">
      <Typography variant="body2" color="text.secondary">共 {total} 条，当前 {offset + 1}-{pageEnd}</Typography>
      <Stack direction="row" spacing={1}>
        <Button startIcon={<ChevronLeft />} disabled={offset <= 0 || busy} onClick={() => updateParams({ offset: String(Math.max(0, offset - pageSize)) })}>上一页</Button>
        <Button endIcon={<ChevronRight />} disabled={offset + pageSize >= total || busy} onClick={() => updateParams({ offset: String(offset + pageSize) })}>下一页</Button>
      </Stack>
    </Stack>}
  </Stack>
}

function AuditLogCard({ item }: { item: AuditLog }) {
  const [expanded, setExpanded] = useState(false)
  const color = item.outcome === 'success' ? 'success' : item.outcome === 'denied' ? 'warning' : 'error'
  const result = item.outcome === 'success' ? '成功' : item.outcome === 'denied' ? '已拒绝' : '失败'
  const target = item.resource_id ? `${item.resource_type} · ${item.resource_id}` : item.resource_type
  return <Card><CardContent><Stack spacing={1.25}>
    <Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" gap={1.5}>
      <Box><Stack direction="row" spacing={1} alignItems="center" flexWrap="wrap"><Chip size="small" color={color} label={result} /><Typography fontWeight={750}>{auditActionLabel(item.action)}</Typography>{target && <Typography variant="body2" color="text.secondary">{target}</Typography>}</Stack>
        <Typography variant="body2" color="text.secondary" sx={{ mt: .75 }}>{formatDate(item.occurred_at)} · {item.remote_ip || '未知来源'} · HTTP {item.status_code} · {item.duration_ms} ms</Typography>
      </Box>
      <Button size="small" onClick={() => setExpanded(value => !value)} aria-expanded={expanded}>{expanded ? '收起详情' : '查看详情'}</Button>
    </Stack>
    {expanded && <Box component="dl" sx={{ m: 0, display: 'grid', gridTemplateColumns: { xs: '1fr', sm: '130px 1fr' }, gap: .75, overflowWrap: 'anywhere' }}>
      <Typography component="dt" color="text.secondary">请求 ID</Typography><Typography component="dd" sx={{ m: 0, fontFamily: 'monospace' }}>{item.request_id}</Typography>
      <Typography component="dt" color="text.secondary">会话</Typography><Typography component="dd" sx={{ m: 0, fontFamily: 'monospace' }}>{item.session_id || '未认证'}</Typography>
      <Typography component="dt" color="text.secondary">操作者</Typography><Typography component="dd" sx={{ m: 0 }}>{item.actor === 'administrator' ? '管理员' : '匿名来源'}</Typography>
      <Typography component="dt" color="text.secondary">User-Agent</Typography><Typography component="dd" sx={{ m: 0 }}>{item.user_agent || '未提供'}</Typography>
      <Typography component="dt" color="text.secondary">路由</Typography><Typography component="dd" sx={{ m: 0, fontFamily: 'monospace' }}>{item.http_method} {item.route}</Typography>
      {item.error_code && <><Typography component="dt" color="text.secondary">错误码</Typography><Typography component="dd" sx={{ m: 0 }}>{item.error_code}</Typography></>}
      <Typography component="dt" color="text.secondary">安全变更摘要</Typography><Box component="dd" sx={{ m: 0 }}><Box component="pre" sx={{ m: 0, p: 1.5, bgcolor: 'action.hover', borderRadius: 1, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{JSON.stringify(item.details || {}, null, 2)}</Box></Box>
    </Box>}
  </Stack></CardContent></Card>
}

function auditActionLabel(action: string) {
  const labels: Record<string, string> = {
    'auth.setup': '初始化管理员', 'auth.login': '管理员登录', 'auth.logout': '管理员退出', 'auth.password.change': '修改管理员密码',
    'up.create': '添加 UP 主', 'up.update': '修改 UP 主', 'up.delete': '删除 UP 主',
    'channel.create': '添加通知渠道', 'channel.update': '修改通知渠道', 'channel.delete': '删除通知渠道', 'channel.test': '测试通知渠道',
    'delivery.retry': '重试投递', 'bilibili.login.start': '开始 B 站登录', 'bilibili.login.cancel': '取消 B 站登录',
    'microsoft.login.start': '开始 Microsoft 授权', 'microsoft.login.cancel': '取消 Microsoft 授权', 'settings.update': '修改采集参数',
  }
  return labels[action] || action
}

type HistoryContent = DynamicHistoryItem | DynamicPreview

export function DynamicHistoryCard({ item }: { item: DynamicHistoryItem }) {
  const [expanded, setExpanded] = useState(false)
  const [clamped, setClamped] = useState(false)
  const bodyRef = useRef<HTMLElement | null>(null)
  const contentCard = isContentCardType(item.type)
  const body = contentCard ? (item.summary || '').trim() : composePreviewBody(item.summary, item.description)
  const title = contentCard ? '' : (item.title || '').trim()
  const targetURL = (item.target_url || item.url || '').trim()
  useLayoutEffect(() => {
    if (expanded) {
      setClamped(false)
      return
    }
    const node = bodyRef.current
    if (!node) {
      setClamped(false)
      return
    }
    setClamped(node.scrollHeight > node.clientHeight + 1)
  }, [body, expanded])
  return <Card className="history-card"><CardContent className="history-card-content">
    <Stack direction="row" spacing={1.5} alignItems="flex-start">
      <Avatar className="history-author-avatar" aria-hidden="true">{historyAvatarText(item.up_name || item.uid)}</Avatar>
      <Box minWidth={0} flex={1}>
        <Stack direction="row" justifyContent="space-between" alignItems="flex-start" gap={1}>
          <Box minWidth={0}>
            <Typography className="history-author-name" fontWeight={750}>{item.up_name || item.uid}</Typography>
            <Typography variant="body2" color="text.secondary" title={formatDate(item.published_at)}>
              {formatRelativeDate(item.published_at)} · {dynamicTypeLabel(item.type)}
            </Typography>
          </Box>
          <Stack direction="row" spacing={.75} flexWrap="wrap" justifyContent="flex-end">
            {item.badge && <Chip size="small" label={item.badge} />}
            {item.baseline && <Chip size="small" label="基线" variant="outlined" />}
          </Stack>
        </Stack>
        <Box className="history-copyable">
          {title && <Typography className="history-post-title" fontWeight={750} sx={{ mt: 1.75 }}>{title}</Typography>}
          {body && <>
            <Typography ref={bodyRef} className={expanded ? undefined : 'history-text-clamp'} sx={{ mt: title ? .75 : 1.75, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{body}</Typography>
            {(expanded || clamped) && <Button size="small" sx={{ mt: .5, px: 0 }} onClick={() => setExpanded(value => !value)}>{expanded ? '收起' : '展开全文'}</Button>}
          </>}
          <DynamicContentPreview item={item} />
          {item.original && <OriginalDynamicPreview item={item.original} />}
          {!title && !body && !item.media?.length && !item.original && <Typography color="text.secondary" sx={{ mt: 1.5 }}>（该归档没有可预览的正文或媒体）</Typography>}
        </Box>
      </Box>
    </Stack>
    <Divider sx={{ mt: 2 }} />
    <Stack className="history-actions" direction="row" justifyContent="space-between" alignItems="center" gap={1}>
      <Stack direction="row" alignItems="center" spacing={{ xs: 1.25, sm: 2.5 }} minWidth={0}>
        {item.stats && <>
          <HistoryStat icon={<Repeat />} value={item.stats.forwards} emptyLabel="转发" label="转发" />
          <HistoryStat icon={<ChatBubbleOutline />} value={item.stats.comments} emptyLabel="评论" label="评论" />
          <HistoryStat icon={<FavoriteBorder />} value={item.stats.likes} emptyLabel="点赞" label="点赞" />
        </>}
      </Stack>
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
  return <>
    <Box className={`history-media-grid ${single ? 'history-media-single' : ''}`} sx={{ mt: 1.5 }}>
      {visible.map((item, index) => <MediaTile key={`${item.url}-${index}`} item={item} extra={index === 8 ? available.length - 9 : 0} single={single} index={index} onOpen={() => setSelected(index)} />)}
    </Box>
    <MediaLightbox media={visible} selected={selected} onSelect={setSelected} onClose={() => setSelected(null)} />
  </>
}

function MediaTile({ item, extra, single, index, onOpen }: { item: DynamicMedia; extra: number; single: boolean; index: number; onOpen: () => void }) {
  const [failed, setFailed] = useState(false)
  const src = historyMediaURL(item.url, single ? 720 : 240)
  return <Box className="history-media-tile" sx={single && item.width && item.height ? { aspectRatio: `${item.width} / ${item.height}` } : undefined}>
    {failed ? <Stack className="history-media-fallback" alignItems="center" justifyContent="center"><BrokenImage /><Typography variant="caption">媒体加载失败</Typography></Stack>
      : <button type="button" className="history-media-button" aria-label={`放大第 ${index + 1} 张${item.kind === 'cover' ? '内容封面' : '动态图片'}`} onClick={onOpen}>
        <img src={src} alt={item.kind === 'cover' ? '内容封面' : '动态图片'} loading="lazy" onError={() => setFailed(true)} />
      </button>}
    {extra > 0 && <Box className="history-media-extra">+{extra}</Box>}
  </Box>
}

function OriginalDynamicPreview({ item }: { item: NonNullable<DynamicHistoryItem['original']> }) {
  const contentCard = isContentCardType(item.type)
  const body = contentCard ? (item.summary || '').trim() : composePreviewBody(item.summary, item.description)
  const title = contentCard ? '' : (item.title || '').trim()
  return <Paper variant="outlined" sx={{ mt: 1.5, p: 1.5, bgcolor: 'action.hover' }}>
    <Typography variant="caption" color="text.secondary">转发自 {item.up_name || item.uid || '原动态'}</Typography>
    {title && <Typography fontWeight={700} sx={{ mt: .5 }}>{title}</Typography>}
    {body && normalizePreviewText(body) !== normalizePreviewText(title) && <Typography className="history-original-clamp" sx={{ mt: .5, whiteSpace: 'pre-wrap' }}>{body}</Typography>}
    <DynamicContentPreview item={item} />
    {!title && !body && !item.media?.length && <Typography color="text.secondary" sx={{ mt: .5 }}>原动态内容未被归档</Typography>}
  </Paper>
}

function DynamicContentPreview({ item }: { item: HistoryContent }) {
  if (!item.media?.length) return null
  if (!isContentCardType(item.type)) return <DynamicMediaGrid media={item.media} />
  const cover = item.media.find(media => media.url)
  if (!cover) return null
  return <ContentLandingCard item={item} cover={cover} />
}

function ContentLandingCard({ item, cover }: { item: HistoryContent; cover: DynamicMedia }) {
  const [selected, setSelected] = useState<number | null>(null)
  const [failed, setFailed] = useState(false)
  const media = (item.media || []).filter(entry => entry.url)
  return <>
    <Paper variant="outlined" className="history-content-card">
      <Box className="history-content-cover">
        {failed ? <Stack className="history-media-fallback" alignItems="center" justifyContent="center"><BrokenImage /><Typography variant="caption">封面加载失败</Typography></Stack>
          : <button type="button" className="history-media-button" aria-label="放大内容封面" onClick={() => setSelected(Math.max(0, media.indexOf(cover)))}>
            <img src={historyMediaURL(cover.url, 720)} alt="内容封面" loading="lazy" onError={() => setFailed(true)} />
            {item.video?.duration && <span className="history-video-duration">{item.video.duration}</span>}
          </button>}
      </Box>
      <Stack className="history-content-meta" spacing={1}>
        <Typography fontWeight={750}>{item.title || dynamicTypeLabel(item.type || '')}</Typography>
        {item.description && <Typography variant="body2" color="text.secondary" className="history-content-description">{item.description}</Typography>}
        {item.video && <Stack direction="row" spacing={2} color="text.secondary" mt="auto">
          {item.video.views && <Stack direction="row" spacing={.5} alignItems="center"><Visibility fontSize="small" /><Typography variant="caption">{item.video.views}</Typography></Stack>}
          {item.video.danmaku && <Typography variant="caption">弹幕 {item.video.danmaku}</Typography>}
        </Stack>}
      </Stack>
    </Paper>
    <MediaLightbox media={media} selected={selected} onSelect={setSelected} onClose={() => setSelected(null)} />
  </>
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
  return <Dialog
    open={open}
    onClose={onClose}
    maxWidth={false}
    className="history-lightbox"
    onKeyDown={event => {
      if (event.key === 'ArrowLeft') move(-1)
      if (event.key === 'ArrowRight') move(1)
      if (event.key === 'Escape') {
        event.preventDefault()
        onClose()
      }
    }}
    PaperProps={{ 'aria-label': '图片预览', sx: { bgcolor: 'transparent', boxShadow: 'none', overflow: 'visible', maxWidth: 'calc(100vw - 32px)', maxHeight: 'calc(100vh - 32px)' } }}
  >
    <Box className="history-lightbox-stage">
      <IconButton className="history-lightbox-close" aria-label="关闭图片预览" onClick={onClose}><Close /></IconButton>
      {current && (failed
        ? <Stack className="history-lightbox-fallback" alignItems="center" justifyContent="center"><BrokenImage /><Typography>图片加载失败</Typography></Stack>
        : <img src={current.url} alt={`预览第 ${selected! + 1} 张图片`} onError={() => setFailed(true)} />)}
      {media.length > 1 && <>
        <IconButton className="history-lightbox-previous" aria-label="上一张图片" disabled={selected === 0} onClick={() => move(-1)}><ChevronLeft /></IconButton>
        <IconButton className="history-lightbox-next" aria-label="下一张图片" disabled={selected === media.length - 1} onClick={() => move(1)}><ChevronRight /></IconButton>
        <Typography className="history-lightbox-count" variant="body2">{selected! + 1} / {media.length}</Typography>
      </>}
    </Box>
  </Dialog>
}

function HistoryStat({ icon, value, emptyLabel, label }: { icon: React.ReactNode; value: number; emptyLabel: string; label: string }) {
  return <Stack direction="row" spacing={.5} alignItems="center" color="text.secondary" aria-label={`${label} ${value}`}>
    {icon}<Typography variant="body2">{formatInteractionCount(value, emptyLabel)}</Typography>
  </Stack>
}

function isContentCardType(type?: string) {
  return type === 'DYNAMIC_TYPE_AV' || type === 'DYNAMIC_TYPE_ARTICLE' || type === 'DYNAMIC_TYPE_PGC' || type === 'DYNAMIC_TYPE_COMMON_SQUARE'
}

function historyAvatarText(value: string) {
  return Array.from(value.trim())[0] || 'UP'
}

export function formatInteractionCount(value: number, emptyLabel: string) {
  if (!Number.isFinite(value) || value <= 0) return emptyLabel
  if (value < 10_000) return Math.floor(value).toLocaleString('zh-CN')
  if (value < 100_000_000) return `${trimFixed(value / 10_000)}万`
  return `${trimFixed(value / 100_000_000)}亿`
}

function trimFixed(value: number) {
  return value.toFixed(value >= 100 ? 0 : 1).replace(/\.0$/, '')
}

export function formatRelativeDate(value: string, now = Date.now()) {
  const date = new Date(value)
  if (Number.isNaN(date.valueOf())) return '—'
  const elapsed = now - date.valueOf()
  if (elapsed < 0 || elapsed >= 7 * 24 * 60 * 60 * 1000) return formatDate(value)
  if (elapsed < 60 * 1000) return '刚刚'
  if (elapsed < 60 * 60 * 1000) return `${Math.floor(elapsed / (60 * 1000))}分钟前`
  if (elapsed < 24 * 60 * 60 * 1000) return `${Math.floor(elapsed / (60 * 60 * 1000))}小时前`
  return `${Math.floor(elapsed / (24 * 60 * 60 * 1000))}天前`
}

export function composePreviewBody(summary?: string, description?: string) {
  const parts = [summary, description].map(value => (value || '').trim()).filter(Boolean)
  if (parts.length === 0) return ''
  if (parts.length === 1) return parts[0]
  if (normalizePreviewText(parts[0]) === normalizePreviewText(parts[1])) return parts[0]
  return parts.join('\n\n')
}

export function historyMediaURL(url: string, width: number) {
  const value = url.trim()
  if (!value || width <= 0) return value
  if (value.startsWith('/api/v1/dynamics/')) return value
  try {
    const parsed = new URL(value, 'https://www.bilibili.com')
    if (!/(^|\.)hdslb\.com$/i.test(parsed.hostname) || !parsed.pathname.includes('/bfs/')) return value
    // Bilibili CDN accepts @<width>w on bfs assets for list-size tiles.
    parsed.pathname = parsed.pathname.replace(/@[^/]*$/, '') + `@${Math.round(width)}w`
    return parsed.toString()
  } catch {
    return value
  }
}

export function normalizePreviewText(value: string) {
  return value.trim().replace(/\s+/g, ' ')
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

function CommentHistoryDialog({ open, detail, fullScreen, onClose }: {
  open: boolean
  detail: CommentDetail | null
  fullScreen: boolean
  onClose: () => void
}) {
  if (!detail) return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm" />
  return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm">
    <DialogTitle>{detail.content_title || 'UP 回复'}</DialogTitle>
    <DialogContent><Stack spacing={1.5} sx={{ pt: 1 }}>
      <Typography variant="body2" color="text.secondary">{detail.up_name} · {formatDate(detail.published_at)}</Typography>
      {detail.incomplete && <Alert severity="warning">对话串可能不完整（翻页窗口外）。</Alert>}
      {detail.thread?.map(node => <Paper key={node.rpid} variant="outlined" sx={{ p: 1.5, bgcolor: node.is_trigger ? 'action.selected' : 'background.paper' }}>
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
      {detail.content_url && <Button startIcon={<OpenInNew />} href={detail.content_url} target="_blank" rel="noopener noreferrer">打开原内容</Button>}
      <Button onClick={onClose}>关闭</Button>
    </DialogActions>
  </Dialog>
}

function SettingsPage({ csrf, preference, setPreference, settings, api, runMutation, onChanged }: {
  csrf: string
  preference: ThemePreference
  setPreference: (value: ThemePreference) => void
  settings: RuntimeSettings
  api: AdminAPI
  runMutation: RunMutation
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
      await runMutation(() => api.updateSettings({
        poll_interval_sec,
        request_rate,
        request_concurrency,
        comment_enabled: commentEnabled,
        comment_track_n,
        comment_root_pages,
        comment_reply_pages,
        comment_batch_interval_sec,
      }), applySettingsMutation)
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
    dingtalk: [{ key: 'webhook', label: 'Webhook URL', required: true, secret: true }, { key: 'secret', label: '签名密钥', required: true, secret: true, help: '钉钉自定义机器人仅支持公开图链，图片使用 B 站 CDN 外链。' }],
    feishu: [
      { key: 'webhook', label: 'Webhook URL', required: true, secret: true },
      { key: 'secret', label: '签名密钥', required: true, secret: true },
      { key: 'app_id', label: '应用 App ID', help: '可选。配置后图片以上传 image_key 发送；不配置则图片显示为链接。' },
      { key: 'app_secret', label: '应用 App Secret', secret: true, help: '与 App ID 成对配置。' },
    ],
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
function settingLabel(value: string) { return ({ host: '主机', port: '端口', tls: 'TLS', from: '发件人', to: '收件人', username: '用户名', password: '密码', webhook: 'Webhook', secret: '签名密钥', client_id: '客户端 ID', tenant: '租户', access_token: '访问令牌', refresh_token: '刷新令牌', app_id: '应用 App ID', app_secret: '应用 App Secret' } as Record<string, string>)[value] || value }
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
