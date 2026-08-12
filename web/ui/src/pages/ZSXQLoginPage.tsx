import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { resources } from '../shared/api/resources'
import { queryKeys } from '../shared/api/query'
import { useSession } from '../modules/session'
import { apiErrorMessage } from '../shared/api/errors'
import { Alert, Button, Card, PageHeader, TextField, useNotify } from '../shared/ui'

export function ZSXQLoginPage() {
  const { csrf } = useSession(); const client = useQueryClient(); const navigate = useNavigate(); const notify = useNotify()
  const [cookie, setCookie] = useState(''); const [error, setError] = useState('')
  const login = useMutation({
    mutationFn: () => resources.importZSXQToken(csrf, cookie),
    onSuccess: async () => {
      setCookie('')
      await client.invalidateQueries({ queryKey: queryKeys.accounts })
      notify('知识星球 Session 导入成功', 'success')
      void navigate('/sources')
    },
    onError: value => setError(value && typeof value === 'object' && 'fields' in value && (value as { fields?: Record<string, string> }).fields?.cookie || apiErrorMessage(value)),
  })

  return <div className="page-stack"><PageHeader title="知识星球登录" subtitle="导入网页会话中的 access token。服务只加密保存 token，不保存完整 Cookie。" />
    <Card><div className="form-stack"><ol><li>在浏览器登录知识星球网页。</li><li>打开开发者工具的网络面板，选择任意 <code>api.zsxq.com</code> 请求。</li><li>复制请求头 <code>Cookie</code> 的完整值并粘贴到下方。</li></ol>{error && <Alert tone="danger">{error}</Alert>}<TextField label="Cookie 请求头值" value={cookie} onChange={value => { setCookie(value); setError('') }} type="password" autoComplete="off" required /><Button variant="primary" busy={login.isPending} isDisabled={!cookie.trim()} onPress={() => login.mutate()}>导入 Session</Button></div></Card>
  </div>
}
