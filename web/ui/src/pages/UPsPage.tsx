import { useEffect, useState } from 'react'
import Add from '@mui/icons-material/Add'
import Delete from '@mui/icons-material/Delete'
import Edit from '@mui/icons-material/Edit'
import People from '@mui/icons-material/People'
import { Alert, Box, Button, Card, CardContent, Chip, Dialog, DialogActions, DialogContent, DialogTitle, FormControlLabel, Stack, Switch, TextField, Typography, useMediaQuery } from '@mui/material'
import type { AdminAPI } from '../api'
import { applyUPDeletion, applyUPMutation } from '../dashboard'
import { followStateLabel, formatDate } from '../presentation'
import type { UP } from '../types'
import { EmptyState, PageHeader, type RunMutation } from '../app/shared'

export function UPsPage({ ups, timeZone, api, runMutation }: { ups: UP[]; timeZone: string; api: AdminAPI; runMutation: RunMutation }) {
  const [editing, setEditing] = useState<UP | null | undefined>(undefined)
  const mobile = useMediaQuery(theme => theme.breakpoints.down('sm'))
  const save = async (value: { uid: string; name: string; enabled: boolean }) => {
    await runMutation(() => editing ? api.updateUP(value) : api.createUP(value), applyUPMutation)
    setEditing(undefined)
  }
  const remove = async (uid: string) => {
    if (!confirm('删除该 UP 主及其去重状态？')) return
    try { await runMutation(() => api.deleteUP(uid), current => applyUPDeletion(current, uid)) } catch { /* shared handler reports it */ }
  }
  return <Stack spacing={3}><PageHeader title="UP 主" subtitle="管理需要轮询的公开账号；首次采集只建立基线。" action={<Button variant="contained" startIcon={<Add />} onClick={() => setEditing(null)}>添加 UP 主</Button>} />
    {ups.length === 0 ? <EmptyState icon={<People />} title="尚未添加 UP 主" action={<Button variant="contained" onClick={() => setEditing(null)}>添加第一个 UP 主</Button>} /> :
      <Box sx={{ display: 'grid', gridTemplateColumns: { xs: '1fr', lg: 'repeat(2, 1fr)' }, gap: 2 }}>{ups.map(up => <Card key={up.uid}><CardContent><Stack spacing={2}><Stack direction="row" justifyContent="space-between" alignItems="start"><Box><Typography variant="h6" fontWeight={800}>{up.name || `UID ${up.uid}`}</Typography><Typography color="text.secondary">UID {up.uid}</Typography></Box><Chip label={up.enabled ? '已启用' : '已停用'} color={up.enabled ? 'success' : 'default'} /></Stack><Stack direction="row" spacing={1} flexWrap="wrap"><Chip size="small" label={up.baseline_ready ? '基线已建立' : '等待基线'} /><Chip size="small" label={followStateLabel(up.follow_state)} color={up.follow_state === 'followed' ? 'success' : up.follow_state === 'unknown' ? 'warning' : 'default'} /><Chip size="small" label={up.collection_route === 'feed_all' ? '综合流采集' : '空间采集'} color={up.collection_route === 'feed_all' ? 'info' : 'default'} /><Chip size="small" label={`连续失败 ${up.consecutive_fail} 次`} color={up.consecutive_fail ? 'warning' : 'default'} /></Stack>{up.last_error && <Alert severity="error">{up.last_error}</Alert>}<Typography variant="body2" color="text.secondary">关注关系检查：{up.follow_checked_at ? formatDate(up.follow_checked_at, timeZone) : '尚未检查'}</Typography><Typography variant="body2" color="text.secondary">最后成功：{up.last_success_at ? formatDate(up.last_success_at, timeZone) : '尚无记录'}</Typography><Stack direction="row" spacing={1}><Button startIcon={<Edit />} onClick={() => setEditing(up)}>编辑</Button><Button color="error" startIcon={<Delete />} onClick={() => void remove(up.uid)}>删除</Button></Stack></Stack></CardContent></Card>)}</Box>}
    <UPDialog open={editing !== undefined} value={editing || undefined} fullScreen={mobile} onClose={() => setEditing(undefined)} onSave={save} />
  </Stack>
}

function UPDialog({ open, value, fullScreen, onClose, onSave }: { open: boolean; value?: UP; fullScreen: boolean; onClose: () => void; onSave: (value: { uid: string; name: string; enabled: boolean }) => Promise<void> }) {
  const [uid, setUID] = useState(''); const [name, setName] = useState(''); const [enabled, setEnabled] = useState(true); const [busy, setBusy] = useState(false); const [error, setError] = useState('')
  useEffect(() => { setUID(value?.uid || ''); setName(value?.name || ''); setEnabled(value?.enabled ?? true); setError('') }, [value, open])
  const submit = async () => {
    setBusy(true); setError('')
    try { await onSave({ uid, name, enabled }) } catch (err) { setError(err instanceof Error ? err.message : '发生未知错误') } finally { setBusy(false) }
  }
  return <Dialog open={open} onClose={onClose} fullScreen={fullScreen} fullWidth maxWidth="sm"><DialogTitle>{value ? '编辑 UP 主' : '添加 UP 主'}</DialogTitle><DialogContent><Stack spacing={2} sx={{ pt: 1 }}>{error && <Alert severity="error">{error}</Alert>}<TextField label="UID" value={uid} onChange={event => setUID(event.target.value)} disabled={Boolean(value)} inputMode="numeric" required /><TextField label="备注名" value={name} onChange={event => setName(event.target.value)} /><FormControlLabel control={<Switch checked={enabled} onChange={event => setEnabled(event.target.checked)} />} label="启用轮询" /></Stack></DialogContent><DialogActions><Button onClick={onClose}>取消</Button><Button variant="contained" disabled={busy || !uid} onClick={() => void submit()}>保存</Button></DialogActions></Dialog>
}
