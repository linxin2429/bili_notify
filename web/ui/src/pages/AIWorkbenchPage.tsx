import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { Link } from 'react-router-dom'
import { useSession } from '../modules/session'
import { apiErrorMessage } from '../shared/api/errors'
import { queries } from '../shared/api/query'
import { resources } from '../shared/api/resources'
import type { AIJob } from '../shared/api/types'
import { Alert, Badge, Button, Card, EmptyState, LoadingState, PageError, PageHeader, SelectField, TextField, useNotify } from '../shared/ui'
import { Dialog } from '../shared/ui/Dialog'

const labels = { queued: '排队中', running: '处理中', succeeded: '已完成', failed: '失败', canceled: '已取消', skipped: '已跳过' } as const

export function AIWorkbenchPage() {
  const profiles = useQuery(queries.aiProfiles()); const prompts = useQuery(queries.aiPrompts()); const jobs = useQuery(queries.aiJobs({ limit: 50 })); const worker = useQuery(queries.aiStatus())
  const [kind, setKind] = useState<'transcription' | 'summary'>('transcription'); const [bvid, setBVID] = useState(''); const [page, setPage] = useState('0'); const [text, setText] = useState(''); const [source, setSource] = useState(''); const [profile, setProfile] = useState(''); const [prompt, setPrompt] = useState(''); const [selected, setSelected] = useState(''); const [deleting, setDeleting] = useState<string | null>(null)
  const { csrf } = useSession(); const notify = useNotify(); const client = useQueryClient(); const invalidate = () => void client.invalidateQueries({ queryKey: ['ai-jobs'] })
  const submit = useMutation({ mutationFn: ({ profileID, promptID }: { profileID: string; promptID: string }) => kind === 'transcription' ? resources.createAITranscription(csrf, { client_request_id: crypto.randomUUID(), bvid: bvid.trim(), page: Number(page) || undefined, profile_id: profileID }) : resources.createAISummary(csrf, { client_request_id: crypto.randomUUID(), ...(source ? { transcription_job_id: source } : { text: text.trim() }), profile_id: profileID, prompt_id: promptID }), onSuccess: job => { setSelected(job.id); notify('任务已提交', 'success'); invalidate() }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const action = useMutation({
    mutationFn: async ({ id, verb }: { id: string; verb: 'cancel' | 'retry' | 'delete' }) => {
      if (verb === 'cancel') await resources.cancelAIJob(csrf, id)
      else if (verb === 'retry') await resources.retryAIJob(csrf, id)
      else await resources.deleteAIJob(csrf, id)
    },
    onSuccess: (_data, variables) => {
      if (variables.verb === 'delete') {
        setDeleting(null)
        if (selected === variables.id) setSelected('')
      }
      notify('任务状态已更新', 'success')
      invalidate()
    },
    onError: error => notify(apiErrorMessage(error), 'danger'),
  })
  if (profiles.isPending || prompts.isPending || jobs.isPending || worker.isPending) return <LoadingState label="正在加载 AI 工作台" />
  if (profiles.error || prompts.error || jobs.error || worker.error) {
    return <PageError error={profiles.error || prompts.error || jobs.error || worker.error} retry={() => { void profiles.refetch(); void prompts.refetch(); void jobs.refetch(); void worker.refetch() }} />
  }
  const candidates = profiles.data.filter(item => item.kind === (kind === 'summary' ? 'text' : 'transcription')); const profileID = candidates.some(item => item.id === profile) ? profile : candidates.find(item => item.default)?.id || candidates[0]?.id || ''; const promptID = prompts.data.some(item => item.id === prompt) ? prompt : prompts.data.find(item => item.default)?.id || prompts.data[0]?.id || ''
  const sources = jobs.data.items.filter(item => item.kind === 'transcription' && item.state === 'succeeded')
  return <div className="page-stack"><PageHeader title="AI 工作台" subtitle="转写和总结由独立 Worker 异步执行，关闭页面不会中断任务。" />
    {!worker.data.connected && <Alert tone="warning">AI Worker 当前不可用；新任务将保留在队列中。</Alert>}
    <div className="tabs" role="tablist" aria-label="任务类型"><Button role="tab" aria-selected={kind === 'transcription'} className={kind === 'transcription' ? 'tab--active' : ''} onPress={() => { setKind('transcription'); setProfile('') }}>视频转写</Button><Button role="tab" aria-selected={kind === 'summary'} className={kind === 'summary' ? 'tab--active' : ''} onPress={() => { setKind('summary'); setProfile('') }}>文本总结</Button></div>
    <Card><form className="form-stack" onSubmit={event => { event.preventDefault(); submit.mutate({ profileID, promptID }) }}>
      {kind === 'transcription' ? <><TextField label="BVID" value={bvid} onChange={setBVID} required description="默认处理全部分 P。" /><TextField label="分 P" value={page} onChange={setPage} type="number" description="0 表示全部分 P。" /></> : <><SelectField label="来源转写（可选）" value={source} onChange={setSource} options={[{ value: '', label: '直接输入文本' }, ...sources.map(item => ({ value: item.id, label: `转写 ${item.id.slice(0, 8)}` }))]} />{!source && <TextField label="待总结文本" value={text} onChange={setText} multiline required />}</>}
      <SelectField label="模型配置档" value={profileID} onChange={setProfile} disabled={!candidates.length} options={candidates.map(item => ({ value: item.id, label: `${item.name} · ${item.model}` }))} />{kind === 'summary' && <SelectField label="提示词模板" value={promptID} onChange={setPrompt} disabled={!prompts.data.length} options={prompts.data.map(item => ({ value: item.id, label: item.name }))} />}
      {!candidates.length || (kind === 'summary' && !prompts.data.length) ? <Alert tone="warning">请先在 <Link to="/ai-settings">AI 设置</Link> 中补齐模型配置档和提示词。</Alert> : <Button type="submit" variant="primary" busy={submit.isPending}>提交任务</Button>}
    </form></Card>
    <section><h2>任务记录</h2>{!jobs.data.items.length ? <EmptyState title="还没有 AI 任务" icon="✦" /> : <div className="list-stack">{jobs.data.items.map(job => <Card key={job.id} className="ai-job-card"><button type="button" className="ai-job-card__main" onClick={() => setSelected(selected === job.id ? '' : job.id)}><div><div className="card-title-inline"><Badge tone={tone(job)}>{labels[job.state]}</Badge><strong>{job.kind === 'transcription' ? '视频转写' : '文本总结'}</strong></div><p className="muted">{job.stage} · 尝试 {job.attempts} 次</p></div><progress max="100" value={job.progress} aria-label={`任务进度 ${job.progress}%`} /></button>{selected === job.id && <JobDetail job={job} onAction={verb => { if (verb === 'delete') setDeleting(job.id); else action.mutate({ id: job.id, verb }) }} />}</Card>)}</div>}</section>
    <Dialog open={Boolean(deleting)} title="删除 AI 任务记录" onClose={() => setDeleting(null)} actions={<><Button onPress={() => setDeleting(null)}>取消</Button><Button variant="primary" danger busy={action.isPending} onPress={() => deleting && action.mutate({ id: deleting, verb: 'delete' })}>确认删除</Button></>}><p>删除后无法从管理台恢复该任务结果。</p></Dialog>
  </div>
}

function JobDetail({ job, onAction }: { job: AIJob; onAction: (verb: 'cancel' | 'retry' | 'delete') => void }) {
  const detail = useQuery(queries.aiJob(job.id)); const value = detail.data || job
  return <div className="ai-job-detail">{value.last_error && <Alert tone="danger">{value.error_code && <strong>{value.error_code}：</strong>}{value.last_error}</Alert>}{value.result && 'pages' in value.result && <div className="transcript"><h3>{value.result.title}</h3>{value.result.pages.map(item => <section key={item.page}><h3>P{item.page} · {item.title}</h3>{item.segments.map((segment, index) => <p key={`${segment.start_ms}-${index}`}><a target="_blank" rel="noreferrer" href={`https://www.bilibili.com/video/${value.result && 'bvid' in value.result ? value.result.bvid : ''}?p=${item.page}&t=${Math.floor(segment.start_ms / 1000)}`}>{formatTime(segment.start_ms)}</a><span>{segment.text}</span></p>)}</section>)}</div>}{value.result && 'markdown' in value.result && <div className="summary-result"><h3>总结结果</h3><pre>{value.result.markdown}</pre></div>}<div className="button-row">{['queued', 'running'].includes(value.state) && <Button danger onPress={() => onAction('cancel')}>取消任务</Button>}{['failed', 'canceled', 'skipped'].includes(value.state) && <Button variant="outline" onPress={() => onAction('retry')}>重新执行</Button>}{['succeeded', 'failed', 'canceled', 'skipped'].includes(value.state) && <Button danger onPress={() => onAction('delete')}>删除记录</Button>}</div></div>
}

function formatTime(ms: number) { const seconds = Math.floor(ms / 1000); return `${Math.floor(seconds / 60).toString().padStart(2, '0')}:${(seconds % 60).toString().padStart(2, '0')}` }
function tone(job: AIJob): 'neutral' | 'success' | 'warning' | 'danger' { return job.state === 'succeeded' ? 'success' : job.state === 'failed' ? 'danger' : ['queued', 'running'].includes(job.state) ? 'warning' : 'neutral' }
