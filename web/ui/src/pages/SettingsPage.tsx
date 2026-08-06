import { useEffect, useState } from 'react'
import BrightnessAuto from '@mui/icons-material/BrightnessAuto'
import DarkMode from '@mui/icons-material/DarkMode'
import LightMode from '@mui/icons-material/LightMode'
import Password from '@mui/icons-material/Password'
import { Alert, Box, Button, Card, CardContent, FormControlLabel, Stack, Switch, TextField, Typography } from '@mui/material'
import type { AdminAPI } from '../api'
import { httpJSON } from '../api'
import { emptyResponseSchema } from '../contracts'
import { applySettingsMutation } from '../dashboard'
import { errorMessage, themeLabel } from '../presentation'
import type { RuntimeSettings, ThemePreference } from '../types'
import { PageHeader, type RunMutation } from '../app/shared'
import { parseRuntimeSettingsForm } from './settings-form'

export function SettingsPage({ csrf, preference, setPreference, settings, api, runMutation, onChanged }: { csrf: string; preference: ThemePreference; setPreference: (value: ThemePreference) => void; settings: RuntimeSettings; api: AdminAPI; runMutation: RunMutation; onChanged: () => void }) {
  const [current, setCurrent] = useState(''); const [replacement, setReplacement] = useState(''); const [confirm, setConfirm] = useState(''); const [message, setMessage] = useState(''); const [busy, setBusy] = useState(false)
  const [pollSec, setPollSec] = useState(String(settings.poll_interval_sec)); const [requestRate, setRequestRate] = useState(String(settings.request_rate)); const [concurrency, setConcurrency] = useState(String(settings.request_concurrency)); const [commentEnabled, setCommentEnabled] = useState(Boolean(settings.comment_enabled)); const [commentTrackN, setCommentTrackN] = useState(String(settings.comment_track_n)); const [commentRootPages, setCommentRootPages] = useState(String(settings.comment_root_pages)); const [commentReplyPages, setCommentReplyPages] = useState(String(settings.comment_reply_pages)); const [commentBatchSec, setCommentBatchSec] = useState(String(settings.comment_batch_interval_sec)); const [settingsMessage, setSettingsMessage] = useState(''); const [settingsBusy, setSettingsBusy] = useState(false)
  useEffect(() => { setPollSec(String(settings.poll_interval_sec)); setRequestRate(String(settings.request_rate)); setConcurrency(String(settings.request_concurrency)); setCommentEnabled(Boolean(settings.comment_enabled)); setCommentTrackN(String(settings.comment_track_n)); setCommentRootPages(String(settings.comment_root_pages)); setCommentReplyPages(String(settings.comment_reply_pages)); setCommentBatchSec(String(settings.comment_batch_interval_sec)) }, [settings])
  const change = async () => {
    if (replacement !== confirm) { setMessage('两次输入的新密码不一致'); return }
    setBusy(true)
    try { await httpJSON('/api/v1/session/password', emptyResponseSchema, { method: 'PUT', body: JSON.stringify({ current_password: current, new_password: replacement }) }, csrf); await onChanged() } catch (error) { setMessage(errorMessage(error)) } finally { setBusy(false) }
  }
  const saveSettings = async () => {
    const parsed = parseRuntimeSettingsForm({ pollSec, requestRate, concurrency, commentEnabled, commentTrackN, commentRootPages, commentReplyPages, commentBatchSec })
    if (!parsed.ok) { setSettingsMessage(parsed.error); return }
    setSettingsBusy(true); setSettingsMessage('')
    try { await runMutation(() => api.updateSettings(parsed.value), applySettingsMutation) } catch (error) { setSettingsMessage(errorMessage(error)) } finally { setSettingsBusy(false) }
  }
  return <Stack spacing={3}><PageHeader title="设置" subtitle="管理采集参数、本浏览器外观与管理员凭据。" />
    <Card><CardContent><Stack spacing={2} maxWidth={520}><Box><Typography variant="h6" fontWeight={800}>采集参数</Typography><Typography color="text.secondary">修改后立即生效并写入数据库；重启后仍以这里的值为准。命令行参数仅在首次启动空库时作为默认值。</Typography></Box><TextField label="轮询间隔（秒）" type="number" value={pollSec} onChange={event => setPollSec(event.target.value)} helperText="至少 10 秒" inputProps={{ min: 10, step: 1 }} /><TextField label="请求速率（次/秒）" type="number" value={requestRate} onChange={event => setRequestRate(event.target.value)} helperText="(0, 10]" inputProps={{ min: 0.1, max: 10, step: 0.1 }} /><TextField label="并发数" type="number" value={concurrency} onChange={event => setConcurrency(event.target.value)} helperText="1 到 16" inputProps={{ min: 1, max: 16, step: 1 }} /><FormControlLabel control={<Switch checked={commentEnabled} onChange={event => setCommentEnabled(event.target.checked)} />} label="启用 UP 评论回复监控" /><TextField label="每 UP 跟踪内容数 N" type="number" value={commentTrackN} onChange={event => setCommentTrackN(event.target.value)} disabled={!commentEnabled} /><TextField label="根评论最大页数" type="number" value={commentRootPages} onChange={event => setCommentRootPages(event.target.value)} disabled={!commentEnabled} /><TextField label="子评论最大页数" type="number" value={commentReplyPages} onChange={event => setCommentReplyPages(event.target.value)} disabled={!commentEnabled} /><TextField label="评论批次间隔（秒）" type="number" value={commentBatchSec} onChange={event => setCommentBatchSec(event.target.value)} disabled={!commentEnabled} />{settingsMessage && <Alert severity="error">{settingsMessage}</Alert>}<Button variant="contained" disabled={settingsBusy} onClick={() => void saveSettings()}>保存采集参数</Button></Stack></CardContent></Card>
    <Card><CardContent><Typography variant="h6" fontWeight={800}>外观</Typography><Typography color="text.secondary" gutterBottom>跟随系统会响应操作系统的明暗模式。</Typography><Stack direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ mt: 2 }}>{(['system', 'light', 'dark'] as ThemePreference[]).map(value => <Button key={value} variant={preference === value ? 'contained' : 'outlined'} startIcon={value === 'system' ? <BrightnessAuto /> : value === 'dark' ? <DarkMode /> : <LightMode />} onClick={() => setPreference(value)}>{themeLabel(value)}</Button>)}</Stack></CardContent></Card>
    <Card><CardContent><Stack spacing={2} maxWidth={520}><Box><Typography variant="h6" fontWeight={800}>修改管理员密码</Typography><Typography color="text.secondary">修改后所有设备会话都会立即失效。</Typography></Box><TextField label="当前密码" type="password" value={current} onChange={event => setCurrent(event.target.value)} autoComplete="current-password" /><TextField label="新密码" type="password" value={replacement} onChange={event => setReplacement(event.target.value)} autoComplete="new-password" helperText="至少 12 个字节" /><TextField label="确认新密码" type="password" value={confirm} onChange={event => setConfirm(event.target.value)} autoComplete="new-password" />{message && <Alert severity="error">{message}</Alert>}<Button variant="contained" startIcon={<Password />} disabled={busy || !current || !replacement} onClick={() => void change()}>修改密码</Button></Stack></CardContent></Card>
  </Stack>
}
