import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import type { Source } from '../shared/api/types'
import { queries, queryKeys } from '../shared/api/query'
import { resources } from '../shared/api/resources'
import { useSession } from '../modules/session'
import { apiErrorMessage } from '../shared/api/errors'
import { Alert, Badge, Button, Card, EmptyState, LoadingState, PageError, PageHeader, SwitchField, TextField, useNotify } from '../shared/ui'
import { Dialog } from '../shared/ui/Dialog'

export function SourcesPage() {
  const sources = useQuery(queries.sources()); const accounts = useQuery(queries.accounts()); const { csrf } = useSession(); const client = useQueryClient(); const notify = useNotify()
  const [editing, setEditing] = useState<Source | null | undefined>(); const [removing, setRemoving] = useState<Source | null>(null)
  const refresh = async () => { await Promise.all([client.invalidateQueries({ queryKey: ['sources'] }), client.invalidateQueries({ queryKey: queryKeys.accounts }), client.invalidateQueries({ queryKey: queryKeys.runtime })]) }
  const save = useMutation({ mutationFn: (input: { uid: string; name: string; note: string; enabled: boolean }) => editing ? resources.updateSource(csrf, { id: editing.id, name: input.name, note: input.note, enabled: input.enabled }) : resources.createSource(csrf, { platform: 'bilibili', external_id: input.uid, name: input.name, note: input.note, enabled: input.enabled }), onSuccess: async () => { await refresh(); setEditing(undefined) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const remove = useMutation({ mutationFn: (id: string) => resources.deleteSource(csrf, id), onSuccess: async () => { await refresh(); setRemoving(null) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const sync = useMutation({ mutationFn: () => resources.syncZSXQSources(csrf), onSuccess: refresh, onError: error => notify(apiErrorMessage(error), 'danger') })
  if (sources.isPending || accounts.isPending) return <LoadingState />
  if (sources.error || accounts.error) return <PageError error={sources.error || accounts.error} retry={() => { void sources.refetch(); void accounts.refetch() }} />
  const bili = sources.data.filter(item => item.platform === 'bilibili'); const planets = sources.data.filter(item => item.platform === 'zsxq')
  const zsxqAccount = accounts.data.find(item => item.platform === 'zsxq')
  return <div className="page-stack"><PageHeader title="采集源" subtitle="平台账号决定访问权限；来源决定需要归档哪些内容。首次启用只建立历史基线，不发送旧内容通知。" action={<Button variant="primary" onPress={() => setEditing(null)}>＋ 添加 B 站 UP</Button>} />
    <Card><div className="card-title"><div><h2>知识星球账号</h2><p>{zsxqAccount?.status === 'connected' ? `${zsxqAccount.display_name || ''} ${zsxqAccount.masked_phone || ''}` : '尚未连接'}</p></div><Badge tone={zsxqAccount?.status === 'connected' ? 'success' : 'warning'}>{zsxqAccount?.status || 'disconnected'}</Badge></div>{zsxqAccount?.last_error && <Alert tone="danger">{zsxqAccount.last_error}</Alert>}<div className="button-row"><Button variant="primary"><Link to="/integrations/zsxq-login">手机号验证码登录</Link></Button><Button busy={sync.isPending} isDisabled={zsxqAccount?.status !== 'connected'} onPress={() => sync.mutate()}>刷新可见星球</Button></div></Card>
    <SourceSection title="B 站" empty="尚未添加 B 站 UP" sources={bili} edit={setEditing} remove={setRemoving} />
    <SourceSection title="知识星球" empty="登录并刷新后，可见星球会显示在这里" sources={planets} edit={setEditing} remove={setRemoving} />
    {editing !== undefined && <SourceDialog value={editing || undefined} busy={save.isPending} error={save.error ? apiErrorMessage(save.error) : ''} onClose={() => setEditing(undefined)} onSave={value => save.mutate(value)} />}
    <Dialog open={Boolean(removing)} title="删除采集源" onClose={() => setRemoving(null)} actions={<><Button onPress={() => setRemoving(null)}>取消</Button><Button variant="primary" danger busy={remove.isPending} onPress={() => removing && remove.mutate(removing.id)}>删除归档</Button></>}><p>会取消未投递任务并删除该来源的内容、评论与本地附件。知识星球来源下次同步账号后会以停用状态重新出现。</p></Dialog>
  </div>
}

function SourceSection({ title, empty, sources, edit, remove }: { title: string; empty: string; sources: Source[]; edit: (source: Source) => void; remove: (source: Source) => void }) {
  return <section className="page-stack"><h2>{title}</h2>{sources.length === 0 ? <EmptyState icon="◎" title={empty} /> : <div className="card-grid">{sources.map(source => <Card key={source.id}><div className="card-title"><div><h2>{source.note || source.name}</h2><p>{source.name} · {source.external_id}</p></div><Badge tone={source.enabled ? 'success' : 'neutral'}>{source.enabled ? '已启用' : '已停用'}</Badge></div><div className="badge-row"><Badge>{baselineLabel(source.baseline_state)}</Badge>{source.owner_name && <Badge tone="info">星主 {source.owner_name}</Badge>}<Badge>已回补 {source.backfill_done}</Badge></div>{source.last_error && <Alert tone="danger">{source.last_error}</Alert>}<div className="button-row"><Button onPress={() => edit(source)}>✎ 编辑</Button><Button danger onPress={() => remove(source)}>⌫ 删除</Button></div></Card>)}</div>}</section>
}

function SourceDialog({ value, busy, error, onClose, onSave }: { value?: Source; busy: boolean; error: string; onClose: () => void; onSave: (value: { uid: string; name: string; note: string; enabled: boolean }) => void }) {
  const [uid, setUID] = useState(value?.external_id || ''); const [name, setName] = useState(value?.name || ''); const [note, setNote] = useState(value?.note || ''); const [enabled, setEnabled] = useState(value?.enabled ?? true)
  return <Dialog open onClose={onClose} title={value ? '编辑采集源' : '添加 B 站 UP'} actions={<><Button onPress={onClose}>取消</Button><Button variant="primary" busy={busy} isDisabled={!uid} onPress={() => onSave({ uid, name, note, enabled })}>保存</Button></>}><div className="form-stack">{error && <Alert tone="danger">{error}</Alert>}<TextField label="UID" value={uid} onChange={setUID} disabled={Boolean(value)} required inputMode="numeric" /><TextField label="来源名称" value={name} onChange={setName} disabled={value?.platform === 'zsxq'} /><TextField label="备注" value={note} onChange={setNote} /><SwitchField checked={enabled} onChange={setEnabled}>启用采集</SwitchField></div></Dialog>
}

function baselineLabel(state: Source['baseline_state']) { return ({ pending: '等待基线', running: '历史回补中', complete: '基线完成', failed: '回补失败' })[state] }
