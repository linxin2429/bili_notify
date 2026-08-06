import { useEffect, useState } from 'react'
import Add from '@mui/icons-material/Add'
import Delete from '@mui/icons-material/Delete'
import Edit from '@mui/icons-material/Edit'
import Email from '@mui/icons-material/Email'
import NotificationsActive from '@mui/icons-material/NotificationsActive'
import Science from '@mui/icons-material/Science'
import { Alert, Avatar, Box, Button, Card, CardContent, Chip, Dialog, DialogActions, DialogContent, DialogTitle, Divider, FormControl, FormControlLabel, InputLabel, MenuItem, Select, Stack, Switch, TextField, Typography, useMediaQuery } from '@mui/material'
import type { AdminAPI } from '../api'
import { applyChannelDeletion, applyChannelMutation, applyMicrosoftLoginDeletion, applyMicrosoftLoginMutation } from '../dashboard'
import { channelTypeLabel, settingLabel } from '../presentation'
import type { Channel, ChannelDraft, ChannelType, MicrosoftLogin } from '../types'
import { EmptyState, PageHeader, type RunMutation } from '../app/shared'
import { channelFields } from './channel-form'

export function ChannelsPage({ channels, logins, api, runMutation }: { channels: Channel[]; logins: MicrosoftLogin[]; api: AdminAPI; runMutation: RunMutation }) {
  const [editing, setEditing] = useState<Channel | null | undefined>(undefined)
  const mobile = useMediaQuery(theme => theme.breakpoints.down('sm'))
  const save = async (draft: ChannelDraft) => {
    await runMutation(() => draft.id ? api.updateChannel(draft as ChannelDraft & { id: string }) : api.createChannel(draft), applyChannelMutation)
    setEditing(undefined)
  }
  const remove = async (id: string) => {
    if (!confirm('存在待投递任务时不能删除渠道。继续？')) return
    try { await runMutation(() => api.deleteChannel(id), current => applyChannelDeletion(current, id)) } catch { /* shared handler reports it */ }
  }
  const authorize = async (channelID: string) => {
    try {
      const login = await runMutation(() => api.startMicrosoftLogin(channelID), applyMicrosoftLoginMutation)
      const url = login.verification_uri_complete || login.verification_uri
      if (url) window.open(url, '_blank', 'noopener,noreferrer')
    } catch { /* shared handler reports it */ }
  }
  const cancelAuthorization = async (channelID: string) => {
    try { await runMutation(() => api.cancelMicrosoftLogin(channelID), current => applyMicrosoftLoginDeletion(current, channelID)) } catch { /* shared handler reports it */ }
  }
  const test = async (channelID: string) => {
    try { await runMutation(() => api.testChannel(channelID)) } catch { /* shared handler reports it */ }
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
    try { await onSave({ id: channel?.id, name, type, enabled, settings, ...(Object.keys(changedSecrets).length ? { secrets: changedSecrets } : {}) }) } catch (err) { setError(err instanceof Error ? err.message : '发生未知错误') } finally { setBusy(false) }
  }
  return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm"><DialogTitle>{channel ? '编辑通知渠道' : '添加通知渠道'}</DialogTitle><DialogContent><Stack spacing={2} sx={{ pt: 1 }}>{error && <Alert severity="error">{error}</Alert>}<TextField label="渠道名称" value={name} onChange={event => setName(event.target.value)} required /><FormControl><InputLabel id="channel-type-label">渠道类型</InputLabel><Select labelId="channel-type-label" label="渠道类型" value={type} onChange={event => { setType(event.target.value as ChannelType); setFields({}); setSecrets({}) }}>{(['email', 'microsoft', 'dingtalk', 'feishu', 'wecom'] as ChannelType[]).map(value => <MenuItem key={value} value={value}>{channelTypeLabel(value)}</MenuItem>)}</Select></FormControl>{channelFields(type).map(field => <TextField key={field.key} label={field.label} type={field.secret ? 'password' : 'text'} value={field.secret ? secrets[field.key] || '' : fields[field.key] || field.defaultValue || ''} onChange={event => field.secret ? setSecret(field.key, event.target.value) : setField(field.key, event.target.value)} required={field.required && !(channel?.configured_secrets.includes(field.key))} helperText={field.secret && channel?.configured_secrets.includes(field.key) ? '已安全保存；留空表示保留原值' : field.help} />)}<FormControlLabel control={<Switch checked={enabled} onChange={event => setEnabled(event.target.checked)} />} label="启用渠道" />{type === 'microsoft' && <Alert severity="info">保存后需要完成 Microsoft 设备码授权，再启用渠道。</Alert>}</Stack></DialogContent><DialogActions><Button onClick={onClose}>取消</Button><Button variant="contained" disabled={busy || !name} onClick={() => void submit()}>保存</Button></DialogActions></Dialog>
}
