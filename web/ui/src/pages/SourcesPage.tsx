import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Database, Pencil, Plus, Trash2 } from 'lucide-react'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { Source } from '../shared/api/types'
import { queries, queryKeys } from '../shared/api/query'
import { resources } from '../shared/api/resources'
import { useSession } from '../modules/session'
import { apiErrorMessage } from '../shared/api/errors'
import { Alert, Badge, Button, Card, EmptyState, LoadingState, PageError, PageHeader, SelectField, SwitchField, TextField, useNotify } from '../shared/ui'
import { Dialog } from '../shared/ui/Dialog'
import { accountStatusLabel, formatDate } from '../shared/lib/presentation'

export function SourcesPage() {
  const sources = useQuery(queries.sources()); const accounts = useQuery(queries.accounts()); const runtime = useQuery(queries.runtime())
  const { csrf } = useSession(); const client = useQueryClient(); const notify = useNotify()
  const [editing, setEditing] = useState<Source | null | undefined>(); const [removing, setRemoving] = useState<Source | null>(null)
  const [confirmZSXQLogout, setConfirmZSXQLogout] = useState(false)
  const refresh = async () => { await Promise.all([client.invalidateQueries({ queryKey: ['sources'] }), client.invalidateQueries({ queryKey: queryKeys.accounts }), client.invalidateQueries({ queryKey: queryKeys.runtime })]) }
  const save = useMutation({ mutationFn: (input: SourceDraft) => editing ? resources.updateSource(csrf, { id: editing.id, name: input.name, note: input.note, enabled: input.enabled }) : resources.createSource(csrf, { platform: input.platform, type: input.platform === 'zsxq' ? 'planet' : 'up', external_id: input.externalID, name: input.name, note: input.note, enabled: input.enabled }), onSuccess: async () => { await refresh(); setEditing(undefined) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const remove = useMutation({ mutationFn: (id: string) => resources.deleteSource(csrf, id), onSuccess: async () => { await refresh(); setRemoving(null) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const logoutZSXQ = useMutation({
    mutationFn: () => resources.deleteZSXQSession(csrf),
    onSuccess: async () => { setConfirmZSXQLogout(false); notify('已退出知识星球登录', 'success'); await refresh() },
    onError: error => notify(apiErrorMessage(error), 'danger'),
  })
  if (sources.isPending || accounts.isPending) return <LoadingState />
  if (sources.error || accounts.error) return <PageError error={sources.error || accounts.error} retry={() => { void sources.refetch(); void accounts.refetch() }} />
  const bili = sources.data.filter(item => item.platform === 'bilibili'); const planets = sources.data.filter(item => item.platform === 'zsxq')
  const zsxqAccount = accounts.data.find(item => item.platform === 'zsxq')
  const timeZone = runtime.data?.timezone || ''
  return <div className="page-stack"><PageHeader title="采集源" subtitle="平台账号只提供当前访问凭证；来源独立决定需要归档哪些内容。首次启用只建立历史基线，不发送旧内容通知。" action={<Button variant="primary" onPress={() => setEditing(null)}><Plus aria-hidden="true" />添加采集源</Button>} />
    <Card>
      <div className="card-title">
        <div>
          <h2>知识星球账号</h2>
          <p>{zsxqAccount?.status === 'connected' ? `${zsxqAccount.display_name || ''} ${zsxqAccount.masked_phone || ''}` : '尚未连接'}</p>
        </div>
        <Badge tone={zsxqAccount?.status === 'connected' ? 'success' : 'warning'}>{accountStatusLabel(zsxqAccount?.status)}</Badge>
      </div>
      {zsxqAccount?.last_error && <Alert tone="danger">{zsxqAccount.last_error}</Alert>}
      <div className="button-row">
        <Link className="button button--primary" to="/integrations/zsxq-login">导入 Session</Link>
        {zsxqAccount?.status === 'connected' && <Button danger onPress={() => setConfirmZSXQLogout(true)}>退出登录</Button>}
      </div>
    </Card>
    <SourceSection title="B 站" empty="尚未添加 B 站 UP" emptyAction={<Button variant="primary" onPress={() => setEditing(null)}>添加采集源</Button>} sources={bili} edit={setEditing} remove={setRemoving} timeZone={timeZone} />
    <SourceSection title="知识星球" empty="尚未手动添加知识星球" emptyAction={<Button variant="primary" onPress={() => setEditing(null)}>添加采集源</Button>} sources={planets} edit={setEditing} remove={setRemoving} timeZone={timeZone} />
    {editing !== undefined && <SourceDialog value={editing || undefined} busy={save.isPending} error={save.error ? apiErrorMessage(save.error) : ''} onClose={() => setEditing(undefined)} onSave={value => save.mutate(value)} />}
    <Dialog open={Boolean(removing)} title="删除采集源" onClose={() => setRemoving(null)} actions={<><Button onPress={() => setRemoving(null)}>取消</Button><Button variant="primary" danger busy={remove.isPending} onPress={() => removing && remove.mutate(removing.id)}>删除采集源</Button></>}><p>会取消未投递任务并删除该来源的内容、评论与本地附件。需要再次采集时必须手动重新添加。</p></Dialog>
    <Dialog open={confirmZSXQLogout} title="退出知识星球登录" onClose={() => setConfirmZSXQLogout(false)} actions={<><Button onPress={() => setConfirmZSXQLogout(false)}>取消</Button><Button variant="primary" danger busy={logoutZSXQ.isPending} onPress={() => logoutZSXQ.mutate()}>确认退出</Button></>}><p>退出后已启用的知识星球采集将暂停，直到重新登录。本地已归档内容不会删除。</p></Dialog>
  </div>
}

function SourceSection({ title, empty, emptyAction, sources, edit, remove, timeZone }: { title: string; empty: string; emptyAction?: React.ReactNode; sources: Source[]; edit: (source: Source) => void; remove: (source: Source) => void; timeZone: string }) {
  return <section className="page-stack"><h2>{title}</h2>{sources.length === 0 ? <EmptyState icon={<Database />} title={empty} action={emptyAction} /> : <div className="card-grid">{sources.map(source => <Card key={source.id}><div className="card-title"><div><h2>{source.note || source.name}</h2><p>{source.name} · {source.external_id}</p></div><Badge tone={source.enabled ? 'success' : 'neutral'}>{source.enabled ? '已启用' : '已停用'}</Badge></div><div className="badge-row"><Badge>{baselineLabel(source.baseline_state)}</Badge>{source.owner_name && <Badge tone="info">星主 {source.owner_name}</Badge>}<Badge>已回补 {source.backfill_done}</Badge>{source.consecutive_fails > 0 && <Badge tone="danger">连续失败 {source.consecutive_fails}</Badge>}</div>{source.last_success_at && <p className="muted">最近成功 {formatDate(source.last_success_at, timeZone)}</p>}{source.last_error && <Alert tone="danger">{source.last_error}</Alert>}<div className="button-row"><Button onPress={() => edit(source)}><Pencil aria-hidden="true" />编辑</Button><Button danger onPress={() => remove(source)}><Trash2 aria-hidden="true" />删除</Button></div></Card>)}</div>}</section>
}

type SourceDraft = { platform: 'bilibili' | 'zsxq'; externalID: string; name: string; note: string; enabled: boolean }

function SourceDialog({ value, busy, error, onClose, onSave }: { value?: Source; busy: boolean; error: string; onClose: () => void; onSave: (value: SourceDraft) => void }) {
  const [platform, setPlatform] = useState<'bilibili' | 'zsxq'>(value?.platform || 'bilibili'); const [externalID, setExternalID] = useState(value?.external_id || ''); const [name, setName] = useState(value?.name || ''); const [note, setNote] = useState(value?.note || ''); const [enabled, setEnabled] = useState(value?.enabled ?? true)
  const idLabel = platform === 'zsxq' ? '星球 ID' : 'UID'
  return <Dialog open onClose={onClose} title={value ? '编辑采集源' : '添加采集源'} actions={<><Button onPress={onClose}>取消</Button><Button variant="primary" busy={busy} isDisabled={!externalID.trim() || !name.trim()} onPress={() => onSave({ platform, externalID, name, note, enabled })}>保存</Button></>}><div className="form-stack">{error && <Alert tone="danger">{error}</Alert>}{!value && <SelectField label="平台" value={platform} onChange={selected => setPlatform(selected as 'bilibili' | 'zsxq')} options={[{ value: 'bilibili', label: 'B 站 UP' }, { value: 'zsxq', label: '知识星球' }]} />}<TextField label={idLabel} value={externalID} onChange={setExternalID} disabled={Boolean(value)} required inputMode="numeric" /><TextField label="来源名称" value={name} onChange={setName} required />{platform === 'zsxq' && !value && <p className="muted">直接保存星球 ID，不检查当前 Session 是否有权访问；采集失败会显示在该来源上。</p>}<TextField label="备注" value={note} onChange={setNote} /><SwitchField checked={enabled} onChange={setEnabled}>启用采集</SwitchField></div></Dialog>
}

function baselineLabel(state: Source['baseline_state']) { return ({ pending: '等待基线', running: '历史回补中', complete: '基线完成', failed: '回补失败' })[state] }
