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
  const [apiKey, setAPIKey] = useState(''); const [error, setError] = useState('')
  const login = useMutation({
    mutationFn: () => resources.updateZSXQCredential(csrf, apiKey),
    onSuccess: async () => {
      setAPIKey('')
      await client.invalidateQueries({ queryKey: queryKeys.accounts })
      notify('知识星球密钥连接成功', 'success')
      void navigate('/sources')
    },
    onError: value => setError(value && typeof value === 'object' && 'fields' in value && (value as { fields?: Record<string, string> }).fields?.api_key || apiErrorMessage(value)),
  })

  return <div className="page-stack"><PageHeader title="知识星球密钥连接" subtitle="通过知识星球官方 MCP 读取内容。密钥会加密保存，更新密钥不会修改现有采集源、历史档案或同步水位。" />
    <Card><div className="form-stack"><ol><li>打开 <a href="https://garden.zsxq.com/jasmine/" target="_blank" rel="noreferrer">Jasmine 密钥管理</a>。</li><li>创建一个密钥并立即复制；密钥可随时在 Jasmine 撤销。</li><li>将密钥粘贴到下方，保存前会通过官方 MCP 验证当前用户。</li></ol>{error && <Alert tone="danger">{error}</Alert>}<TextField label="Jasmine API 密钥" value={apiKey} onChange={value => { setAPIKey(value); setError('') }} type="password" autoComplete="off" required /><Button variant="primary" busy={login.isPending} isDisabled={!apiKey.trim()} onPress={() => login.mutate()}>连接或更新密钥</Button></div></Card>
  </div>
}
