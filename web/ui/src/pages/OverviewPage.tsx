import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { resources } from '../shared/api/resources'
import { queries, queryKeys } from '../shared/api/query'
import { apiErrorMessage } from '../shared/api/errors'
import { Alert, Badge, Button, Card, LoadingState, PageError, PageHeader, useNotify } from '../shared/ui'
import { Dialog } from '../shared/ui/Dialog'
import { formatDate, loginLabel } from '../shared/lib/presentation'
import { useSession } from '../modules/session'
import { useState } from 'react'

export function OverviewPage() {
  const runtime = useQuery(queries.runtime())
  const settings = useQuery(queries.settings())
  const accounts = useQuery(queries.accounts())
  const sources = useQuery(queries.sources())
  const channels = useQuery(queries.channels())
  const login = useQuery(queries.biliLogin())
  const { csrf } = useSession()
  const client = useQueryClient()
  const notify = useNotify()
  const [confirmLogout, setConfirmLogout] = useState(false)

  const refreshLogin = () => {
    void client.invalidateQueries({ queryKey: queryKeys.biliLogin })
    void client.invalidateQueries({ queryKey: queryKeys.runtime })
    void client.invalidateQueries({ queryKey: queryKeys.accounts })
  }
  const start = useMutation({ mutationFn: () => resources.startBiliLogin(csrf), onSuccess: refreshLogin, onError: error => notify(apiErrorMessage(error), 'danger') })
  const cancel = useMutation({ mutationFn: (id: string) => resources.cancelBiliLogin(csrf, id), onSuccess: refreshLogin, onError: error => notify(apiErrorMessage(error), 'danger') })
  const logout = useMutation({
    mutationFn: () => resources.deleteBilibiliSession(csrf),
    onSuccess: () => { setConfirmLogout(false); notify('已退出 B 站登录', 'success'); refreshLogin() },
    onError: error => notify(apiErrorMessage(error), 'danger'),
  })

  if (runtime.isPending) return <LoadingState label="正在读取运行状态" />
  if (runtime.error) return <PageError error={runtime.error} retry={() => void runtime.refetch()} />

  const run = runtime.data
  const status = run.status
  const configuredAccounts = accounts.data || []
  const configuredSources = sources.data || []
  const configuredChannels = channels.data || []
  const zsxqAccount = configuredAccounts.find(account => account.platform === 'zsxq')
  const enabledBili = configuredSources.some(source => source.enabled && source.platform === 'bilibili')
  const enabledZsxq = configuredSources.some(source => source.enabled && source.platform === 'zsxq')
  const hasEnabledChannel = configuredChannels.some(channel => channel.enabled)
  const hasEnabledSource = configuredSources.some(source => source.enabled)
  const biliAuthOK = !enabledBili || status.auth_valid
  const zsxqAuthOK = !enabledZsxq || zsxqAccount?.status === 'connected'

  return <div className="page-stack">
    <PageHeader title="运行概览" subtitle="分别确认两个平台的账号、采集源、回补与可靠投递状态。" />
    <Alert tone={status.ready ? 'success' : 'warning'}>
      <h2>{status.ready ? '服务已就绪' : '服务尚未就绪'}</h2>
      <p>{status.ready ? '采集和投递所需条件均由服务端确认有效。' : '请完成下方启动检查，服务端会在条件满足后切换为就绪。'}</p>
    </Alert>

    <div className="metric-grid">
      <Metric label="B站登录" value={status.auth_valid ? '有效' : '未登录'} detail={status.bili_account ? `${status.bili_account.name} · UID ${status.bili_account.uid}` : undefined} />
      <Metric label="知识星球登录" value={zsxqAccount?.status === 'connected' ? '有效' : '未登录'} detail={zsxqAccount?.display_name || zsxqAccount?.masked_phone} href="/sources" />
      <Metric label="采集源" value={accounts.isError || sources.isError ? '—' : String(configuredSources.length)} detail={sources.isError ? '加载失败' : `${configuredSources.filter(source => source.enabled).length} 个已启用`} href="/sources" />
      <Metric label="待投递" value={String(status.outbox_depth)} detail={status.oldest_delivery ? `最早 ${formatDate(status.oldest_delivery, run.timezone)}` : '队列为空'} href={status.outbox_depth > 0 ? '/deliveries?state=blocked' : '/deliveries'} />
    </div>

    {status.risk_paused_until && <Alert tone="danger">B站风控暂停至 {formatDate(status.risk_paused_until, run.timezone)}，程序不会尝试绕过风控。</Alert>}
    {settings.data && <p className="muted">B 站每 {settings.data.bilibili_dynamic_interval_sec} 秒轮询 · 知识星球每 {settings.data.zsxq_dynamic_interval_sec} 秒轮询 · 两个平台独立限流与暂停</p>}
    {settings.isError && <Alert tone="warning">运行参数加载失败，轮询间隔暂时不可用。<Button variant="outline" onPress={() => void settings.refetch()}>重试</Button></Alert>}

    <div className="two-column">
      <Card>
        <div className="section-heading">
          <div>
            <h2>B站账号</h2>
            <p>{status.bili_account ? `${status.bili_account.name || '已登录账号'} · UID ${status.bili_account.uid}` : '使用哔哩哔哩 App 扫码建立网页会话'}</p>
          </div>
          <span className="section-icon">▦</span>
        </div>
        {login.isError && <Alert tone="danger">登录状态加载失败：{apiErrorMessage(login.error)} <Button variant="outline" onPress={() => void login.refetch()}>重试</Button></Alert>}
        {!login.isError && (!login.data || ['success', 'expired'].includes(login.data.status)) && (
          <div className="button-row">
            <Button variant="primary" busy={start.isPending} onPress={() => start.mutate()}>生成登录二维码</Button>
            {status.auth_valid && <Button danger onPress={() => setConfirmLogout(true)}>退出登录</Button>}
          </div>
        )}
        {!login.isError && login.data && !['success', 'expired'].includes(login.data.status) && (
          <div className="qr-panel">
            <Badge tone={login.data.status === 'scanned' ? 'info' : 'warning'}>{loginLabel(login.data.status)}</Badge>
            {login.data.qr_data_url && <img src={login.data.qr_data_url} className="qr-image" alt="哔哩哔哩登录二维码" />}
            <p>二维码有效至 {formatDate(login.data.expires_at, run.timezone)}</p>
            <Button onPress={() => cancel.mutate(login.data!.id)}>取消本次登录</Button>
          </div>
        )}
      </Card>

      <Card>
        <h2>启动检查</h2>
        {(accounts.isError || sources.isError || channels.isError) && (
          <Alert tone="warning">
            部分检查项依赖的数据加载失败。
            <Button variant="outline" onPress={() => { void accounts.refetch(); void sources.refetch(); void channels.refetch() }}>重试</Button>
          </Alert>
        )}
        <Checklist done={biliAuthOK} action={!biliAuthOK ? undefined : undefined} hint={!biliAuthOK ? '请在左侧扫码登录 B 站' : undefined}>
          已启用 B 站来源的账号有效
        </Checklist>
        <Checklist
          done={zsxqAuthOK}
          action={!zsxqAuthOK ? <Link className="button button--outline" to="/integrations/zsxq-login">去登录知识星球</Link> : undefined}
        >
          已启用知识星球来源的账号有效
        </Checklist>
        <Checklist
          done={hasEnabledChannel}
          action={!hasEnabledChannel && !channels.isError ? <Link className="button button--outline" to="/channels">配置通知渠道</Link> : undefined}
        >
          至少一个通知渠道已启用
        </Checklist>
        <Checklist
          done={hasEnabledSource}
          action={!hasEnabledSource && !sources.isError ? <Link className="button button--outline" to="/sources">添加采集源</Link> : undefined}
        >
          至少一个采集源已启用
        </Checklist>
        <hr />
        <p className="muted">最后成功采集：{status.last_success_at ? formatDate(status.last_success_at, run.timezone) : '尚无记录'}</p>
      </Card>
    </div>

    <Dialog
      open={confirmLogout}
      title="退出 B 站登录"
      onClose={() => setConfirmLogout(false)}
      actions={<>
        <Button onPress={() => setConfirmLogout(false)}>取消</Button>
        <Button variant="primary" danger busy={logout.isPending} onPress={() => logout.mutate()}>确认退出</Button>
      </>}
    >
      <p>退出后已启用的 B 站采集源将无法继续拉取，直到重新扫码登录。投递队列中的任务不受影响。</p>
    </Dialog>
  </div>
}

function Metric({ label, value, detail, href }: { label: string; value: string; detail?: string; href?: string }) {
  const body = <><span>{label}</span><strong>{value}</strong>{detail && <small>{detail}</small>}</>
  if (href) return <Card className="metric"><Link className="metric-link" to={href}>{body}</Link></Card>
  return <Card className="metric">{body}</Card>
}

function Checklist({ done, children, action, hint }: { done: boolean; children: React.ReactNode; action?: React.ReactNode; hint?: string }) {
  return <div className="checklist-row">
    <p className="checklist"><span aria-hidden="true">{done ? '✓' : '○'}</span>{children}</p>
    {!done && (action || (hint && <span className="muted">{hint}</span>))}
  </div>
}