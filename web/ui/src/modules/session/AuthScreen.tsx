import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { sessionAPI } from '../../shared/api/session'
import { apiErrorMessage } from '../../shared/api/errors'
import { replaceSessionState } from '../../shared/api/session-cache'
import { Alert, Button, Card, TextField } from '../../shared/ui'

export function AuthScreen({ setup }: { setup: boolean }) {
  const client = useQueryClient()
  const [code, setCode] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm] = useState('')
  const [validation, setValidation] = useState('')
  const mutation = useMutation({
    mutationFn: () => setup ? sessionAPI.setup(code, password) : sessionAPI.login(password),
    onSuccess: state => replaceSessionState(client, { setup_required: false, authenticated: true, csrf_token: state.csrf_token }),
  })
  const submit = () => {
    if (setup && password !== confirm) { setValidation('两次输入的密码不一致'); return }
    setValidation('')
    mutation.mutate()
  }
  return <main className="auth-screen"><Card className="auth-card"><header className="brand"><span className="brand__mark">BN</span><div><h1>Bili Notify</h1><p>{setup ? '完成安全初始化' : '登录实时管理台'}</p></div></header>
    {setup && <Alert>初始化码只会输出到本次容器启动日志，设置成功后立即失效。</Alert>}
    <form className="form-stack" onSubmit={event => { event.preventDefault(); submit() }}>
      {setup && <TextField label="初始化码" value={code} onChange={value => setCode(value.toUpperCase())} required autoComplete="one-time-code" />}
      <TextField label={setup ? '设置管理员密码' : '管理员密码'} type="password" value={password} onChange={setPassword} required autoComplete={setup ? 'new-password' : 'current-password'} description={setup ? '至少 12 个字符' : undefined} />
      {setup && <TextField label="确认密码" type="password" value={confirm} onChange={setConfirm} required autoComplete="new-password" />}
      {(validation || mutation.error) && <Alert tone="danger">{validation || apiErrorMessage(mutation.error)}</Alert>}
      <Button type="submit" variant="primary" isDisabled={!password || (setup && !code)} busy={mutation.isPending}>{setup ? '初始化并登录' : '登录'}</Button>
    </form>
  </Card></main>
}
