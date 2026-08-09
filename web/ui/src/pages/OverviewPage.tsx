import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { resources } from '../shared/api/resources'
import { queries, queryKeys } from '../shared/api/query'
import { apiErrorMessage } from '../shared/api/errors'
import { Alert, Badge, Button, Card, LoadingState, PageError, PageHeader, useNotify } from '../shared/ui'
import { formatDate, loginLabel } from '../shared/lib/presentation'
import { useSession } from '../modules/session'

export function OverviewPage() {
  const runtime = useQuery(queries.runtime()); const settings = useQuery(queries.settings()); const ups = useQuery(queries.ups())
  const channels = useQuery(queries.channels()); const login = useQuery(queries.biliLogin())
  const { csrf } = useSession(); const client = useQueryClient(); const notify = useNotify()
  const refreshLogin = () => { void client.invalidateQueries({ queryKey: queryKeys.biliLogin }); void client.invalidateQueries({ queryKey: queryKeys.runtime }) }
  const start = useMutation({ mutationFn: () => resources.startBiliLogin(csrf), onSuccess: refreshLogin, onError: error => notify(apiErrorMessage(error), 'danger') })
  const cancel = useMutation({ mutationFn: (id: string) => resources.cancelBiliLogin(csrf, id), onSuccess: refreshLogin, onError: error => notify(apiErrorMessage(error), 'danger') })
  const all = [runtime, settings, ups, channels, login]
  if (all.some(query => query.isPending)) return <LoadingState label="正在读取运行状态" />
  const failed = all.find(query => query.isError)
  if (failed?.error) return <PageError error={failed.error} retry={() => void Promise.all(all.map(query => query.refetch()))} />
  const run = runtime.data!; const status = run.status; const configuredUps = ups.data!; const configuredChannels = channels.data!
  return <div className="page-stack"><PageHeader title="运行概览" subtitle="第一眼确认服务是否正在发现并可靠投递动态。" />
    <Alert tone={status.ready ? 'success' : 'warning'}><h2>{status.ready ? '服务已就绪' : '服务尚未就绪'}</h2><p>{status.ready ? '采集和投递所需条件均由服务端确认有效。' : '请完成下方启动检查，服务端会在条件满足后切换为就绪。'}</p></Alert>
    <div className="metric-grid"><Metric label="B站登录" value={status.auth_valid ? '有效' : '未登录'} detail={status.bili_account ? `${status.bili_account.name} · UID ${status.bili_account.uid}` : undefined} /><Metric label="UP 主" value={String(status.up_count)} detail={`${configuredUps.filter(up => up.enabled).length} 个已启用`} /><Metric label="通知渠道" value={String(status.channel_count)} detail={`${configuredChannels.filter(channel => channel.enabled).length} 个已启用`} /><Metric label="待投递" value={String(status.outbox_depth)} detail={status.oldest_delivery ? `最早 ${formatDate(status.oldest_delivery, run.timezone)}` : '队列为空'} /></div>
    {status.risk_paused_until && <Alert tone="danger">B站风控暂停至 {formatDate(status.risk_paused_until, run.timezone)}，程序不会尝试绕过风控。</Alert>}
    <p className="muted">当前采集参数：每 {settings.data!.poll_interval_sec} 秒轮询 · {settings.data!.request_rate} 请求/秒 · 并发 {settings.data!.request_concurrency} · 评论监控{settings.data!.comment_enabled ? '开' : '关'}</p>
    <div className="two-column"><Card><div className="section-heading"><div><h2>B站账号</h2><p>{status.bili_account ? `${status.bili_account.name || '已登录账号'} · UID ${status.bili_account.uid}` : '使用哔哩哔哩 App 扫码建立网页会话'}</p></div><span className="section-icon">▦</span></div>{!login.data || ['success', 'expired'].includes(login.data.status) ? <Button variant="primary" busy={start.isPending} onPress={() => start.mutate()}>生成登录二维码</Button> : <div className="qr-panel"><Badge tone={login.data.status === 'scanned' ? 'info' : 'warning'}>{loginLabel(login.data.status)}</Badge>{login.data.qr_data_url && <img src={login.data.qr_data_url} className="qr-image" alt="哔哩哔哩登录二维码" />}<p>二维码有效至 {formatDate(login.data.expires_at, run.timezone)}</p><Button onPress={() => cancel.mutate(login.data!.id)}>取消本次登录</Button></div>}</Card>
      <Card><h2>启动检查</h2><Checklist done={status.auth_valid}>B站账号已登录</Checklist><Checklist done={configuredChannels.some(channel => channel.enabled)}>至少一个通知渠道已启用</Checklist><Checklist done={configuredUps.some(up => up.enabled)}>至少一个 UP 主已启用</Checklist><hr /><p className="muted">最后成功采集：{status.last_success_at ? formatDate(status.last_success_at, run.timezone) : '尚无记录'}</p></Card></div>
  </div>
}

function Metric({ label, value, detail }: { label: string; value: string; detail?: string }) { return <Card className="metric"><span>{label}</span><strong>{value}</strong>{detail && <small>{detail}</small>}</Card> }
function Checklist({ done, children }: { done: boolean; children: React.ReactNode }) { return <p className="checklist"><span aria-hidden="true">{done ? '✓' : '!'}</span>{children}</p> }
