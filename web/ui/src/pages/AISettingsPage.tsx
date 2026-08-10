import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'
import { useSession } from '../modules/session'
import { apiErrorMessage } from '../shared/api/errors'
import { queries } from '../shared/api/query'
import { resources } from '../shared/api/resources'
import type { AIPrompt, AIPromptDraft, AIProfile, AIProfileDraft } from '../shared/api/types'
import { Alert, Badge, Button, Card, LoadingState, PageError, PageHeader, SelectField, SwitchField, TextField, useNotify } from '../shared/ui'

const blankProfile = (kind: AIProfile['kind']): AIProfileDraft => ({ name: '', kind, base_url: 'https://openrouter.ai/api/v1', model: kind === 'transcription' ? 'openai/gpt-transcribe' : 'openai/gpt-5-mini', api_key: '', language: kind === 'transcription' ? 'zh' : '', prompt: '', temperature: 0.2, max_output_tokens: kind === 'text' ? 4096 : 0, context_window_chars: kind === 'text' ? 100000 : 0, timeout_sec: 600, default: false })
const blankPrompt: AIPromptDraft = { name: '', system_prompt: '你是一名严谨的中文内容编辑。', chunk_prompt: '请总结以下内容，保留事实、论点和关键细节：\n\n{{text}}', reduce_prompt: '请将以下分段摘要合并为结构清晰、没有重复的最终摘要：\n\n{{summaries}}', default: false }

export function AISettingsPage() {
  const status = useQuery(queries.aiStatus()); const profiles = useQuery(queries.aiProfiles()); const prompts = useQuery(queries.aiPrompts())
  if (status.isPending || profiles.isPending || prompts.isPending) return <LoadingState label="正在加载 AI 设置" />
  if (status.error || profiles.error || prompts.error) return <PageError error={status.error || profiles.error || prompts.error} />
  return <div className="page-stack"><PageHeader title="AI 设置" subtitle="模型凭据由服务端加密保存；浏览器只会看到凭据是否已配置。" />
    <Card><div className="card-title-inline"><h2>Worker 状态</h2><Badge tone={status.data.connected ? 'success' : 'danger'}>{status.data.connected ? '已连接' : '不可用'}</Badge></div><p className="muted">yt-dlp：{status.data.yt_dlp_available ? '可用' : '不可用'} · FFmpeg：{status.data.ffmpeg_available ? '可用' : '不可用'} · 缓存：{formatBytes(status.data.cache_bytes)}</p>{status.data.last_error && <Alert tone="warning">{status.data.last_error}</Alert>}</Card>
    <ProfileEditor initial={profiles.data} />
    <PromptEditor initial={prompts.data} />
  </div>
}

