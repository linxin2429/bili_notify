import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import type { RuntimeSettings } from '../types'
import { queries, queryKeys } from '../shared/api/query'
import { resources } from '../shared/api/resources'
import { sessionAPI } from '../shared/api/session'
import { apiErrorMessage } from '../shared/api/errors'
import { useSession } from '../modules/session/session'
import { useThemePreference } from '../app/theme'
import { Alert, Button, Card, LoadingState, PageError, PageHeader, SelectField, SwitchField, TextField, useNotify } from '../shared/ui'
import { parseRuntimeSettingsForm, runtimeSettingsToForm, type RuntimeSettingsForm } from './settings-form'

export function SettingsPage() {
  const settings = useQuery(queries.settings())
  if (settings.isPending) return <LoadingState />
  if (settings.error) return <PageError error={settings.error} retry={() => void settings.refetch()} />
  return <SettingsEditor key={JSON.stringify(settings.data)} initial={settings.data} />
}

function SettingsEditor({ initial }: { initial: RuntimeSettings }) {
  const { csrf } = useSession(); const client = useQueryClient(); const notify = useNotify(); const { preference, setPreference } = useThemePreference()
  const [form, setForm] = useState<RuntimeSettingsForm>(() => runtimeSettingsToForm(initial)); const [formError, setFormError] = useState(''); const [current, setCurrent] = useState(''); const [replacement, setReplacement] = useState(''); const [confirm, setConfirm] = useState('')
  const save = useMutation({ mutationFn: (value: RuntimeSettings) => resources.updateSettings(csrf, value), onMutate: () => client.cancelQueries({ queryKey: queryKeys.settings }), onSuccess: async () => { await client.invalidateQueries({ queryKey: queryKeys.settings }); void client.invalidateQueries({ queryKey: queryKeys.runtime }); notify('运行参数已保存', 'success') }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const password = useMutation({ mutationFn: () => sessionAPI.changePassword(csrf, current, replacement), onSuccess: () => { client.removeQueries(); client.setQueryData(queryKeys.session, { setup_required: false, authenticated: false }) }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const patch = <K extends keyof RuntimeSettingsForm>(key: K, value: RuntimeSettingsForm[K]) => setForm(state => ({ ...state, [key]: value }))
  const submit = () => { const parsed = parseRuntimeSettingsForm(form); if (!parsed.ok) { setFormError(parsed.error); return } setFormError(''); save.mutate(parsed.value) }
  const changePassword = () => { if (replacement !== confirm) { notify('两次输入的新密码不一致', 'danger'); return } password.mutate() }
  return <div className="page-stack"><PageHeader title="设置" subtitle="运行参数由服务端持久化；主题偏好只保存在当前浏览器。" />
    <SettingsSection title="采集节奏" description="控制动态轮询、请求速率与单轮翻页上限。" open><div className="settings-grid"><NumberField label="轮询间隔（秒）" value={form.pollSec} set={value => patch('pollSec', value)} /><NumberField label="请求速率（次/秒）" value={form.requestRate} set={value => patch('requestRate', value)} /><NumberField label="请求并发数" value={form.concurrency} set={value => patch('concurrency', value)} /><NumberField label="动态翻页上限" value={form.maxDynamicPages} set={value => patch('maxDynamicPages', value)} /><NumberField label="关注关系刷新（秒）" value={form.relationRefreshSec} set={value => patch('relationRefreshSec', value)} /><NumberField label="空间校验间隔（秒）" value={form.spaceReconcileSec} set={value => patch('spaceReconcileSec', value)} /><NumberField label="风控暂停（秒）" value={form.riskPauseSec} set={value => patch('riskPauseSec', value)} /></div></SettingsSection>
    <SettingsSection title="评论监控" description="控制评论树读取深度和批次频率。"><SwitchField checked={form.commentEnabled} onChange={value => patch('commentEnabled', value)}>启用评论监控</SwitchField><div className="settings-grid"><NumberField label="跟踪内容数 N" value={form.commentTrackN} set={value => patch('commentTrackN', value)} /><NumberField label="根评论页数" value={form.commentRootPages} set={value => patch('commentRootPages', value)} /><NumberField label="子评论页数" value={form.commentReplyPages} set={value => patch('commentReplyPages', value)} /><NumberField label="批次间隔（秒）" value={form.commentBatchSec} set={value => patch('commentBatchSec', value)} /></div></SettingsSection>
    <SettingsSection title="投递与积压" description="控制投递并发、积压告警和五阶段重试。"><div className="settings-grid"><NumberField label="投递并发数" value={form.deliveryConcurrency} set={value => patch('deliveryConcurrency', value)} /><NumberField label="积压条数阈值" value={form.backlogAlertCount} set={value => patch('backlogAlertCount', value)} /><NumberField label="积压时长阈值（秒）" value={form.backlogAlertAgeSec} set={value => patch('backlogAlertAgeSec', value)} />{form.retryDelaysSec.map((value, index) => <NumberField key={index} label={`第 ${index + 1} 阶段重试（秒）`} value={value} set={next => { const values = [...form.retryDelaysSec] as RuntimeSettingsForm['retryDelaysSec']; values[index] = next; patch('retryDelaysSec', values) }} />)}</div></SettingsSection>
    <SettingsSection title="日志与保留" description="设置运行日志级别和审计记录保留期。"><div className="settings-grid"><SelectField label="日志级别" value={form.logLevel} onChange={value => patch('logLevel', value as RuntimeSettings['log_level'])} options={['debug', 'info', 'warn', 'error'].map(value => ({ value, label: value }))} /><NumberField label="审计日志保留（天）" value={form.auditRetentionDays} set={value => patch('auditRetentionDays', value)} /></div></SettingsSection>
    {formError && <Alert tone="danger">{formError}</Alert>}<Button variant="primary" busy={save.isPending} onPress={submit}>保存运行设置</Button>
    <SettingsSection title="外观" description="CSS Variables 切换主题，不重建 React 组件树。"><div className="button-row">{(['system', 'light', 'dark'] as const).map(value => <Button key={value} variant={preference === value ? 'primary' : 'outline'} onPress={() => setPreference(value)}>{value === 'system' ? '跟随系统' : value === 'light' ? '亮色' : '暗色'}</Button>)}</div></SettingsSection>
    <SettingsSection title="修改管理员密码" description="修改成功后，所有设备的会话都会立即失效。"><div className="form-stack form-narrow"><TextField label="当前密码" type="password" value={current} onChange={setCurrent} autoComplete="current-password" /><TextField label="新密码" type="password" value={replacement} onChange={setReplacement} autoComplete="new-password" description="至少 12 个字节" /><TextField label="确认新密码" type="password" value={confirm} onChange={setConfirm} autoComplete="new-password" /><Button variant="primary" busy={password.isPending} isDisabled={!current || !replacement} onPress={changePassword}>修改密码</Button></div></SettingsSection>
  </div>
}

function SettingsSection({ title, description, children, open = false }: { title: string; description: string; children: React.ReactNode; open?: boolean }) { return <Card><details open={open || undefined} className="settings-section"><summary><div><h2>{title}</h2><p>{description}</p></div><span aria-hidden="true">⌄</span></summary><div className="settings-section__body">{children}</div></details></Card> }
function NumberField({ label, value, set }: { label: string; value: string; set: (value: string) => void }) { return <TextField label={label} value={value} onChange={set} inputMode="decimal" /> }
