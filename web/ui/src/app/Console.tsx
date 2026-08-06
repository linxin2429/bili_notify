import { lazy, Suspense, useCallback, useEffect, useMemo, useRef, useState } from 'react'
import Autorenew from '@mui/icons-material/Autorenew'
import BrightnessAuto from '@mui/icons-material/BrightnessAuto'
import CheckCircle from '@mui/icons-material/CheckCircle'
import DarkMode from '@mui/icons-material/DarkMode'
import Dashboard from '@mui/icons-material/Dashboard'
import History from '@mui/icons-material/History'
import Hub from '@mui/icons-material/Hub'
import LightMode from '@mui/icons-material/LightMode'
import Logout from '@mui/icons-material/Logout'
import MenuIcon from '@mui/icons-material/Menu'
import NotificationsActive from '@mui/icons-material/NotificationsActive'
import People from '@mui/icons-material/People'
import ReceiptLong from '@mui/icons-material/ReceiptLong'
import Refresh from '@mui/icons-material/Refresh'
import Settings from '@mui/icons-material/Settings'
import WarningAmber from '@mui/icons-material/WarningAmber'
import { Alert, AppBar, Avatar, BottomNavigation, BottomNavigationAction, Box, Chip, CircularProgress, Container, Drawer, IconButton, List, ListItemButton, ListItemIcon, ListItemText, Paper, Snackbar, Stack, Toolbar, Tooltip, Typography, useMediaQuery } from '@mui/material'
import { Navigate, Route, Routes, useLocation, useNavigate } from 'react-router-dom'
import { AdminAPI, httpJSON } from '../api'
import { emptyResponseSchema } from '../contracts'
import { applyUpdate } from '../dashboard'
import { errorMessage, formatDate, nextTheme, themeLabel } from '../presentation'
import { RealtimeClient } from '../realtime'
import type { ConnectionState, DashboardSnapshot, ThemePreference } from '../types'
import type { RunMutation } from './shared'

const OverviewPage = lazy(() => import('../pages/OverviewPage').then(module => ({ default: module.OverviewPage })))
const UPsPage = lazy(() => import('../pages/UPsPage').then(module => ({ default: module.UPsPage })))
const ChannelsPage = lazy(() => import('../pages/ChannelsPage').then(module => ({ default: module.ChannelsPage })))
const DeliveriesPage = lazy(() => import('../pages/DeliveriesPage').then(module => ({ default: module.DeliveriesPage })))
const HistoryPage = lazy(() => import('../pages/HistoryPage').then(module => ({ default: module.HistoryPage })))
const AuditLogsPage = lazy(() => import('../pages/AuditLogsPage').then(module => ({ default: module.AuditLogsPage })))
const SettingsPage = lazy(() => import('../pages/SettingsPage').then(module => ({ default: module.SettingsPage })))

const drawerWidth = 236
const navigation = [
  { path: '/overview', label: '概览', icon: <Dashboard /> }, { path: '/ups', label: 'UP 主', icon: <People /> },
  { path: '/channels', label: '通知渠道', icon: <NotificationsActive /> }, { path: '/deliveries', label: '投递队列', icon: <Hub /> },
  { path: '/history', label: '历史', icon: <History /> }, { path: '/audit-logs', label: '操作日志', icon: <ReceiptLong /> },
  { path: '/settings', label: '设置', icon: <Settings /> },
]

