import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { BellRing, Pencil, Plus, Send, Trash2 } from 'lucide-react'
import { useState } from 'react'
import type { Channel, ChannelDraft, ChannelType } from '../shared/api/types'
import { queries, queryKeys } from '../shared/api/query'
import { resources } from '../shared/api/resources'
import { useSession } from '../modules/session'
import { apiErrorMessage } from '../shared/api/errors'
import { Alert, Badge, Button, Card, EmptyState, LoadingState, PageError, PageHeader, SelectField, SwitchField, TextField, useNotify } from '../shared/ui'
import { Dialog } from '../shared/ui/Dialog'
import { channelTypeLabel, settingLabel } from '../shared/lib/presentation'

export function ChannelsPage() {
  const channels = useQuery(queries.channels()); const logins = useQuery(queries.microsoftLogins()); const { csrf } = useSession(); const client = useQueryClient(); const notify = useNotify()
  const [editing, setEditing] = useState<Channel | null | undefined>(undefined); const [removing, setRemoving] = useState<Channel | null>(null)
  const [authLinks, setAuthLinks] = useState<Record<string, string>>({})
  const refresh = async () => { await client.invalidateQueries({ queryKey: queryKeys.channels }); void client.invalidateQueries({ queryKey: queryKeys.runtime }) }
  const save = useMutation({ mutationFn: (draft: ChannelDraft) => draft.id ? resources.updateChannel(csrf, draft as ChannelDraft & { id: string }) : resources.createChannel(csrf, draft), onMutate: () => client.cancelQueries({ queryKey: queryKeys.channels }), onSuccess: async () => { await refresh(); setEditing(undefined) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const remove = useMutation({ mutationFn: (id: string) => resources.deleteChannel(csrf, id), onMutate: () => client.cancelQueries({ queryKey: queryKeys.channels }), onSuccess: async () => { await refresh(); setRemoving(null) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const test = useMutation({ mutationFn: (id: string) => resources.testChannel(csrf, id), onSuccess: () => notify('测试通知已发送', 'success'), onError: error => notify(apiErrorMessage(error), 'danger') })
  const authorize = useMutation({
    mutationFn: (id: string) => resources.startMicrosoftLogin(csrf, id),
    onSuccess: login => {
      void client.invalidateQueries({ queryKey: queryKeys.microsoftLogins })
      const url = login.verification_uri_complete || login.verification_uri
      if (!url) return
      const opened = window.open(url, '_blank', 'noopener,noreferrer')
      if (!opened) {
        setAuthLinks(current => ({ ...current, [login.channel_id]: url }))
        notify('无法自动打开验证页，请点击页面上的链接完成授权', 'info')
      } else {
        setAuthLinks(current => {
          const next = { ...current }
          delete next[login.channel_id]
          return next
        })
      }
    },
    onError: error => notify(apiErrorMessage(error), 'danger'),
  })
  const cancelAuthorization = useMutation({
    mutationFn: (id: string) => resources.cancelMicrosoftLogin(csrf, id),
    onSuccess: (_value, id) => {
      setAuthLinks(current => {
        const next = { ...current }
        delete next[id]
        return next
      })
      void client.invalidateQueries({ queryKey: queryKeys.microsoftLogins })
    },
    onError: error => notify(apiErrorMessage(error), 'danger'),
  })
  const copyCode = async (code: string) => {
    try {
      await navigator.clipboard.writeText(code)
      notify('设备码已复制', 'success')
    } catch {
      notify('复制失败，请手动选择设备码', 'danger')
    }
  }
  if (channels.isPending || logins.isPending) return <LoadingState />
  if (channels.error || logins.error) return <PageError error={channels.error || logins.error} retry={() => { void channels.refetch(); void logins.refetch() }} />
  return <div className="page-stack"><PageHeader title="通知渠道" subtitle="秘密字段只写入服务端保险库，不会返回浏览器。" action={<Button variant="primary" onPress={() => setEditing(null)}><Plus aria-hidden="true" />添加渠道</Button>} />
    {channels.data.length === 0 ? <EmptyState icon={<BellRing />} title="尚未配置通知渠道" action={<Button variant="primary" onPress={() => setEditing(null)}>添加第一个渠道</Button>} /> : <div className="card-grid">{channels.data.map(channel => {
      const login = logins.data.find(item => item.channel_id === channel.id)
      const authorized = channel.settings.authorized === 'true'
      const pending = login?.status === 'pending'
      const verificationURL = login?.verification_uri_complete || login?.verification_uri || authLinks[channel.id]
      return <Card key={channel.id}><div className="card-title"><div><h2>{channel.name}</h2><p>{channelTypeLabel(channel.type)}</p></div><Badge tone={channel.enabled ? 'success' : 'neutral'}>{channel.enabled ? '已启用' : '已停用'}</Badge></div>
        <dl className="summary-list">{Object.entries(channel.settings).filter(([key]) => !['authorized', 'token_type', 'token_expiry'].includes(key)).map(([key, value]) => <div key={key}><dt>{settingLabel(key)}</dt><dd>{value}</dd></div>)}{channel.configured_secrets.map(key => <div key={key}><dt>{settingLabel(key)}</dt><dd><Badge>已安全保存</Badge></dd></div>)}</dl>
        {channel.type === 'microsoft' && <Alert tone={authorized ? 'success' : pending ? 'info' : 'warning'}>{pending ? <div className="form-stack">
          <p>在 Microsoft 登录页输入设备码完成授权：</p>
          <p className="button-row"><strong style={{ fontSize: '1.25rem', letterSpacing: '0.08em' }}>{login?.user_code}</strong><Button variant="outline" onPress={() => login?.user_code && void copyCode(login.user_code)}>复制</Button></p>
          {verificationURL && <p><a href={verificationURL} target="_blank" rel="noopener noreferrer">打开验证页面</a></p>}
          <div className="button-row"><Button onPress={() => cancelAuthorization.mutate(channel.id)}>取消</Button></div>
        </div> : <div className="form-stack">
          <p>{login?.error || (authorized ? 'Microsoft 账户已授权。' : '必须完成 Microsoft 授权后才能启用。')} <Button onPress={() => authorize.mutate(channel.id)}>{authorized ? '重新授权' : '开始授权'}</Button></p>
          {authLinks[channel.id] && <p><a href={authLinks[channel.id]} target="_blank" rel="noopener noreferrer">打开验证页面</a></p>}
        </div>}</Alert>}
        <div className="button-row"><Button onPress={() => setEditing(channel)}><Pencil aria-hidden="true" />编辑</Button><Button onPress={() => test.mutate(channel.id)}><Send aria-hidden="true" />发送测试</Button><Button danger onPress={() => setRemoving(channel)}><Trash2 aria-hidden="true" />删除</Button></div>
      </Card>
    })}</div>}
    {editing !== undefined && <ChannelDialog key={editing?.id || 'new'} channel={editing || undefined} busy={save.isPending} error={save.error ? apiErrorMessage(save.error) : ''} onClose={() => setEditing(undefined)} onSave={draft => save.mutate(draft)} />}
    <Dialog open={Boolean(removing)} title="删除通知渠道" onClose={() => setRemoving(null)} actions={<><Button onPress={() => setRemoving(null)}>取消</Button><Button variant="primary" danger busy={remove.isPending} onPress={() => removing && remove.mutate(removing.id)}>确认删除</Button></>}><p>存在待投递任务时服务端会拒绝删除“{removing?.name}”。</p></Dialog>
  </div>
}

function ChannelDialog({ channel, busy, error, onClose, onSave }: { channel?: Channel; busy: boolean; error: string; onClose: () => void; onSave: (draft: ChannelDraft) => void }) {
  const [name, setName] = useState(channel?.name || ''); const [type, setType] = useState<ChannelType>(channel?.type || 'email'); const [enabled, setEnabled] = useState(channel?.enabled ?? true); const [fields, setFields] = useState<Record<string, string>>(channel?.settings || {}); const [secrets, setSecrets] = useState<Record<string, string>>({})
  const field = (key: string, value: string) => setFields(current => ({ ...current, [key]: value })); const secret = (key: string, value: string) => setSecrets(current => ({ ...current, [key]: value }))
  const submit = () => { if (!name) return; onSave(buildDraft(channel?.id, name, type, enabled, fields, secrets)) }
  return <Dialog open onClose={onClose} title={channel ? '编辑通知渠道' : '添加通知渠道'} actions={<><Button onPress={onClose}>取消</Button><Button variant="primary" busy={busy} isDisabled={!name} onPress={submit}>保存</Button></>}>
    <form className="form-stack" onSubmit={event => { event.preventDefault(); submit() }}>
      {error && <Alert tone="danger">{error}</Alert>}
      <TextField label="渠道名称" value={name} onChange={setName} required />
      <SelectField label="渠道类型" value={type} disabled={Boolean(channel)} onChange={value => { setType(value as ChannelType); setFields({}); setSecrets({}); if (value === 'microsoft') setEnabled(false) }} options={(['email', 'microsoft', 'dingtalk', 'feishu', 'wecom'] as ChannelType[]).map(value => ({ value, label: channelTypeLabel(value) }))} />
      {type === 'email' && <EmailFields fields={fields} secrets={secrets} configured={channel?.configured_secrets || []} setField={field} setSecret={secret} />}
      {type === 'microsoft' && <MicrosoftFields fields={fields} setField={field} />}
      {type === 'dingtalk' && <DingTalkFields secrets={secrets} configured={channel?.configured_secrets || []} setSecret={secret} />}
      {type === 'feishu' && <FeishuFields fields={fields} secrets={secrets} configured={channel?.configured_secrets || []} setField={field} setSecret={secret} />}
      {type === 'wecom' && <SecretField label="Webhook URL" name="webhook" values={secrets} configured={channel?.configured_secrets || []} onChange={secret} />}
      <SwitchField checked={enabled} onChange={setEnabled}>启用渠道</SwitchField>
      {type === 'microsoft' && <Alert>保存后需要完成 Microsoft 设备码授权，再启用渠道。</Alert>}
      <button type="submit" hidden tabIndex={-1} aria-hidden="true" />
    </form>
  </Dialog>
}

type FieldSetter = (key: string, value: string) => void
function EmailFields({ fields, secrets, configured, setField, setSecret }: { fields: Record<string, string>; secrets: Record<string, string>; configured: string[]; setField: FieldSetter; setSecret: FieldSetter }) { return <><TextField label="SMTP 主机" value={fields.host || ''} onChange={value => setField('host', value)} required /><TextField label="端口" value={fields.port || '465'} onChange={value => setField('port', value)} required inputMode="numeric" /><SelectField label="TLS 模式" value={fields.tls || 'tls'} onChange={value => setField('tls', value)} options={[{ value: 'tls', label: 'TLS' }, { value: 'starttls', label: 'STARTTLS' }]} /><TextField label="用户名" value={fields.username || ''} onChange={value => setField('username', value)} /><SecretField label="密码" name="password" values={secrets} configured={configured} onChange={setSecret} /><TextField label="发件人" value={fields.from || ''} onChange={value => setField('from', value)} required /><TextField label="收件人" value={fields.to || ''} onChange={value => setField('to', value)} required description="多个地址使用英文逗号分隔" /></> }
function MicrosoftFields({ fields, setField }: { fields: Record<string, string>; setField: FieldSetter }) { return <><TextField label="应用程序（客户端）ID" value={fields.client_id || ''} onChange={value => setField('client_id', value)} required /><TextField label="租户" value={fields.tenant || 'common'} onChange={value => setField('tenant', value)} /><TextField label="收件人" value={fields.to || ''} onChange={value => setField('to', value)} required /></> }
function DingTalkFields({ secrets, configured, setSecret }: { secrets: Record<string, string>; configured: string[]; setSecret: FieldSetter }) { return <><SecretField label="Webhook URL" name="webhook" values={secrets} configured={configured} onChange={setSecret} /><SecretField label="签名密钥" name="secret" values={secrets} configured={configured} onChange={setSecret} /></> }
function FeishuFields({ fields, secrets, configured, setField, setSecret }: { fields: Record<string, string>; secrets: Record<string, string>; configured: string[]; setField: FieldSetter; setSecret: FieldSetter }) { return <><SecretField label="Webhook URL" name="webhook" values={secrets} configured={configured} onChange={setSecret} /><SecretField label="签名密钥" name="secret" values={secrets} configured={configured} onChange={setSecret} /><TextField label="应用 App ID" value={fields.app_id || ''} onChange={value => setField('app_id', value)} /><SecretField label="应用 App Secret" name="app_secret" values={secrets} configured={configured} onChange={setSecret} /></> }
function SecretField({ label, name, values, configured, onChange }: { label: string; name: string; values: Record<string, string>; configured: string[]; onChange: FieldSetter }) { return <TextField label={label} type="password" value={values[name] || ''} onChange={value => onChange(name, value)} required={!configured.includes(name)} description={configured.includes(name) ? '已安全保存；留空表示保留原值' : undefined} /> }

function buildDraft(id: string | undefined, name: string, type: ChannelType, enabled: boolean, fields: Record<string, string>, secrets: Record<string, string>): ChannelDraft {
  const changed = Object.fromEntries(Object.entries(secrets).filter(([, value]) => value))
  if (type === 'email') return { id, name, type, enabled, settings: { host: fields.host || '', port: fields.port || '465', tls: fields.tls || 'tls', username: fields.username || '', from: fields.from || '', to: fields.to || '' }, ...(Object.keys(changed).length ? { secrets: changed } : {}) }
  if (type === 'microsoft') return { id, name, type, enabled, settings: { client_id: fields.client_id || '', tenant: fields.tenant || 'common', to: fields.to || '' } }
  if (type === 'dingtalk') return { id, name, type, enabled, settings: {}, ...(Object.keys(changed).length ? { secrets: changed } : {}) }
  if (type === 'feishu') return { id, name, type, enabled, settings: { app_id: fields.app_id || '' }, ...(Object.keys(changed).length ? { secrets: changed } : {}) }
  return { id, name, type, enabled, settings: {}, ...(Object.keys(changed).length ? { secrets: changed } : {}) }
}
