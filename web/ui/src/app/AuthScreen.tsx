import { useState } from 'react'
import { Alert, Avatar, Box, Button, Card, CardContent, Stack, TextField, Typography } from '@mui/material'
import { httpJSON } from '../api'
import { csrfStateSchema } from '../contracts'
import { errorMessage } from '../presentation'

export function AuthScreen({ setup, onAuthenticated }: { setup: boolean; onAuthenticated: (state: { csrf_token: string }) => void }) {
  const [code, setCode] = useState(''); const [password, setPassword] = useState(''); const [confirm, setConfirm] = useState(''); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  const submit = async () => {
    if (setup && password !== confirm) { setError('两次输入的密码不一致'); return }
    setBusy(true); setError('')
    try { onAuthenticated(await httpJSON(setup ? '/api/v1/setup' : '/api/v1/session', csrfStateSchema, { method: 'POST', body: JSON.stringify(setup ? { setup_code: code, password } : { password }) })) } catch (err) { setError(errorMessage(err)) } finally { setBusy(false) }
  }
  return <Box minHeight="100vh" display="grid" sx={{ placeItems: 'center', p: 2, background: theme => theme.palette.mode === 'dark' ? 'radial-gradient(circle at top, #251925, #101116 46%)' : 'radial-gradient(circle at top, #fff0f5, #f6f7fb 48%)' }}><Card sx={{ width: '100%', maxWidth: 440 }}><CardContent sx={{ p: { xs: 3, sm: 5 } }}><Stack spacing={3}><Stack direction="row" spacing={2} alignItems="center"><Avatar sx={{ bgcolor: 'primary.main', width: 52, height: 52, fontWeight: 800 }}>BN</Avatar><Box><Typography variant="h5" fontWeight={800}>Bili Notify</Typography><Typography color="text.secondary">{setup ? '完成安全初始化' : '登录实时管理台'}</Typography></Box></Stack>{setup && <Alert severity="info">初始化码只会输出到本次容器启动日志。设置成功后立即失效。</Alert>}{setup && <TextField label="初始化码" value={code} onChange={event => setCode(event.target.value.toUpperCase())} autoComplete="one-time-code" fullWidth />}<TextField label={setup ? '设置管理员密码' : '管理员密码'} type="password" value={password} onChange={event => setPassword(event.target.value)} autoComplete={setup ? 'new-password' : 'current-password'} helperText={setup ? '至少 12 个字节' : undefined} fullWidth onKeyDown={event => { if (event.key === 'Enter' && !setup) void submit() }} />{setup && <TextField label="确认密码" type="password" value={confirm} onChange={event => setConfirm(event.target.value)} autoComplete="new-password" fullWidth onKeyDown={event => { if (event.key === 'Enter') void submit() }} />}{error && <Alert severity="error">{error}</Alert>}<Button variant="contained" size="large" disabled={busy || !password || (setup && !code)} onClick={() => void submit()}>{busy ? '处理中…' : setup ? '初始化并登录' : '登录'}</Button></Stack></CardContent></Card></Box>
}