export function Console({ csrf, themePreference, setThemePreference, onAuthLost }: { csrf: string; themePreference: ThemePreference; setThemePreference: (value: ThemePreference) => void; onAuthLost: () => void | Promise<void> }) {
  const [snapshot, setSnapshot] = useState<DashboardSnapshot | null>(null); const [connection, setConnection] = useState<ConnectionState>('connecting'); const [message, setMessage] = useState(''); const [mobileOpen, setMobileOpen] = useState(false); const [pageRefresh, setPageRefresh] = useState(0)
  const snapshotVersion = useRef(0); const refreshRequest = useRef(0); const api = useMemo(() => new AdminAPI(csrf), [csrf]); const mobile = useMediaQuery(theme => theme.breakpoints.down('md')); const location = useLocation(); const navigate = useNavigate()
  useEffect(() => {
    const client = new RealtimeClient({ onSnapshot: value => { snapshotVersion.current += 1; setSnapshot(value) }, onEvent: (event, data) => { snapshotVersion.current += 1; setSnapshot(current => applyUpdate(current, event, data)) }, onState: setConnection, onAuthLost: () => void onAuthLost(), onError: setMessage })
    client.start(); return () => client.stop()
  }, [onAuthLost])
  const runMutation = useCallback<RunMutation>(async (request, update) => {
    try { const value = await request(); if (update) setSnapshot(current => current ? update(current, value) : current); return value } catch (error) { setMessage(errorMessage(error)); throw error }
  }, [])
  const refreshDashboard = useCallback(async () => {
    const request = ++refreshRequest.current; const version = snapshotVersion.current
    try { const value = await api.dashboard(); if (!canApplyDashboardRefresh(request, refreshRequest.current, version, snapshotVersion.current)) return; snapshotVersion.current += 1; setSnapshot(value) } catch (error) { if (request === refreshRequest.current) setMessage(errorMessage(error)) }
  }, [api])
  const logout = async () => { try { await httpJSON('/api/v1/session', emptyResponseSchema, { method: 'DELETE' }, csrf) } finally { await onAuthLost() } }
  const activePath = navigation.find(item => location.pathname.startsWith(item.path))?.path || '/overview'
  const navigateTo = (path: string) => { activateNavigation(activePath, path, navigate, () => { setPageRefresh(current => current + 1); void refreshDashboard() }); setMobileOpen(false) }
  const connectionMeta = connectionPresentation(connection)
  return <Box minHeight="100vh" bgcolor="background.default">
    <AppBar position="fixed" color="inherit" elevation={0} sx={{ borderBottom: 1, borderColor: 'divider', zIndex: theme => theme.zIndex.drawer + 1 }}><Toolbar>{mobile && <IconButton edge="start" onClick={() => setMobileOpen(true)} aria-label="打开导航"><MenuIcon /></IconButton>}<Avatar sx={{ bgcolor: 'primary.main', width: 34, height: 34, fontSize: 14, fontWeight: 800, ml: mobile ? 1 : 0 }}>BN</Avatar><Typography fontWeight={800} sx={{ ml: 1.25, flexGrow: 1 }}>Bili Notify</Typography><Tooltip title={`实时状态：${connectionMeta.label}`}><Chip size="small" icon={connectionMeta.icon} color={connectionMeta.color} label={connectionMeta.label} variant={connection === 'live' ? 'filled' : 'outlined'} /></Tooltip><Tooltip title={`主题：${themeLabel(themePreference)}`}><IconButton onClick={() => setThemePreference(nextTheme(themePreference))} aria-label="切换主题">{themePreference === 'system' ? <BrightnessAuto /> : themePreference === 'dark' ? <DarkMode /> : <LightMode />}</IconButton></Tooltip><Tooltip title="退出登录"><IconButton onClick={() => void logout()} aria-label="退出登录"><Logout /></IconButton></Tooltip></Toolbar></AppBar>
    <Drawer variant={mobile ? 'temporary' : 'permanent'} open={mobile ? mobileOpen : true} onClose={() => setMobileOpen(false)} sx={{ width: drawerWidth, flexShrink: 0, '& .MuiDrawer-paper': { width: drawerWidth, mt: '64px', height: 'calc(100% - 64px)', borderRightColor: 'divider' } }}><List sx={{ p: 1.5 }}>{navigation.map(item => <ListItemButton key={item.path} selected={activePath === item.path} onClick={() => navigateTo(item.path)} sx={{ borderRadius: 2, mb: .5 }}><ListItemIcon>{item.icon}</ListItemIcon><ListItemText primary={item.label} /></ListItemButton>)}</List></Drawer>
    <Box component="main" sx={{ ml: mobile ? 0 : `${drawerWidth}px`, pt: '64px', pb: mobile ? '74px' : 3 }}>{connection !== 'live' && snapshot && <Alert severity="warning" icon={<Autorenew />} sx={{ borderRadius: 0 }}>实时连接已中断，正在保留 {formatDate(snapshot.updated_at, snapshot.timezone)} 的最后状态并尝试重连。</Alert>}<Container maxWidth="xl" sx={{ py: { xs: 2, sm: 3 } }}>{!snapshot ? <Box minHeight="50vh" display="grid" sx={{ placeItems: 'center' }}><Stack alignItems="center" spacing={2}><CircularProgress /><Typography color="text.secondary">正在加载实时状态</Typography></Stack></Box> : <Suspense fallback={<Box minHeight="50vh" display="grid" sx={{ placeItems: 'center' }}><CircularProgress aria-label="正在加载页面" /></Box>}><Routes><Route path="/overview" element={<OverviewPage snapshot={snapshot} api={api} runMutation={runMutation} />} /><Route path="/ups" element={<UPsPage ups={snapshot.ups} timeZone={snapshot.timezone} api={api} runMutation={runMutation} />} /><Route path="/channels" element={<ChannelsPage channels={snapshot.channels} logins={snapshot.microsoft_logins} api={api} runMutation={runMutation} />} /><Route path="/deliveries" element={<DeliveriesPage deliveries={snapshot.deliveries} channels={snapshot.channels} total={snapshot.status.outbox_depth} timeZone={snapshot.timezone} api={api} runMutation={runMutation} refreshDashboard={refreshDashboard} />} /><Route path="/history" element={<HistoryPage ups={snapshot.ups} timeZone={snapshot.timezone} api={api} refresh={pageRefresh} />} /><Route path="/audit-logs" element={<AuditLogsPage api={api} timeZone={snapshot.timezone} refresh={pageRefresh} />} /><Route path="/settings" element={<SettingsPage csrf={csrf} preference={themePreference} setPreference={setThemePreference} settings={snapshot.settings} api={api} runMutation={runMutation} onChanged={() => void onAuthLost()} />} /><Route path="*" element={<Navigate to="/overview" replace />} /></Routes></Suspense>}</Container></Box>
    {mobile && <Paper elevation={6} sx={{ position: 'fixed', left: 0, right: 0, bottom: 0, zIndex: theme => theme.zIndex.appBar }}><BottomNavigation value={activePath} onChange={(_, value) => navigateTo(value)} showLabels>{navigation.slice(0, 5).map(item => <BottomNavigationAction key={item.path} value={item.path} label={item.label} icon={item.icon} />)}</BottomNavigation></Paper>}
    <Snackbar open={Boolean(message)} autoHideDuration={6000} onClose={() => setMessage('')} message={message} />
  </Box>
}

export function activateNavigation(activePath: string, targetPath: string, navigate: (path: string) => void, refresh: () => void) { if (activePath !== targetPath) navigate(targetPath); refresh() }
export function canApplyDashboardRefresh(request: number, latestRequest: number, version: number, latestVersion: number) { return request === latestRequest && version === latestVersion }

function connectionPresentation(state: ConnectionState): { label: string; color: 'success' | 'warning'; icon: React.ReactElement } {
  if (state === 'live') return { label: '实时', color: 'success', icon: <CheckCircle /> }
  if (state === 'connecting' || state === 'reconnecting') return { label: '重连中', color: 'warning', icon: <Refresh /> }
  return { label: '数据过期', color: 'warning', icon: <WarningAmber /> }
}
