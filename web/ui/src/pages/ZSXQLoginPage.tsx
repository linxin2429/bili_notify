import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { resources } from '../shared/api/resources'
import { queryKeys } from '../shared/api/query'
import { useSession } from '../modules/session'
import { apiErrorMessage } from '../shared/api/errors'
import { Alert, Button, Card, PageHeader, SelectField, SwitchField, TextField, useNotify } from '../shared/ui'
import { knowledgePlanetCaptcha } from './zsxq-captcha'

const captcha = typeof window === 'undefined' ? undefined : (window.__zsxqCaptcha || knowledgePlanetCaptcha())
const RESEND_COOLDOWN_SEC = 60

export function ZSXQLoginPage() {
  const { csrf } = useSession(); const client = useQueryClient(); const navigate = useNavigate(); const notify = useNotify()
  const [country, setCountry] = useState('+86'); const [phone, setPhone] = useState(''); const [accepted, setAccepted] = useState(false)
  const [transaction, setTransaction] = useState(''); const [masked, setMasked] = useState(''); const [code, setCode] = useState(''); const [error, setError] = useState('')
  const [cooldown, setCooldown] = useState(0)

  useEffect(() => {
    if (cooldown <= 0) return
    const timer = window.setTimeout(() => setCooldown(value => value - 1), 1_000)
    return () => window.clearTimeout(timer)
  }, [cooldown])

  const send = useMutation({
    mutationFn: async () => {
      const verifier = window.__zsxqCaptcha || captcha
      if (!verifier) throw new Error('滑块验证组件不可用')
      const value = await verifier.verify()
      return resources.sendZSXQCode(csrf, { country_code: country, phone, captcha_verify_param: value, agreement_accepted: accepted })
    },
    onSuccess: value => { setTransaction(value.id); setMasked(value.masked_phone); setError(''); setCooldown(RESEND_COOLDOWN_SEC) },
    onError: value => setError(apiErrorMessage(value)),
  })
  const login = useMutation({
    mutationFn: () => resources.createZSXQSession(csrf, transaction, code),
    onSuccess: async () => {
      await Promise.all([client.invalidateQueries({ queryKey: queryKeys.accounts }), client.invalidateQueries({ queryKey: ['sources'] })])
      notify('知识星球登录成功', 'success')
      void navigate('/sources')
    },
    onError: value => setError(apiErrorMessage(value)),
  })
  const resetPhone = () => { setTransaction(''); setMasked(''); setCode(''); setError(''); setCooldown(0) }

  return <div className="page-stack"><PageHeader title="知识星球登录" subtitle="滑块结果、短信验证码、完整手机号和上游响应不会写入数据库或日志。登录事务 10 分钟后失效，最多提交 5 次验证码。" />
    <Card><div className="form-stack">{error && <Alert tone="danger">{error}</Alert>}{!transaction ? <><SelectField label="国家或地区码" value={country} onChange={setCountry} options={[{ value: '+86', label: '中国大陆 +86' }, { value: '+852', label: '中国香港 +852' }, { value: '+853', label: '中国澳门 +853' }, { value: '+886', label: '中国台湾 +886' }]} /><TextField label="手机号" value={phone} onChange={setPhone} inputMode="tel" required /><SwitchField checked={accepted} onChange={setAccepted}>我已阅读并同意<a href="https://support.zsxq.com/index.html" target="_blank" rel="noreferrer">《知识星球用户协议》</a>和<a href="https://support.zsxq.com/privacy.html" target="_blank" rel="noreferrer">《隐私政策》</a></SwitchField><div id="zsxq-captcha" aria-label="知识星球滑块验证" /><Button variant="primary" busy={send.isPending} isDisabled={!phone || !accepted} onPress={() => send.mutate()}>启动滑块并发送验证码</Button></> : <><p>验证码已发送至 {masked}</p><TextField label="短信验证码" value={code} onChange={setCode} inputMode="numeric" required /><div className="button-row"><Button variant="primary" busy={login.isPending} isDisabled={!code} onPress={() => login.mutate()}>登录并同步星球</Button><Button variant="outline" busy={send.isPending} isDisabled={cooldown > 0} onPress={() => send.mutate()}>{cooldown > 0 ? `重新发送（${cooldown}s）` : '重新发送'}</Button><Button onPress={resetPhone}>更换手机号</Button></div></>}</div></Card>
  </div>
}