function ProfileEditor({ initial }: { initial: AIProfile[] }) {
  const { csrf } = useSession(); const notify = useNotify(); const client = useQueryClient(); const [form, setForm] = useState<AIProfileDraft>(() => blankProfile('transcription'))
  const refresh = () => void client.invalidateQueries({ queryKey: ['ai-profiles'] })
  const save = useMutation({ mutationFn: (value: AIProfileDraft) => value.id ? resources.updateAIProfile(csrf, value as AIProfileDraft & { id: string }) : resources.createAIProfile(csrf, value), onSuccess: () => { notify('模型配置档已保存', 'success'); setForm(blankProfile(form.kind)); refresh() }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const remove = useMutation({ mutationFn: (id: string) => resources.deleteAIProfile(csrf, id), onSuccess: () => { notify('模型配置档已删除', 'success'); refresh() }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const test = useMutation({ mutationFn: (id: string) => resources.testAIProfile(csrf, id), onSuccess: () => notify('Worker 连接正常', 'success'), onError: error => notify(apiErrorMessage(error), 'danger') })
  const patch = <K extends keyof AIProfileDraft>(key: K, value: AIProfileDraft[K]) => setForm(state => ({ ...state, [key]: value }))
  const edit = (value: AIProfile) => setForm({
    id: value.id,
    name: value.name,
    kind: value.kind,
    base_url: value.base_url,
    model: value.model,
    api_key: '',
    language: value.language,
    prompt: value.prompt,
    temperature: value.temperature,
    max_output_tokens: value.max_output_tokens,
    context_window_chars: value.context_window_chars,
    timeout_sec: value.timeout_sec,
    default: value.default,
  })
  return <Card><h2>模型配置档</h2><p className="muted">转写和文本模型分开配置。Base URL 使用 OpenAI 兼容 API 根地址；API Key 留空表示编辑时保留原密钥。</p>
    <div className="settings-grid"><SelectField label="用途" value={form.kind} onChange={value => setForm(blankProfile(value as AIProfile['kind']))} options={[{ value: 'transcription', label: '音频转写' }, { value: 'text', label: '文本总结' }]} /><TextField label="名称" value={form.name} onChange={value => patch('name', value)} required /><TextField label="Base URL" value={form.base_url} onChange={value => patch('base_url', value)} required /><TextField label="模型" value={form.model} onChange={value => patch('model', value)} required /><TextField label="API Key" type="password" value={form.api_key || ''} onChange={value => patch('api_key', value)} autoComplete="off" description={form.id ? '留空即保留现有密钥' : '创建时必填'} /><TextField label="超时（秒）" type="number" value={String(form.timeout_sec)} onChange={value => patch('timeout_sec', Number(value))} /></div>
    {form.kind === 'transcription' ? <div className="settings-grid"><TextField label="语言" value={form.language || ''} onChange={value => patch('language', value)} description="例如 zh；留空让模型自动判断。" /><TextField label="转写提示词" value={form.prompt || ''} onChange={value => patch('prompt', value)} /></div> : <div className="settings-grid"><TextField label="温度" type="number" value={String(form.temperature || 0)} onChange={value => patch('temperature', Number(value))} /><TextField label="最大输出 Token" type="number" value={String(form.max_output_tokens || 0)} onChange={value => patch('max_output_tokens', Number(value))} /><TextField label="单段上下文字符数" type="number" value={String(form.context_window_chars || 0)} onChange={value => patch('context_window_chars', Number(value))} /></div>}
    <SwitchField checked={form.default} onChange={value => patch('default', value)}>设为该用途的默认配置</SwitchField><div className="button-row"><Button variant="primary" busy={save.isPending} isDisabled={!form.name || !form.base_url || !form.model || (!form.id && !form.api_key)} onPress={() => save.mutate(form)}>保存</Button>{form.id && <Button variant="outline" onPress={() => setForm(blankProfile(form.kind))}>取消编辑</Button>}</div>
    <div className="list-stack">{initial.map(item => <div className="config-row" key={item.id}><div><strong>{item.name}</strong> <Badge>{item.kind === 'transcription' ? '转写' : '总结'}</Badge>{item.default && <Badge tone="success">默认</Badge>}<p className="muted">{item.model} · {item.base_url} · Key {item.configured_secrets.includes('api_key') ? '已配置' : '未配置'}</p></div><div className="button-row"><Button variant="outline" onPress={() => edit(item)}>编辑</Button><Button variant="outline" busy={test.isPending} onPress={() => test.mutate(item.id)}>检查 Worker</Button><Button danger onPress={() => remove.mutate(item.id)}>删除</Button></div></div>)}</div>
  </Card>
}

function PromptEditor({ initial }: { initial: AIPrompt[] }) {
  const { csrf } = useSession(); const notify = useNotify(); const client = useQueryClient(); const [form, setForm] = useState<AIPromptDraft>(blankPrompt)
  const refresh = () => void client.invalidateQueries({ queryKey: ['ai-prompts'] })
  const save = useMutation({ mutationFn: (value: AIPromptDraft) => value.id ? resources.updateAIPrompt(csrf, value as AIPromptDraft & { id: string }) : resources.createAIPrompt(csrf, value), onSuccess: () => { notify('提示词模板已保存', 'success'); setForm(blankPrompt); refresh() }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const remove = useMutation({ mutationFn: (id: string) => resources.deleteAIPrompt(csrf, id), onSuccess: () => { notify('提示词模板已删除', 'success'); refresh() }, onError: error => notify(apiErrorMessage(error), 'danger') })
  const patch = <K extends keyof AIPromptDraft>(key: K, value: AIPromptDraft[K]) => setForm(state => ({ ...state, [key]: value }))
  return <Card><h2>总结提示词模板</h2><p className="muted">长文本先按段执行 Chunk Prompt，再用 Reduce Prompt 合并。占位符必须分别包含 <code>{'{{text}}'}</code> 和 <code>{'{{summaries}}'}</code>。</p><div className="form-stack"><TextField label="名称" value={form.name} onChange={value => patch('name', value)} required /><TextField label="System Prompt" value={form.system_prompt} onChange={value => patch('system_prompt', value)} multiline /><TextField label="Chunk Prompt" value={form.chunk_prompt} onChange={value => patch('chunk_prompt', value)} multiline required /><TextField label="Reduce Prompt" value={form.reduce_prompt} onChange={value => patch('reduce_prompt', value)} multiline required /><SwitchField checked={form.default} onChange={value => patch('default', value)}>设为默认模板</SwitchField><div className="button-row"><Button variant="primary" busy={save.isPending} onPress={() => save.mutate(form)}>保存</Button>{form.id && <Button variant="outline" onPress={() => setForm(blankPrompt)}>取消编辑</Button>}</div></div><div className="list-stack">{initial.map(item => <div className="config-row" key={item.id}><div><strong>{item.name}</strong>{item.default && <Badge tone="success">默认</Badge>}</div><div className="button-row"><Button variant="outline" onPress={() => setForm(item)}>编辑</Button><Button danger onPress={() => remove.mutate(item.id)}>删除</Button></div></div>)}</div></Card>
}

function formatBytes(value: number) { if (!value) return '0 B'; const units = ['B', 'KiB', 'MiB', 'GiB']; const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1); return `${(value / 1024 ** index).toFixed(index ? 1 : 0)} ${units[index]}` }
