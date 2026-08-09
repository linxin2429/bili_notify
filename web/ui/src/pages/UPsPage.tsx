import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { UP } from '../shared/api/types'
import { queries, queryKeys } from '../shared/api/query'
import { resources } from '../shared/api/resources'
import { useSession } from '../modules/session'
import { apiErrorMessage } from '../shared/api/errors'
import { Alert, Badge, Button, Card, EmptyState, LoadingState, PageError, PageHeader, SwitchField, TextField, useNotify } from '../shared/ui'
import { Dialog } from '../shared/ui/Dialog'
import { followStateLabel, formatDate } from '../shared/lib/presentation'

export function UPsPage() {
  const ups = useQuery(queries.ups()); const runtime = useQuery(queries.runtime()); const { csrf } = useSession(); const client = useQueryClient(); const notify = useNotify()
  const [editing, setEditing] = useState<UP | null | undefined>(undefined); const [removing, setRemoving] = useState<UP | null>(null)
  const finish = async () => { await client.invalidateQueries({ queryKey: queryKeys.ups }); void client.invalidateQueries({ queryKey: queryKeys.runtime }) }
  const save = useMutation({ mutationFn: (value: Pick<UP, 'uid' | 'name' | 'enabled'>) => editing ? resources.updateUP(csrf, value) : resources.createUP(csrf, value), onMutate: () => client.cancelQueries({ queryKey: queryKeys.ups }), onSuccess: async () => { await finish(); setEditing(undefined) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const remove = useMutation({ mutationFn: (uid: string) => resources.deleteUP(csrf, uid), onMutate: () => client.cancelQueries({ queryKey: queryKeys.ups }), onSuccess: async () => { await finish(); setRemoving(null) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  if (ups.isPending || runtime.isPending) return <LoadingState />
  if (ups.error || runtime.error) return <PageError error={ups.error || runtime.error} retry={() => { void ups.refetch(); void runtime.refetch() }} />
  return <div className="page-stack"><PageHeader title="UP 主" subtitle="管理需要轮询的公开账号；首次采集只建立基线。" action={<Button variant="primary" onPress={() => setEditing(null)}>＋ 添加 UP 主</Button>} />
    {ups.data.length === 0 ? <EmptyState icon="♟" title="尚未添加 UP 主" action={<Button variant="primary" onPress={() => setEditing(null)}>添加第一个 UP 主</Button>} /> : <div className="card-grid">{ups.data.map(up => <Card key={up.uid}><div className="card-title"><div><h2>{up.name || `UID ${up.uid}`}</h2><p>UID {up.uid}</p></div><Badge tone={up.enabled ? 'success' : 'neutral'}>{up.enabled ? '已启用' : '已停用'}</Badge></div><div className="badge-row"><Badge>{up.baseline_ready ? '基线已建立' : '等待基线'}</Badge><Badge tone={up.follow_state === 'followed' ? 'success' : up.follow_state === 'unknown' ? 'warning' : 'neutral'}>{followStateLabel(up.follow_state)}</Badge><Badge tone="info">{up.collection_route === 'feed_all' ? '综合流采集' : '空间采集'}</Badge><Badge tone={up.consecutive_fail ? 'warning' : 'neutral'}>连续失败 {up.consecutive_fail} 次</Badge></div>{up.last_error && <Alert tone="danger">{up.last_error}</Alert>}<p className="muted">关注关系检查：{up.follow_checked_at ? formatDate(up.follow_checked_at, runtime.data.timezone) : '尚未检查'}</p><p className="muted">最后成功：{up.last_success_at ? formatDate(up.last_success_at, runtime.data.timezone) : '尚无记录'}</p><div className="button-row"><Button onPress={() => setEditing(up)}>✎ 编辑</Button><Button danger onPress={() => setRemoving(up)}>⌫ 删除</Button></div></Card>)}</div>}
    {editing !== undefined && <UPDialog key={editing?.uid || 'new'} value={editing || undefined} busy={save.isPending} error={save.error ? apiErrorMessage(save.error) : ''} onClose={() => setEditing(undefined)} onSave={value => save.mutate(value)} />}
    <Dialog open={Boolean(removing)} title="删除 UP 主" onClose={() => setRemoving(null)} actions={<><Button onPress={() => setRemoving(null)}>取消</Button><Button variant="primary" danger busy={remove.isPending} onPress={() => removing && remove.mutate(removing.uid)}>确认删除</Button></>}><p>将删除“{removing?.name || removing?.uid}”以及对应去重状态。此操作无法撤销。</p></Dialog>
  </div>
}

function UPDialog({ value, busy, error, onClose, onSave }: { value?: UP; busy: boolean; error: string; onClose: () => void; onSave: (value: Pick<UP, 'uid' | 'name' | 'enabled'>) => void }) {
  const [uid, setUID] = useState(value?.uid || ''); const [name, setName] = useState(value?.name || ''); const [enabled, setEnabled] = useState(value?.enabled ?? true)
  return <Dialog open onClose={onClose} title={value ? '编辑 UP 主' : '添加 UP 主'} actions={<><Button onPress={onClose}>取消</Button><Button variant="primary" busy={busy} isDisabled={!uid} onPress={() => onSave({ uid, name, enabled })}>保存</Button></>}><div className="form-stack">{error && <Alert tone="danger">{error}</Alert>}<TextField label="UID" value={uid} onChange={setUID} disabled={Boolean(value)} required inputMode="numeric" /><TextField label="备注名" value={name} onChange={setName} /><SwitchField checked={enabled} onChange={setEnabled}>启用轮询</SwitchField></div></Dialog>
}
