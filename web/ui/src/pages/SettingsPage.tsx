import { useEffect, useState } from 'react'
import BrightnessAuto from '@mui/icons-material/BrightnessAuto'
import DarkMode from '@mui/icons-material/DarkMode'
import LightMode from '@mui/icons-material/LightMode'
import Password from '@mui/icons-material/Password'
import { Alert, Box, Button, Card, CardContent, FormControlLabel, MenuItem, Stack, Switch, TextField, Typography } from '@mui/material'
import type { AdminAPI } from '../api'
import { httpJSON } from '../api'
import { emptyResponseSchema } from '../contracts'
import { applySettingsMutation } from '../dashboard'
import { errorMessage, themeLabel } from '../presentation'
import type { RuntimeSettings, ThemePreference } from '../types'
import { PageHeader, type RunMutation } from '../app/shared'
import { parseRuntimeSettingsForm, runtimeSettingsToForm, type RuntimeSettingsForm } from './settings-form'

interface SettingsPageProps {
  csrf: string
  preference: ThemePreference
  setPreference: (value: ThemePreference) => void
  settings: RuntimeSettings
  api: AdminAPI
  runMutation: RunMutation
  onChanged: () => void
}

export function SettingsPage({ csrf, preference, setPreference, settings, api, runMutation, onChanged }: SettingsPageProps) {
  const [current, setCurrent] = useState('')
  const [replacement, setReplacement] = useState('')
  const [confirm, setConfirm] = useState('')
  const [message, setMessage] = useState('')
  const [busy, setBusy] = useState(false)
  const [form, setForm] = useState<RuntimeSettingsForm>(() => runtimeSettingsToForm(settings))
  const [settingsMessage, setSettingsMessage] = useState('')
  const [settingsBusy, setSettingsBusy] = useState(false)

  useEffect(() => setForm(runtimeSettingsToForm(settings)), [settings])

  const setField = <K extends keyof RuntimeSettingsForm>(field: K, value: RuntimeSettingsForm[K]) => {
    setForm(previous => ({ ...previous, [field]: value }))
  }
  const setRetryDelay = (index: number, value: string) => {
    const next = [...form.retryDelaysSec] as RuntimeSettingsForm['retryDelaysSec']
    next[index] = value
    setField('retryDelaysSec', next)
  }
  const change = async () => {
    if (replacement !== confirm) { setMessage('两次输入的新密码不一致'); return }
    setBusy(true)
    try {
      await httpJSON('/api/v1/session/password', emptyResponseSchema, { method: 'PUT', body: JSON.stringify({ current_password: current, new_password: replacement }) }, csrf)
      await onChanged()
    } catch (error) { setMessage(errorMessage(error)) } finally { setBusy(false) }
  }
  const saveSettings = async () => {
    const parsed = parseRuntimeSettingsForm(form)
    if (!parsed.ok) { setSettingsMessage(parsed.error); return }
    setSettingsBusy(true); setSettingsMessage('')
    try { await runMutation(() => api.updateSettings(parsed.value), applySettingsMutation) } catch (error) { setSettingsMessage(errorMessage(error)) } finally { setSettingsBusy(false) }
  }

  return <Stack spacing={3}>
    <PageHeader title="设置" subtitle="管理运行参数、本浏览器外观与管理员凭据。" />
    <SettingsCard title="基础采集" description="保存后写入数据库并用于后续采集周期；正在执行的任务不会被取消。">
      <TextField label="轮询间隔（秒）" type="number" value={form.pollSec} onChange={event => setField('pollSec', event.target.value)} helperText="10–86400" inputProps={{ min: 10, max: 86400, step: 1 }} />
      <TextField label="请求速率（次/秒）" type="number" value={form.requestRate} onChange={event => setField('requestRate', event.target.value)} helperText="(0, 10]" inputProps={{ min: 0.1, max: 10, step: 0.1 }} />
      <TextField label="请求并发数" type="number" value={form.concurrency} onChange={event => setField('concurrency', event.target.value)} helperText="1–16" inputProps={{ min: 1, max: 16, step: 1 }} />
      <FormControlLabel control={<Switch checked={form.commentEnabled} onChange={event => setField('commentEnabled', event.target.checked)} />} label="启用 UP 评论回复监控" />
      <TextField label="每 UP 跟踪内容数 N" type="number" value={form.commentTrackN} onChange={event => setField('commentTrackN', event.target.value)} disabled={!form.commentEnabled} helperText="1–50" />
      <TextField label="根评论最大页数" type="number" value={form.commentRootPages} onChange={event => setField('commentRootPages', event.target.value)} disabled={!form.commentEnabled} helperText="1–10" />
      <TextField label="子评论最大页数" type="number" value={form.commentReplyPages} onChange={event => setField('commentReplyPages', event.target.value)} disabled={!form.commentEnabled} helperText="1–20" />
      <TextField label="评论批次间隔（秒）" type="number" value={form.commentBatchSec} onChange={event => setField('commentBatchSec', event.target.value)} disabled={!form.commentEnabled} helperText="30–86400" />
    </SettingsCard>

    <SettingsCard title="高级采集" description="这些参数会改变 B 站请求深度与风控后的恢复节奏，请保持保守值。">
      <TextField label="关注关系刷新间隔（秒）" type="number" value={form.relationRefreshSec} onChange={event => setField('relationRefreshSec', event.target.value)} helperText="60–86400" />
      <TextField label="空间完整性校验间隔（秒）" type="number" value={form.spaceReconcileSec} onChange={event => setField('spaceReconcileSec', event.target.value)} helperText="300–604800" />
      <TextField label="动态最大翻页数" type="number" value={form.maxDynamicPages} onChange={event => setField('maxDynamicPages', event.target.value)} helperText="1–20；同时作用于综合流和空间动态" />
      <TextField label="风控暂停时长（秒）" type="number" value={form.riskPauseSec} onChange={event => setField('riskPauseSec', event.target.value)} helperText="60–3600；仅影响之后发生的风控暂停" />
    </SettingsCard>

    <SettingsCard title="投递与告警" description="新策略只影响之后的投递批次和失败；已写入的重试时间不会被改写。">
      <TextField label="投递并发数" type="number" value={form.deliveryConcurrency} onChange={event => setField('deliveryConcurrency', event.target.value)} helperText="1–32" />
      <TextField label="积压条数告警阈值" type="number" value={form.backlogAlertCount} onChange={event => setField('backlogAlertCount', event.target.value)} helperText="1–100000" />
      <TextField label="积压时长告警阈值（秒）" type="number" value={form.backlogAlertAgeSec} onChange={event => setField('backlogAlertAgeSec', event.target.value)} helperText="60–86400" />
      <Typography variant="subtitle2" fontWeight={750}>五段重试上限（秒）</Typography>
      <Typography variant="body2" color="text.secondary">实际延迟在每段配置值的 50%–100% 之间；第五段会持续复用。</Typography>
      <Stack direction={{ xs: 'column', sm: 'row' }} spacing={1.5}>
        {form.retryDelaysSec.map((value, index) => <TextField key={index} label={`第 ${index + 1} 段`} type="number" value={value} onChange={event => setRetryDelay(index, event.target.value)} inputProps={{ min: 1, max: 86400, step: 1 }} />)}
      </Stack>
    </SettingsCard>

    <SettingsCard title="日志" description="日志级别立即生效；缩短保留期后，旧数据在下一次清理或轮转维护时删除。">
      <TextField select label="日志级别" value={form.logLevel} onChange={event => setField('logLevel', event.target.value as RuntimeSettings['log_level'])}>
        {(['debug', 'info', 'warn', 'error'] as const).map(level => <MenuItem key={level} value={level}>{level}</MenuItem>)}
      </TextField>
      <TextField label="审计日志保留天数" type="number" value={form.auditRetentionDays} onChange={event => setField('auditRetentionDays', event.target.value)} helperText="1–3650；下一次每日清理生效" />
      <TextField label="系统日志保留天数" type="number" value={form.systemRetentionDays} onChange={event => setField('systemRetentionDays', event.target.value)} helperText="1–3650；下一次日志轮转维护生效" />
    </SettingsCard>

    {settingsMessage && <Alert severity="error">{settingsMessage}</Alert>}
    <Button variant="contained" disabled={settingsBusy} onClick={() => void saveSettings()}>保存运行设置</Button>

    <Card><CardContent><Typography variant="h6" fontWeight={800}>外观</Typography><Typography color="text.secondary" gutterBottom>跟随系统会响应操作系统的明暗模式。</Typography><Stack direction={{ xs: 'column', sm: 'row' }} spacing={1} sx={{ mt: 2 }}>{(['system', 'light', 'dark'] as ThemePreference[]).map(value => <Button key={value} variant={preference === value ? 'contained' : 'outlined'} startIcon={value === 'system' ? <BrightnessAuto /> : value === 'dark' ? <DarkMode /> : <LightMode />} onClick={() => setPreference(value)}>{themeLabel(value)}</Button>)}</Stack></CardContent></Card>
    <Card><CardContent><Stack spacing={2} maxWidth={520}><Box><Typography variant="h6" fontWeight={800}>修改管理员密码</Typography><Typography color="text.secondary">修改后所有设备会话都会立即失效。</Typography></Box><TextField label="当前密码" type="password" value={current} onChange={event => setCurrent(event.target.value)} autoComplete="current-password" /><TextField label="新密码" type="password" value={replacement} onChange={event => setReplacement(event.target.value)} autoComplete="new-password" helperText="至少 12 个字节" /><TextField label="确认新密码" type="password" value={confirm} onChange={event => setConfirm(event.target.value)} autoComplete="new-password" />{message && <Alert severity="error">{message}</Alert>}<Button variant="contained" startIcon={<Password />} disabled={busy || !current || !replacement} onClick={() => void change()}>修改密码</Button></Stack></CardContent></Card>
  </Stack>
}

function SettingsCard({ title, description, children }: { title: string; description: string; children: React.ReactNode }) {
  return <Card><CardContent><Stack spacing={2} maxWidth={720}><Box><Typography variant="h6" fontWeight={800}>{title}</Typography><Typography color="text.secondary">{description}</Typography></Box>{children}</Stack></CardContent></Card>
}
